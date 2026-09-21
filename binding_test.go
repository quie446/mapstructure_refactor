package mapstructure

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type bindingAddress struct {
	City string `mapstructure:"city,required"`
	Zip  int    `mapstructure:"zip"`
}

type bindingPerson struct {
	Name  string          `mapstructure:"name,required"`
	Age   int             `mapstructure:"age,required"`
	Addr  *bindingAddress `mapstructure:"addr"`
	Tags  []string        `mapstructure:"tags,omitempty"`
	Other map[string]any  `mapstructure:",remain"`
}

type bindingStrictPerson struct {
	Name string          `mapstructure:"name,required"`
	Age  int             `mapstructure:"age,required"`
	Addr *bindingAddress `mapstructure:"addr"`
}

type bindingSquashEmbed struct {
	TraceID string `mapstructure:"trace_id"`
}

type bindingSquash struct {
	bindingSquashEmbed `mapstructure:",squash"`
	Name               string         `mapstructure:"name,required"`
	Inner              bindingAddress `mapstructure:"inner"`
	Count              int            `mapstructure:"count,omitzero"`
	Rest               map[string]any `mapstructure:",remain"`
}

func assertIssue(t *testing.T, report *BindingReport, category IssueCategory, path string) {
	t.Helper()

	for _, issue := range report.ByCategory(category) {
		if issue.Path == path {
			if strings.TrimSpace(issue.Message) == "" {
				t.Fatalf("issue %s/%s reported without a reason", category, path)
			}

			return
		}
	}

	t.Fatalf("expected %s issue at %q, got %v", category, path, report.Issues)
}

func assertNoIssue(t *testing.T, report *BindingReport, category IssueCategory) {
	t.Helper()

	if report.Has(category) {
		t.Fatalf("did not expect %s issues, got %v", category, report.ByCategory(category))
	}
}

// A previously valid binding must keep passing once the correction layer is
// enabled: tightening must not reject stable inputs.
func TestBindingBackwardCompatible(t *testing.T) {
	input := map[string]any{
		"name":     "alice",
		"age":      31,
		"addr":     map[string]any{"city": "sh", "zip": 200000},
		"tags":     []string{"a"},
		"leftover": true,
	}

	var person bindingPerson

	report, err := DecodeWithBindings(input, &person, StrictBindingPolicy())
	if err != nil {
		t.Fatalf("stable binding was rejected: %v", err)
	}

	if person.Name != "alice" || person.Age != 31 || person.Addr.City != "sh" {
		t.Fatalf("decoded value drifted: %+v", person)
	}

	if person.Other["leftover"] != true {
		t.Fatalf("remain key was not captured: %+v", person.Other)
	}

	if len(report.Issues) != 0 {
		t.Fatalf("clean input produced issues: %v", report.Issues)
	}
}

// The four failure classes must be reported separately and never merged.
func TestBindingClassifiesFailuresSeparately(t *testing.T) {
	input := map[string]any{
		"age":  0,
		"addr": map[string]any{"city": 42},
		"junk": true,
	}

	var person bindingStrictPerson

	report, err := DecodeWithBindings(input, &person, StrictBindingPolicy())
	if err == nil {
		t.Fatal("expected a classified binding error")
	}

	var bindingErr *BindingError
	if !errors.As(err, &bindingErr) {
		t.Fatalf("expected *BindingError, got %T", err)
	}

	assertIssue(t, report, CategoryMissingRequired, "name")
	assertIssue(t, report, CategoryTypeMismatch, "addr.city")
	assertIssue(t, report, CategoryUnusedKey, "junk")
	assertIssue(t, report, CategoryZeroAmbiguous, "age")

	if mismatches := report.ByCategory(CategoryTypeMismatch); len(mismatches) != 1 || mismatches[0].Path != "addr.city" {
		t.Fatalf("type mismatch set drifted: %v", mismatches)
	}

	for index := 1; index < len(report.Issues); index++ {
		prev, cur := report.Issues[index-1], report.Issues[index]
		if prev.Category > cur.Category ||
			(prev.Category == cur.Category && prev.Path > cur.Path) {
			t.Fatalf("issues are not deterministically ordered: %v", report.Issues)
		}
	}
}

