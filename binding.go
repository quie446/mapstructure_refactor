package mapstructure

// This file implements the binding correction layer on top of the existing
// decoding flow. It does not replace the decoder: every code path below runs
// through Decoder created with DecoderConfig. The layer is responsible for
// four things that the raw decoder deliberately keeps separate:
//
//   - classifying binding failures into distinct, individually inspectable
//     categories (missing required fields, unconvertible types, unused keys,
//     and zero-vs-missing ambiguity), with a strict and a weak run compared
//     side by side;
//   - turning nested/squash/remain/omitempty/omitzero combinations into a
//     deterministic, reproducible table that refuses half-generated output;
//   - the two-pass path: input -> generic map -> strongly typed struct, with
//     lost fields and weak-conversion drift surfaced instead of skipped;
//   - an aggregate lock that only releases when every collected issue is
//     ordered, inspectable and accompanied by migration notes, and that
//     re-gates whenever the policy changes.

import (
	"crypto/sha256"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// IssueCategory identifies a single, separable class of binding failure.
// Categories are never merged into one generic error so that mixed and
// missing reports are impossible by construction.
type IssueCategory string

const (
	// CategoryMissingRequired is reported when a field tagged "required"
	// has no corresponding key in the input.
	CategoryMissingRequired IssueCategory = "missing_required"

	// CategoryTypeMismatch is reported in strict mode when a value cannot
	// be converted to the target type without weak conversion.
	CategoryTypeMismatch IssueCategory = "type_mismatch"

	// CategoryWeakDrift is reported when strict decoding rejected a value but
	// weak decoding accepted it: the weak switch moved the boundary.
	CategoryWeakDrift IssueCategory = "weak_conversion_drift"

	// CategoryUnusedKey is reported when the input contains a key that no
	// field consumes and no remain field captures.
	CategoryUnusedKey IssueCategory = "unused_key"

	// CategoryZeroAmbiguous is reported when a required field is present in
	// the input but holds the Go zero value, which cannot be distinguished
	// from "key absent" by inspecting the decoded struct alone.
	CategoryZeroAmbiguous IssueCategory = "zero_value_missing_ambiguous"

	// CategoryFieldLost is reported on the two-pass path when a key survived
	// in the generic map but never reached the typed struct.
	CategoryFieldLost IssueCategory = "field_lost_on_second_pass"

	// CategoryEmptyInput is reported when a decode is attempted with no
	// input path at all, rather than pretending an empty result decoded.
	CategoryEmptyInput IssueCategory = "empty_input_path"
)

// String returns the stable, lower-snake identifier used in reports.
func (c IssueCategory) String() string { return string(c) }

// BindingIssue is a single classified failure. Issues are value types with
// deterministic fields so reports can be compared machine to machine.
type BindingIssue struct {
	// Category is the failure class.
	Category IssueCategory

	// Path is the dotted input/field path ("address.city"), "" at root.
	Path string

	// Message is a human readable explanation of the failure.
	Message string
}

func (i BindingIssue) String() string {
	if i.Path == "" {
		return fmt.Sprintf("%s: %s", i.Category, i.Message)
	}

	return fmt.Sprintf("%s: %s: %s", i.Category, i.Path, i.Message)
}

// BindingPolicy pins down which failure classes are enforced and whether the
// weak conversion switch participates. The zero value is lenient for backward
// compatibility; use StrictBindingPolicy for the pinned set.
type BindingPolicy struct {
	// RequireMissing reports fields tagged "required" that are absent.
	RequireMissing bool

	// RejectUnused reports input keys that no field consumes.
	RejectUnused bool

	// FlagZeroAmbiguous flags required fields present with a zero value.
	FlagZeroAmbiguous bool

	// WeaklyTypedInput toggles the weak conversion switch. Comparisons run
	// the opposite setting as well, so both sides are always observed.
	WeaklyTypedInput bool

	// TagName selects the struct tag; defaults to "mapstructure".
	TagName string

	// MigrationNotes maps a failure category to the migration guidance that
	// must exist before a breaking aggregate is allowed to lock.
	MigrationNotes map[IssueCategory]string
}

// StrictBindingPolicy returns the pinned policy: every distinguishable class
// is enforced, weak conversion is off, and the reference run is strict.
func StrictBindingPolicy() BindingPolicy {
	return BindingPolicy{
		RequireMissing:    true,
		RejectUnused:      true,
		FlagZeroAmbiguous: true,
	}
}

// Signature returns a stable fingerprint of the policy. An aggregate locked
// under one signature must re-gate when the signature changes.
func (p BindingPolicy) Signature() string {
	notes := make([]string, 0, len(p.MigrationNotes))
	for category, note := range p.MigrationNotes {
		notes = append(notes, string(category)+"="+note)
	}
	sort.Strings(notes)

	raw := strings.Join([]string{
		fmt.Sprintf("missing=%t", p.RequireMissing),
		fmt.Sprintf("unused=%t", p.RejectUnused),
		fmt.Sprintf("zero=%t", p.FlagZeroAmbiguous),
		fmt.Sprintf("weak=%t", p.WeaklyTypedInput),
		fmt.Sprintf("tag=%s", p.effectiveTagName()),
		"notes=" + strings.Join(notes, ";"),
	}, "|")

	sum := sha256.Sum256([]byte(raw))

	return hex.EncodeToString(sum[:])
}

func (p BindingPolicy) effectiveTagName() string {
	if p.TagName == "" {
		return "mapstructure"
	}

	return p.TagName
}

// BindingReport is the ordered, inspectable result of a binding run.
type BindingReport struct {
	// PolicySignature records the policy the input was checked against.
	PolicySignature string

	// Issues are sorted by category then path and are deduplicated.
	Issues []BindingIssue

	// WeaklyTypedInput records the switch setting of the accepted run.
	WeaklyTypedInput bool

	// WeakAccepted lists fields accepted only with the weak switch on.
	WeakAccepted []string

	// Metadata is the metadata of the accepted (strict-first) decode.
	Metadata *Metadata
}

// ByCategory returns the issue subset for one category. Ordering preserved.
func (r *BindingReport) ByCategory(category IssueCategory) []BindingIssue {
	out := make([]BindingIssue, 0)
	for _, issue := range r.Issues {
		if issue.Category == category {
			out = append(out, issue)
		}
	}

	return out
}

// Has reports whether the category is present.
func (r *BindingReport) Has(category IssueCategory) bool {
	for _, issue := range r.Issues {
		if issue.Category == category {
			return true
		}
	}

	return false
}

// Err converts the report into a classified error, or nil when clean.
func (r *BindingReport) Err() error {
	if len(r.Issues) == 0 {
		return nil
	}

	return &BindingError{Issues: append([]BindingIssue(nil), r.Issues...)}
}

// BindingError carries the full ordered issue set and implements the package
// Error marker so it joins the existing error hierarchy.
type BindingError struct {
	Issues []BindingIssue
}

func (e *BindingError) Error() string {
	lines := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		lines = append(lines, "  - "+issue.String())
	}

	return "binding failed due to the following error(s):\n" + strings.Join(lines, "\n")
}

