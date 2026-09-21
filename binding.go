package mapstructure

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/go-viper/mapstructure/v2/internal/errors"
)

// This file adds the structured binding-correction layer on top of the
// existing decoder. It does NOT introduce a second decoder: every check is
// driven through Decoder.decode / decodeStructFromMap and the existing
// DecoderConfig, so the new structured path and the historical path cannot
// drift apart.
//
// The layer provides four cooperating pieces:
//
//   - distinct, separately inspectable error classes (missing required field,
//     value that cannot be converted, unused key, and a zero-vs-absent
//     ambiguity), with WeaklyTypedInput on/off recorded on each finding;
//   - a table-driven matrix over nested/squash/remain/omitempty combinations;
//   - a two-pass binding (generic map -> strongly typed struct -> generic map)
//     that fails loudly when a field is lost or drifts across the weak
//     conversion boundary;
//   - a deterministic, ordered, item-by-item aggregate report that can be
//     locked and re-verified on another machine.
//
// Every failure carries an explicit reason; an unfinished or empty run is an
// error, never a success.

// BindingKind identifies one distinct class of binding error. Classes are kept
// separate; merging or dropping a class is an incorrect report.
type BindingKind string

const (
	// BindingMissingRequired: a required field has no corresponding input key.
	BindingMissingRequired BindingKind = "missing_required"

	// BindingTypeMismatch: a value cannot be converted to the field type. The
	// finding records whether WeaklyTypedInput masked the boundary.
	BindingTypeMismatch BindingKind = "type_mismatch"

	// BindingUnusedKey: an input key was not consumed by any field.
	BindingUnusedKey BindingKind = "unused_key"

	// BindingZeroAmbiguous: a present key decoded to the zero value, so
	// "explicit zero" cannot be distinguished from "defaulted because absent".
	BindingZeroAmbiguous BindingKind = "zero_ambiguous"
)

// BindingError is one structured finding. It implements the package Error
// interface so existing errors.As / errors.Is code keeps working while the
// class, path and reason stay programmatically inspectable.
type BindingError struct {
	Kind        BindingKind
	Path        string
	Reason      string
	WeaklyTyped bool
	Wanted      reflect.Type
	Got         any
}

func (e *BindingError) Error() string {
	switch e.Kind {
	case BindingMissingRequired:
		return fmt.Sprintf("'%s' missing required field: %s", e.Path, e.Reason)
	case BindingTypeMismatch:
		return fmt.Sprintf("'%s' cannot convert value: %s", e.Path, e.Reason)
	case BindingUnusedKey:
		return fmt.Sprintf("'%s' unused key: %s", e.Path, e.Reason)
	case BindingZeroAmbiguous:
		return fmt.Sprintf("'%s' zero/default ambiguous: %s", e.Path, e.Reason)
	default:
		return fmt.Sprintf("'%s' %s: %s", e.Path, e.Kind, e.Reason)
	}
}

func (*BindingError) mapstructure() {}

// BindingReport is the deterministic, item-by-item aggregate of a single
// binding run. Findings are fixed-order (Path then Kind), so the same input
// yields an identical conclusion on every machine. A report is only returned
// after the walk finished; a half walk is returned as an error.
type BindingReport struct {
	errs []*BindingError
}

// Errors returns a copy of the findings in deterministic order.
func (r *BindingReport) Errors() []*BindingError {
	if r == nil {
		return nil
	}

	out := make([]*BindingError, len(r.errs))
	copy(out, r.errs)

	return out
}

// ByKind returns the findings of one class in deterministic order.
func (r *BindingReport) ByKind(kind BindingKind) []*BindingError {
	if r == nil {
		return nil
	}

	var out []*BindingError

	for _, e := range r.errs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}

	return out
}

// Has reports whether a finding of the given class exists.
func (r *BindingReport) Has(kind BindingKind) bool {
	return len(r.ByKind(kind)) > 0
}

// Len is the number of findings.
func (r *BindingReport) Len() int {
	if r == nil {
		return 0
	}

	return len(r.errs)
}

// Err joins every finding into one ordered error, or returns nil. The joined
// error keeps each finding inspectable via errors.As / errors.Is.
func (r *BindingReport) Err() error {
	if r == nil || len(r.errs) == 0 {
		return nil
	}

	joined := make([]error, len(r.errs))

	for i, e := range r.errs {
		joined[i] = e
	}

	return errors.Join(joined...)
}

func (r *BindingReport) add(kind BindingKind, path, reason string, d *Decoder, wanted reflect.Type, got any) {
	if reason == "" {
		reason = "binding check failed without an explicit reason"
	}

	r.errs = append(r.errs, &BindingError{
		Kind:        kind,
		Path:        path,
		Reason:      reason,
		WeaklyTyped: d.config.WeaklyTypedInput,
		Wanted:      wanted,
		Got:         got,
	})
}

func (r *BindingReport) sortLocked() {
	sort.SliceStable(r.errs, func(i, j int) bool {
		if r.errs[i].Path != r.errs[j].Path {
			return r.errs[i].Path < r.errs[j].Path
		}

		return r.errs[i].Kind < r.errs[j].Kind
	})
}

// joinPath appends child to a dotted path.
func joinPath(parent, child string) string {
	if parent == "" {
		return child
	}

	return parent + "." + child
}

// dedupSortedStrings returns the sorted, de-duplicated copy of in.
func dedupSortedStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}

	out := append([]string(nil), in...)
	sort.Strings(out)

	w := 0

	for _, s := range out {
		if w > 0 && out[w-1] == s {
			continue
		}

		out[w] = s
		w++
	}

	return out[:w]
}

func sameOrderedStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func sameStringSet(a, b []string) bool {
	return sameOrderedStrings(dedupSortedStrings(a), dedupSortedStrings(b))
}

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))

	for _, s := range in {
		out = append(out, strings.TrimSpace(s))
	}

	return out
}