// Weak conversion off rejects; on it accepts but the boundary drift is
// reported, so both sides of the switch have a contrasting result.
func TestBindingWeakSwitchContrast(t *testing.T) {
	input := map[string]any{
		"name": "bob",
		"age":  "37",
		"addr": map[string]any{"city": "sh"},
	}

	var strictPerson bindingPerson

	strictReport, strictErr := DecodeWithBindings(input, &strictPerson, StrictBindingPolicy())
	if strictErr == nil {
		t.Fatal("strict policy must reject string -> int")
	}

	assertIssue(t, strictReport, CategoryTypeMismatch, "age")

	weakPolicy := StrictBindingPolicy()
	weakPolicy.WeaklyTypedInput = true

	var weakPerson bindingPerson

	weakReport, weakErr := DecodeWithBindings(input, &weakPerson, weakPolicy)
	if weakErr == nil {
		t.Fatal("weak policy must still surface the drift rather than hide it")
	}

	assertIssue(t, weakReport, CategoryWeakDrift, "age")
	assertNoIssue(t, weakReport, CategoryTypeMismatch)

	if weakPerson.Age != 37 {
		t.Fatalf("weak conversion did not populate value: %d", weakPerson.Age)
	}

	var hardPerson bindingPerson

	hardInput := map[string]any{
		"name": "bob",
		"age":  "not-a-number",
		"addr": map[string]any{"city": "sh"},
	}

	hardReport, hardErr := DecodeWithBindings(hardInput, &hardPerson, weakPolicy)
	if hardErr == nil {
		t.Fatal("unconvertible value must fail even with weak input enabled")
	}

	assertIssue(t, hardReport, CategoryTypeMismatch, "age")
}

// An empty path halts with an explicit reason instead of an empty success.
func TestBindingEmptyInputHalts(t *testing.T) {
	var person bindingStrictPerson

	_, err := DecodeWithBindings(nil, &person, StrictBindingPolicy())
	if err == nil {
		t.Fatal("nil input was treated as decoded")
	}

	var bindingErr *BindingError
	if !errors.As(err, &bindingErr) {
		t.Fatalf("expected *BindingError, got %T", err)
	}

	if !strings.Contains(err.Error(), string(CategoryEmptyInput)) {
		t.Fatalf("empty path reason missing: %v", err)
	}
}

// The nested/squash/remain/omit matrix is deterministic across regenerations.
func TestBindingCombinationTable(t *testing.T) {
	first, err := GenerateBindingTable(&bindingSquash{}, "")
	if err != nil {
		t.Fatalf("table generation failed: %v", err)
	}

	second, err := GenerateBindingTable(&bindingSquash{}, "")
	if err != nil {
		t.Fatalf("table regeneration failed: %v", err)
	}

	if first.Signature() != second.Signature() {
		t.Fatal("combination table drifted across regenerations")
	}

	byPath := map[string]BindingCombination{}

	for index, row := range first.Rows {
		if strings.TrimSpace(row.Path) == "" || strings.TrimSpace(row.Expectation) == "" {
			t.Fatalf("blank row was emitted at index %d: %+v", index, row)
		}

		if _, exists := byPath[row.Path]; exists {
			t.Fatalf("duplicate row not suppressed: %s", row.Path)
		}

		byPath[row.Path] = row
	}

	squashed := byPath["trace_id"]
	if !squashed.Squashed || squashed.Kind != reflect.String {
		t.Fatalf("squashed embedded leaf misreported: %+v", squashed)
	}

	if !byPath["inner"].Nested || byPath["inner.city"].Path != "inner.city" {
		t.Fatalf("nested combination missing: %+v", byPath)
	}

	if !byPath["Rest"].Remain {
		t.Fatalf("remain combination missing: %+v", byPath["Rest"])
	}

	if !byPath["count"].OmitEmpty {
		t.Fatalf("omitzero combination missing: %+v", byPath["count"])
	}

	for index := 1; index < len(first.Rows); index++ {
		if first.Rows[index-1].Path > first.Rows[index].Path {
			t.Fatal("combination table is not sorted")
		}
	}
}

// A type with no bindable fields refuses to masquerade as generated.
func TestBindingCombinationTableEmptyHalts(t *testing.T) {
	type noBindableFields struct {
		private string
	}

	if _, err := GenerateBindingTable(&noBindableFields{}, ""); err == nil {
		t.Fatal("empty table generation should halt")
	}
}