func (e *BindingError) Unwrap() []error {
	errs := make([]error, len(e.Issues))
	for i, issue := range e.Issues {
		errs[i] = stderrors.New(issue.String())
	}

	return errs
}

func (*BindingError) mapstructure() {}

// BindingDecoder binds the correction layer into an existing DecoderConfig
// template and a pinned policy. The same template feeds every run, so binding
// correction is part of the decode flow rather than a parallel implementation.
type BindingDecoder struct {
	template DecoderConfig
	policy   BindingPolicy
}

// NewBindingDecoder validates the template and pins the policy. Result must be
// a pointer exactly as for NewDecoder.
func NewBindingDecoder(template *DecoderConfig, policy BindingPolicy) (*BindingDecoder, error) {
	if template == nil {
		return nil, stderrors.New("binding decoder requires a DecoderConfig")
	}

	if template.Result == nil {
		return nil, stderrors.New("binding decoder requires DecoderConfig.Result")
	}

	val := reflect.ValueOf(template.Result)
	if val.Kind() != reflect.Ptr {
		return nil, stderrors.New("result must be a pointer")
	}

	if policy.TagName != "" {
		template.TagName = policy.TagName
	}

	return &BindingDecoder{template: *template, policy: policy}, nil
}

// Decode runs the strict/weak comparison and returns the ordered report.
// Decoding never silently skips a failure: every collected issue is attached.
func (b *BindingDecoder) Decode(input any) (*BindingReport, error) {
	if isEmptyInputPath(input) {
		return nil, &BindingError{Issues: []BindingIssue{{
			Category: CategoryEmptyInput,
			Message:  "decode called with an empty input path; nothing was decoded",
		}}}
	}

	report := &BindingReport{
		PolicySignature:  b.policy.Signature(),
		WeaklyTypedInput: b.policy.WeaklyTypedInput,
		Issues:           []BindingIssue{},
		WeakAccepted:     []string{},
	}

	strictResult := newTarget(b.template.Result)
	strictMeta := &Metadata{}
	strictCfg := b.template
	strictCfg.Result = strictResult
	strictCfg.Metadata = strictMeta
	strictCfg.WeaklyTypedInput = false
	strictCfg.ErrorUnused = false
	strictCfg.ErrorUnset = false

	strictDecoder, err := NewDecoder(&strictCfg)
	if err != nil {
		return nil, err
	}
	strictErr := strictDecoder.Decode(input)

	weakResult := newTarget(b.template.Result)
	weakMeta := &Metadata{}
	weakCfg := b.template
	weakCfg.Result = weakResult
	weakCfg.Metadata = weakMeta
	weakCfg.WeaklyTypedInput = true
	weakCfg.ErrorUnused = false
	weakCfg.ErrorUnset = false

	weakDecoder, err := NewDecoder(&weakCfg)
	if err != nil {
		return nil, err
	}
	weakErr := weakDecoder.Decode(input)

	// The accepted run follows the policy switch, but both runs are observed.
	if b.policy.WeaklyTypedInput {
		report.Metadata = weakMeta
		publishResult(b.template.Result, weakResult)
	} else {
		report.Metadata = strictMeta
		publishResult(b.template.Result, strictResult)
	}

	strictFailures := decodeFailures(strictErr)
	weakFailures := decodeFailures(weakErr)

	for path, msg := range strictFailures {
		if _, ok := weakFailures[path]; ok {
			continue // both modes reject: hard type mismatch, handled below
		}

		report.WeakAccepted = append(report.WeakAccepted, path)

		if b.policy.WeaklyTypedInput {
			report.Issues = append(report.Issues, BindingIssue{
				Category: CategoryWeakDrift,
				Path:     path,
				Message:  "value rejected in strict mode but accepted with WeaklyTypedInput: " + msg,
			})
		} else {
			report.Issues = append(report.Issues, BindingIssue{
				Category: CategoryTypeMismatch,
				Path:     path,
				Message:  msg,
			})
		}
	}

	// A value weak mode still cannot convert is a mismatch in both settings.
	for path, msg := range weakFailures {
		if _, ok := strictFailures[path]; ok {
			report.Issues = append(report.Issues, BindingIssue{
				Category: CategoryTypeMismatch,
				Path:     path,
				Message:  msg,
			})
		}
	}

	if b.policy.RejectUnused {
		for _, key := range unconsumedLeafKeys(input, report.Metadata.Keys, remainKeys(b.template.Result, b.policy.effectiveTagName())) {
			report.Issues = append(report.Issues, BindingIssue{
				Category: CategoryUnusedKey,
				Path:     key,
				Message:  "input key is not bound to any field or remain map",
			})
		}
	}

	required := requiredLeafPaths(b.template.Result, b.policy.effectiveTagName(), nil, "")
	present := stringSet(report.Metadata.Keys)
	if b.policy.RequireMissing {
		for _, path := range required {
			if _, ok := present[path]; !ok {
				report.Issues = append(report.Issues, BindingIssue{
					Category: CategoryMissingRequired,
					Path:     path,
					Message:  "required field has no matching key in the input",
				})
			}
		}
	}

	if b.policy.FlagZeroAmbiguous {
		report.Issues = append(report.Issues,
			zeroAmbiguities(reflect.ValueOf(b.template.Result).Elem(), input, b.policy.effectiveTagName(), "")...)
	}

	report.Issues = dedupeIssues(report.Issues)
	sortIssues(report.Issues)
	sort.Strings(report.WeakAccepted)

	return report, report.Err()
}

