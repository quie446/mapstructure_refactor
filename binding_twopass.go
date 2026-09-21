package mapstructure

import (
	"crypto/sha256"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

func stableFingerprint(raw string) string {
	sum := sha256.Sum256([]byte(raw))

	return hex.EncodeToString(sum[:])
}

// TwoPassReport is the audited result of the generic-map -> typed-struct path.
type TwoPassReport struct {
	// InputFingerprint identifies the exact input that was processed.
	InputFingerprint string

	// PolicySignature records the binding policy in force.
	PolicySignature string

	// Generic is the populated intermediate map[string]any from pass one.
	Generic map[string]any

	// GenericKeys are the dotted leaf keys present after pass one.
	GenericKeys []string

	// TypedKeys are the dotted keys consumed in pass two.
	TypedKeys []string

	// Issues are ordered by category then path.
	Issues []BindingIssue
}

// Err returns nil when the two-pass run is clean.
func (r *TwoPassReport) Err() error {
	if len(r.Issues) == 0 {
		return nil
	}

	return &BindingError{Issues: append([]BindingIssue(nil), r.Issues...)}
}

// DecodeTwoPass forces the canonical path: the input is first decoded into a
// generic map[string]any and that exact intermediate is decoded again into the
// strongly typed target. Neither pass may skip a field or let a weak boundary
// drift silently; failures abort the path with classified issues.
func DecodeTwoPass(input any, output any, policy BindingPolicy) (*TwoPassReport, error) {
	if isEmptyInputPath(input) {
		return nil, &BindingError{Issues: []BindingIssue{{
			Category: CategoryEmptyInput,
			Path:     "",
			Message:  "two-pass decode requires a non-empty input path; nothing ran",
		}}}
	}

	if policy.TagName == "" {
		policy.TagName = "mapstructure"
	}

	fingerprint, err := fingerprintValue(input)
	if err != nil {
		return nil, fmt.Errorf("two-pass decode: %w", err)
	}

	report := &TwoPassReport{
		InputFingerprint: fingerprint,
		PolicySignature:  policy.Signature(),
		Issues:           []BindingIssue{},
	}

	// Pass one: input -> generic map, running through the existing decoder.
	var generic map[string]any

	passOneCfg := &DecoderConfig{
		Result:           &generic,
		WeaklyTypedInput: policy.WeaklyTypedInput,
		TagName:          policy.TagName,
	}

	passOne, err := NewDecoder(passOneCfg)
	if err != nil {
		return nil, err
	}

	if err := passOne.Decode(input); err != nil {
		for path, msg := range decodeFailures(err) {
			report.Issues = append(report.Issues, BindingIssue{
				Category: CategoryTypeMismatch,
				Path:     path,
				Message:  "first pass into the generic map failed: " + msg,
			})
		}

		sortIssues(report.Issues)

		return report, report.Err()
	}

	if len(generic) == 0 {
		return nil, &BindingError{Issues: []BindingIssue{{
			Category: CategoryEmptyInput,
			Message:  "first pass produced an empty map; refusing to claim the path ran",
		}}}
	}

	report.Generic = generic

	// Pass two: generic map -> typed struct, with the strict/weak comparison
	// embedded through the binding decoder rather than a parallel routine.
	bindingDecoder, err := NewBindingDecoder(&DecoderConfig{
		Result:  output,
		TagName: policy.TagName,
	}, policy)
	if err != nil {
		return nil, err
	}

	bindingReport, err := bindingDecoder.Decode(generic)
	if err != nil {
		var bindingErr *BindingError

		if stderrors.As(err, &bindingErr) {
			report.Issues = append(report.Issues, bindingErr.Issues...)
		} else {
			return nil, err
		}
	} else {
		report.Issues = append(report.Issues, bindingReport.Issues...)
	}

	genericKeys := leafKeys(generic, "")
	typedSet := stringSet(bindingReport.Metadata.Keys)
	unconsumed := unconsumedLeafKeys(generic, bindingReport.Metadata.Keys,
		remainKeys(output, policy.effectiveTagName()))

	// When the policy rejects unused keys the binding run already emitted
	// CategoryUnusedKey; otherwise an unmatched key vanished silently on the
	// second pass and must be reported as lost.
	if !policy.RejectUnused {
		for _, path := range unconsumed {
			report.Issues = append(report.Issues, BindingIssue{
				Category: CategoryFieldLost,
				Path:     path,
				Message:  "key survived in the generic map but never reached the typed struct",
			})
		}
	}

	report.GenericKeys = genericKeys
	report.TypedKeys = sortedKeys(typedSet)

	report.Issues = dedupeIssues(report.Issues)
	sortIssues(report.Issues)

	return report, report.Err()
}

func leafKeys(value any, prefix string) []string {
	m, ok := asStringMap(value)
	if !ok {
		return nil
	}

	keys := make([]string, 0)

	for key, child := range m {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}

		if nested, ok := asStringMap(child); ok && len(nested) > 0 {
			keys = append(keys, leafKeys(child, path)...)
			continue
		}

		keys = append(keys, path)
	}

	sort.Strings(keys)

	return keys
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}

	sort.Strings(out)

	return out
}

func fingerprintValue(value any) (string, error) {
	raw, err := stableValue(value)
	if err != nil {
		return "", err
	}

	return stableFingerprint(raw), nil
}

// stableValue renders a value into a deterministic textual form without
// relying on map iteration order.
func stableValue(value any) (string, error) {
	if value == nil {
		return "<nil>", nil
	}

	var builder strings.Builder

	if err := writeStableValue(&builder, reflect.ValueOf(value)); err != nil {
		return "", err
	}

	return builder.String(), nil
}

func writeStableValue(builder *strings.Builder, value reflect.Value) error {
	switch value.Kind() {
	case reflect.Interface, reflect.Ptr:
		if value.IsNil() {
			builder.WriteString("<nil>")
			return nil
		}

		return writeStableValue(builder, value.Elem())

	case reflect.Map:
		keys := value.MapKeys()
		keyStrings := make([]string, 0, len(keys))
		keyByString := map[string]reflect.Value{}

		for _, key := range keys {
			raw := fmt.Sprintf("%v", key.Interface())
			keyStrings = append(keyStrings, raw)
			keyByString[raw] = key
		}

		sort.Strings(keyStrings)
		builder.WriteString("map{")

		for index, keyString := range keyStrings {
			if index > 0 {
				builder.WriteString(",")
			}

			builder.WriteString(keyString)
			builder.WriteString(":")

			if err := writeStableValue(builder, value.MapIndex(keyByString[keyString])); err != nil {
				return err
			}
		}

		builder.WriteString("}")

	case reflect.Slice, reflect.Array:
		builder.WriteString("seq[")

		for index := 0; index < value.Len(); index++ {
			if index > 0 {
				builder.WriteString(",")
			}

			if err := writeStableValue(builder, value.Index(index)); err != nil {
				return err
			}
		}

		builder.WriteString("]")

	default:
		builder.WriteString(fmt.Sprintf("%T(%v)", value.Interface(), value.Interface()))
	}

	return nil
}