// The two-pass path reports a generic-map key that never reaches the struct.
func TestTwoPassReportsLostField(t *testing.T) {
	type twoPassInner struct {
		City string `mapstructure:"city,required"`
	}

	type twoPassOuter struct {
		Name  string       `mapstructure:"name,required"`
		Inner twoPassInner `mapstructure:"inner"`
	}

	input := map[string]any{
		"name":  "carol",
		"inner": map[string]any{"city": "sh"},
		"ghost": 1,
	}

	var outer twoPassOuter

	report, err := DecodeTwoPass(input, &outer, BindingPolicy{RequireMissing: true})
	if err == nil {
		t.Fatal("lost field was silently skipped")
	}

	assertIssue(t, &BindingReport{Issues: report.Issues}, CategoryFieldLost, "ghost")

	if outer.Name != "carol" || outer.Inner.City != "sh" {
		t.Fatalf("reachable fields were not decoded: %+v", outer)
	}

	if len(report.GenericKeys) == 0 || len(report.TypedKeys) == 0 {
		t.Fatal("two-pass audit trails were empty")
	}
}

// The same input fingerprints identically on a repeated run.
func TestTwoPassInputFingerprintStable(t *testing.T) {
	type fingerprintTarget struct {
		Name string `mapstructure:"name,required"`
	}

	input := map[string]any{"name": "dave"}

	first, err := DecodeTwoPass(input, &fingerprintTarget{}, BindingPolicy{RequireMissing: true})
	if err != nil {
		t.Fatalf("clean two-pass run failed: %v", err)
	}

	second, err := DecodeTwoPass(input, &fingerprintTarget{}, BindingPolicy{RequireMissing: true})
	if err != nil {
		t.Fatalf("repeated two-pass run failed: %v", err)
	}

	if first.InputFingerprint != second.InputFingerprint {
		t.Fatal("input fingerprint changed across repeated runs")
	}
}

func TestTwoPassEmptyPathHalts(t *testing.T) {
	type emptyPathTarget struct {
		Name string `mapstructure:"name"`
	}

	if _, err := DecodeTwoPass(nil, &emptyPathTarget{}, BindingPolicy{}); err == nil {
		t.Fatal("empty two-pass input was treated as executed")
	}
}

// The aggregate gate blocks extra+missing without migration notes, releases
// with notes, and re-gates after the error strategy changes.
func TestAggregateLockMigrationAndReGate(t *testing.T) {
	policy := StrictBindingPolicy()

	var firstPerson bindingPerson

	report, err := DecodeWithBindings(map[string]any{"junk": true}, &firstPerson, policy)
	if err == nil {
		t.Fatal("expected simultaneous missing and unused failures")
	}

	lock := NewAggregateLock(report, policy)
	if err := lock.Lock(); err == nil {
		t.Fatal("breaking aggregate locked without migration notes")
	}

	policy.MigrationNotes = map[IssueCategory]string{
		CategoryMissingRequired: "callers must provide name and age",
		CategoryUnusedKey:       "unknown keys are rejected; remove them",
	}

	var secondPerson bindingPerson

	report, err = DecodeWithBindings(map[string]any{"junk": true}, &secondPerson, policy)
	if err == nil {
		t.Fatal("expected simultaneous missing and unused failures")
	}

	lock = NewAggregateLock(report, policy)
	if err := lock.Lock(); err != nil {
		t.Fatalf("documented aggregate should lock: %v", err)
	}

	if err := lock.Verify(policy); err != nil {
		t.Fatalf("unchanged policy must verify: %v", err)
	}

	changed := policy
	changed.WeaklyTypedInput = true

	if err := lock.Verify(changed); err == nil {
		t.Fatal("changed strategy slipped through without re-gating")
	}

	if lock.Locked() {
		t.Fatal("failed verification must invalidate the lock")
	}
}

// A clean report locks without notes and remains inspectable.
func TestAggregateLockClean(t *testing.T) {
	input := map[string]any{
		"name": "erin",
		"age":  29,
		"addr": map[string]any{"city": "sh"},
	}

	var person bindingPerson

	report, err := DecodeWithBindings(input, &person, StrictBindingPolicy())
	if err != nil {
		t.Fatalf("clean input failed: %v", err)
	}

	lock := NewAggregateLock(report, StrictBindingPolicy())
	if err := lock.Lock(); err != nil {
		t.Fatalf("clean aggregate failed to lock: %v", err)
	}

	if !lock.Locked() {
		t.Fatal("clean aggregate should report locked")
	}
}