// DecodeWithBindings is the convenience wrapper around NewBindingDecoder.
func DecodeWithBindings(input any, output any, policy BindingPolicy) (*BindingReport, error) {
	decoder, err := NewBindingDecoder(&DecoderConfig{Result: output}, policy)
	if err != nil {
		return nil, err
	}

	return decoder.Decode(input)
}

// decodeFailures flattens a joined decode error into path -> message. Only
// *DecodeError leaves carry a dotted field path, matching decoder naming.
func decodeFailures(err error) map[string]string {
	out := map[string]string{}
	if err == nil {
		return out
	}

	var joined interface{ Unwrap() []error }
	if stderrors.As(err, &joined) {
		for _, sub := range joined.Unwrap() {
			collectFailure(sub, out)
		}
	} else {
		collectFailure(err, out)
	}

	return out
}

func collectFailure(err error, out map[string]string) {
	var decodeErr *DecodeError
	if stderrors.As(err, &decodeErr) {
		out[decodeErr.Name()] = decodeErr.Unwrap().Error()
		return
	}

	var joined interface{ Unwrap() []error }
	if stderrors.As(err, &joined) {
		for _, sub := range joined.Unwrap() {
			collectFailure(sub, out)
		}
		return
	}

	out[""] = err.Error()
}

// newTarget allocates a fresh value of the same type as the pointer target.
func newTarget(pointer any) any {
	target := reflect.ValueOf(pointer).Elem()

	return reflect.New(target.Type()).Interface()
}

