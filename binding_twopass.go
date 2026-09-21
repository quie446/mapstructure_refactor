package mapstructure

import (
	"fmt"
	"strings"

	"github.com/go-viper/mapstructure/v2/internal/errors"
)

// LockedBinding is a frozen binding conclusion tied to the exact run it was
// produced from. Re-running on another machine must reproduce it; changing the
// error strategy or the weak-conversion switch invalidates the lock and forces
// a fresh gate pass.
type LockedBinding struct {
	// WeaklyTyped records the weak-conversion switch the lock was taken under.
	WeaklyTyped bool

	// Fingerprint is the deterministic, ordered text of every finding.
	Fingerprint string

	// Count is the number of findings locked.
	Count int
}

// Lock produces a deterministic fingerprint for the report. A nil/absent report
// is rejected: a run that did not happen is never locked.
func (r *BindingReport) Lock(weaklyTyped bool) (LockedBinding, error) {
	if r == nil {
		return LockedBinding{}, errors.New("cannot lock a nil binding report")
	}

	var b strings.Builder

	for _, e := range r.errs {
		b.WriteString(string(e.Kind))
		b.WriteByte('|')
		b.WriteString(e.Path)
		b.WriteByte('|')
		b.WriteString(e.Reason)
		b.WriteByte('\n')
	}

	return LockedBinding{
		WeaklyTyped: weaklyTyped,
		Fingerprint: b.String(),
		Count:       len(r.errs),
	}, nil
}

// VerifyLock re-runs the binding on the same input and refuses to pass when the
// conclusion, finding count or weak-conversion switch drifts.
func VerifyLock(cfg *BindingConfig, lock LockedBinding, input map[string]any, result any) error {
	if cfg == nil {
		return errors.New("binding config must not be nil")
	}

	if cfg.DecoderConfig.WeaklyTypedInput != lock.WeaklyTyped {
		return fmt.Errorf(
			"binding lock invalidated: weak-conversion switch changed (locked=%v, now=%v); re-run the gate",
			lock.WeaklyTyped, cfg.DecoderConfig.WeaklyTypedInput)
	}

	report, err := DecodeWithBindings(cfg, input, result)
	if err != nil && report == nil {
		return fmt.Errorf("binding lock verification failed to run: %w", err)
	}

	fresh, ferr := report.Lock(cfg.DecoderConfig.WeaklyTypedInput)
	if ferr != nil {
		return ferr
	}

	if fresh.Count != lock.Count {
		return fmt.Errorf(
			"binding lock drifted: finding count locked=%d now=%d",
			lock.Count, fresh.Count)
	}

	if fresh.Fingerprint != lock.Fingerprint {
		return errors.New("binding lock drifted: ordered findings differ from the locked conclusion")
	}

	return nil
}

// TwoPassBinding decodes first into a generic table and then into the strongly
// typed result, reporting any field lost on the second pass or whose
// weak-conversion boundary drifts. An empty input path is an error, never an
// empty success.
func TwoPassBinding(cfg *BindingConfig, input map[string]any, result any) (*BindingReport, error) {
	if cfg == nil {
		return nil, errors.New("binding config must not be nil")
	}

	if len(input) == 0 {
		return nil, errors.New("two-pass binding path is empty: nothing to decode")
	}

	// Pass 1: bind into a generic table.
	generic := map[string]any{}

	decOne, err := prepareBindingDecoder(cfg, &generic)
	if err != nil {
		return nil, err
	}

	reportOne, errOne := decOne.runBinding(input)
	if errOne != nil {
		return reportOne, errOne
	}

	// The first pass binds into a generic table; its survived keys are the map
	// keys. They must not be empty, otherwise the path carried no data.
	firstKeys := sortedStringKeySetOf(generic)
	if len(firstKeys) == 0 {
		return nil, errors.New("two-pass binding: first pass recorded no fields; aborting before second pass")
	}

	// Pass 2: decode the generic table into the strong type with the same
	// configuration so weak-conversion boundaries cannot move between passes.
	decTwo, err := prepareBindingDecoder(cfg, result)
	if err != nil {
		return nil, err
	}

	reportTwo, errTwo := decTwo.runBinding(generic)

	merged := &BindingReport{}
	merged.errs = append(merged.errs, reportOne.errs...)
	merged.errs = append(merged.errs, reportTwo.errs...)

	// Field-loss / boundary-drift: every key that survived pass 1 must be
	// accounted for on pass 2.
	secondKeys := map[string]bool{}
	for _, k := range sortedKeySet(decTwo.binding.present) {
		secondKeys[k] = true
	}

	for _, key := range firstKeys {
		if !secondKeys[key] {
			merged.add(BindingTypeMismatch, key,
				"field survived the generic pass but was lost when binding into the strong type",
				decTwo, nil, nil)
		}
	}

	merged.sortLocked()

	if errTwo != nil {
		return merged, merged.Err()
	}

	return merged, merged.Err()
}