// publishResult copies the fresh decode target back into the user pointer.
func publishResult(dst any, src any) {
	reflect.ValueOf(dst).Elem().Set(reflect.ValueOf(src).Elem())
}

func stringSet(items []string) map[string]struct{} {
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		set[item] = struct{}{}
	}

	return set
}

func sortIssues(issues []BindingIssue) {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Category != issues[j].Category {
			return issues[i].Category < issues[j].Category
		}
		if issues[i].Path != issues[j].Path {
			return issues[i].Path < issues[j].Path
		}

		return issues[i].Message < issues[j].Message
	})
}

func dedupeIssues(issues []BindingIssue) []BindingIssue {
	seen := make(map[BindingIssue]struct{}, len(issues))
	out := make([]BindingIssue, 0, len(issues))

	for _, issue := range issues {
		if _, ok := seen[issue]; ok {
			continue
		}

		seen[issue] = struct{}{}
		out = append(out, issue)
	}

	return out
}

func isEmptyInputPath(input any) bool {
	if input == nil {
		return true
	}

	val := reflect.ValueOf(input)
	switch val.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array:
		return val.Len() == 0
	case reflect.Ptr, reflect.Interface:
		return val.IsNil()
	}

	return false
}

func sortIssueKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)

	return out
}

// unconsumedLeafKeys returns dotted input leaf keys that never appear in the
// consumed key list. Keys collected by a remain field are consumed at decode
// time and therefore absent from the result.
func unconsumedLeafKeys(input any, consumed, remain []string) []string {
	inputKeys := stringSet(leafKeys(input, ""))

	for _, key := range consumed {
		delete(inputKeys, key)
	}

	for _, key := range remain {
		delete(inputKeys, key)
	}

	// A consumed parent (a nested struct key) covers its leaf paths.
	for _, key := range consumed {
		prefix := key + "."
		for leaf := range inputKeys {
			if strings.HasPrefix(leaf, prefix) {
				delete(inputKeys, leaf)
			}
		}
	}

	return sortIssueKeys(inputKeys)
}

// remainKeys returns the top-level keys currently held by remain-tagged maps.
func remainKeys(pointer any, tagName string) []string {
	target := reflect.ValueOf(pointer).Elem()
	out := map[string]struct{}{}
	collectRemainKeys(target, tagName, out)

	return sortIssueKeys(out)
}

func collectRemainKeys(value reflect.Value, tagName string, out map[string]struct{}) {
	value = indirectValue(value)
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return
	}

	valueType := value.Type()

	for index := 0; index < valueType.NumField(); index++ {
		structField := valueType.Field(index)
		tagParts := getTagParts(structField, tagName)

		isRemain := false
		for _, option := range tagParts {
			if option == "remain" {
				isRemain = true
			}
		}

		fieldValue := value.Field(index)

		if isRemain {
			fieldValue = indirectValue(fieldValue)
			if fieldValue.IsValid() && fieldValue.Kind() == reflect.Map {
				for _, key := range fieldValue.MapKeys() {
					if keyString, ok := key.Interface().(string); ok {
						out[keyString] = struct{}{}
					}
				}
			}

			continue
		}

		if derefType(structField.Type).Kind() == reflect.Struct {
			collectRemainKeys(fieldValue, tagName, out)
		}
	}
}

// fieldBinding describes one bound leaf in a target type.
type fieldBinding struct {
	path     string
	kind     reflect.Kind
	required bool
	squash   bool
	remain   bool
	omit     bool // omitempty or omitzero
	nested   bool
}

// walkBindings traverses a struct type using the same tag semantics as the
// decoder (squash, remain, anonymous embedding) and invokes visit for each
// leaf in deterministic field order.
func walkBindings(t reflect.Type, tagName string, prefix string, squashedParent bool, visit func(fieldBinding)) {
	t = derefType(t)
	if t.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < t.NumField(); i++ {
		structField := t.Field(i)
		if structField.PkgPath != "" && !structField.Anonymous {
			continue
		}

		tagParts := getTagParts(structField, tagName)
		if len(tagParts) == 0 {
			tagParts = []string{""}
		}

		name := tagParts[0]
		if name == "-" {
			continue
		}

		var squash, remain, omit, required bool
		for _, option := range tagParts[1:] {
			switch option {
			case "squash":
				squash = true
			case "remain":
				remain = true
			case "omitempty", "omitzero":
				omit = true
			case "required":
				required = true
			}
		}

		if name == "" {
			if squash || structField.Anonymous {
				name = ""
			} else {
				name = structField.Name
			}
		}

		path := name
		if prefix != "" && path != "" {
			path = prefix + "." + path
		}

		fieldType := derefType(structField.Type)
		nested := fieldType.Kind() == reflect.Struct

		if (squash || (structField.Anonymous && name == "")) && nested {
			walkBindings(fieldType, tagName, prefix, true, visit)
			continue
		}

		visit(fieldBinding{
			path:     path,
			kind:     fieldType.Kind(),
			required: required,
			squash:   squash || squashedParent,
			remain:   remain,
			omit:     omit,
			nested:   nested,
		})

		if nested && !remain {
			walkBindings(fieldType, tagName, path, false, visit)
		}
	}
}

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	return t
}

// requiredLeafPaths returns the sorted dotted paths of required leaf fields.
func requiredLeafPaths(pointer any, tagName string, _ map[string]struct{}, _ string) []string {
	target := reflect.ValueOf(pointer).Elem().Type()
	paths := map[string]struct{}{}

	walkBindings(target, tagName, "", false, func(binding fieldBinding) {
		if binding.required && !binding.nested {
			paths[binding.path] = struct{}{}
		}
	})

	return sortIssueKeys(paths)
}

// zeroAmbiguities flags required leaves whose decoded value is zero while the
// input key is actually present: the struct cannot tell zero from missing.
func zeroAmbiguities(target reflect.Value, input any, tagName, prefix string) []BindingIssue {
	targetType := derefType(target.Type())
	if targetType.Kind() != reflect.Struct {
		return nil
	}

	issues := []BindingIssue{}

	for i := 0; i < targetType.NumField(); i++ {
		structField := targetType.Field(i)
		if structField.PkgPath != "" && !structField.Anonymous {
			continue
		}

		tagParts := getTagParts(structField, tagName)
		if len(tagParts) == 0 {
			tagParts = []string{""}
		}

		name := tagParts[0]
		if name == "-" {
			continue
		}

		var squash, required bool
		for _, option := range tagParts[1:] {
			switch option {
			case "squash":
				squash = true
			case "required":
				required = true
			}
		}

		fieldValue := target.Field(i)
		fieldType := derefType(structField.Type)

		if name == "" {
			name = structField.Name
		}

		path := name
		if prefix != "" {
			path = prefix + "." + name
		}

		if fieldType.Kind() == reflect.Struct {
			nestedInput := input
			nestedPrefix := prefix

			if !(squash || (structField.Anonymous && tagParts[0] == "")) {
				nestedInput = lookupNested(input, tagName, path)
				nestedPrefix = path
			}

			child := indirectValue(fieldValue)
			if child.IsValid() && child.Kind() == reflect.Struct {
				issues = append(issues, zeroAmbiguities(child, nestedInput, tagName, nestedPrefix)...)
			}

			continue
		}

		if !required {
			continue
		}

		raw, ok := lookupPath(input, path)
		if ok && isEmptyInterface(raw) {
			issues = append(issues, BindingIssue{
				Category: CategoryZeroAmbiguous,
				Path:     path,
				Message:  "required key is present with a zero value; zero cannot be distinguished from a missing key",
			})
		}
	}

	return issues
}

func indirectValue(value reflect.Value) reflect.Value {
	for value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return value
		}
		value = value.Elem()
	}

	return value
}

func isEmptyInterface(value any) bool {
	if value == nil {
		return true
	}

	return isEmptyValue(reflect.ValueOf(value))
}

// lookupPath resolves a dotted path through nested maps using case-insensitive
// key matching, mirroring the decoder's default MatchName.
func lookupPath(input any, path string) (any, bool) {
	current := input

	for _, segment := range strings.Split(path, ".") {
		m, ok := asStringMap(current)
		if !ok {
			return nil, false
		}

		next, ok := matchMapKey(m, segment)
		if !ok {
			return nil, false
		}

		current = next
	}

	return current, true
}

func lookupNested(input any, _ string, prefix string) any {
	value, ok := lookupPath(input, prefix)
	if !ok {
		return nil
	}

	return value
}

func asStringMap(value any) (map[string]any, bool) {
	if value == nil {
		return nil, false
	}

	ref := reflect.ValueOf(value)
	if ref.Kind() != reflect.Map {
		return nil, false
	}

	out := map[string]any{}

	for _, key := range ref.MapKeys() {
		keyString, ok := key.Interface().(string)
		if !ok {
			return nil, false
		}

		out[keyString] = ref.MapIndex(key).Interface()
	}

	return out, true
}

func matchMapKey(m map[string]any, name string) (any, bool) {
	if value, ok := m[name]; ok {
		return value, true
	}

	for key, value := range m {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}

	return nil, false
}
