package mapstructure

import (
	"errors"
	"testing"
)

type bindPerson struct {
	Name string `mapstructure:"name,required"`
	Age  int    `mapstructure:"age,required"`
	City string `mapstructure:"city"`
}

func bindingCfg(weak bool) *BindingConfig {
	return &BindingConfig{
		DecoderConfig: DecoderConfig{
			WeaklyTypedInput: weak,
		},
		ReportZeroAmbiguous: true,
	}
}

func TestBinding_DistinctErrorClasses(t *testing.T) {
	t.Parallel()

	input := map[string]any{
		"age":   "not-an-int", // type cannot convert (weak off)
		"city":  "NYC",
		"extra": 1, // unused key
		// "name" missing -> missing required
	}

	var p bindPerson

	report, err := DecodeWithBindings(bindingCfg(false), input, &p)
	if err == nil {
		t.Fatal("expected a binding error")
	}

	if !report.Has(BindingMissingRequired) {
		t.Errorf("expected missing-required finding, got %#v", report.Errors())
	}

	if !report.Has(BindingTypeMismatch) {
		t.Errorf("expected type-mismatch finding, got %#v", report.Errors())
	}

	if !report.Has(BindingUnusedKey) {
		t.Errorf("expected unused-key finding, got %#v", report.Errors())
	}

	// Zero ambiguity must not fire: the present field that decoded (city) is
	// non-zero, and age was a type error rather than a decoded zero.
	if report.Has(BindingZeroAmbiguous) {
		t.Errorf("did not expect zero-ambiguous finding, got %#v", report.Errors())
	}

	// Each finding is individually inspectable and carries a reason.
	for _, e := range report.Errors() {
		if e.Reason == "" {
			t.Errorf("finding %s has no reason", e.Kind)
		}

		if e.WeaklyTyped {
			t.Errorf("finding must record weak=false, got true")
		}
	}

	// Missing required and unused key coexist in one ordered, queryable set.
	missing := report.ByKind(BindingMissingRequired)
	if len(missing) != 1 || missing[0].Path != "name" {
		t.Fatalf("unexpected missing findings: %#v", missing)
	}

	unused := report.ByKind(BindingUnusedKey)
	if len(unused) != 1 || unused[0].Path != "extra" {
		t.Fatalf("unexpected unused findings: %#v", unused)
	}
}

func TestBinding_WeakConversionOnOffContrast(t *testing.T) {
	t.Parallel()

	input := map[string]any{
		"name": "bob",
		"age":  "42",
	}

	// Weak OFF: string -> int is a type mismatch.
	var strict bindPerson

	strictReport, strictErr := DecodeWithBindings(bindingCfg(false), input, &strict)
	if strictErr == nil || !strictReport.Has(BindingTypeMismatch) {
		t.Fatalf("expected strict type mismatch, report=%#v err=%v", strictReport, strictErr)
	}

	if strictReport.Errors()[0].WeaklyTyped {
		t.Fatal("strict report must record WeaklyTyped=false")
	}

	// Weak ON: same input binds cleanly and converts the age.
	var weak bindPerson

	weakReport, weakErr := DecodeWithBindings(bindingCfg(true), input, &weak)
	if weakErr != nil || weakReport.Has(BindingTypeMismatch) {
		t.Fatalf("expected weak success, report=%#v err=%v", weakReport, weakErr)
	}

	if weak.Age != 42 {
		t.Fatalf("weak conversion did not apply, age=%d", weak.Age)
	}

	for _, e := range weakReport.Errors() {
		if !e.WeaklyTyped {
			t.Fatal("weak report must record WeaklyTyped=true")
		}
	}
}

func TestBinding_ZeroAmbiguous(t *testing.T) {
	t.Parallel()

	// "age" is present but explicitly zero: ambiguous with an absent default.
	input := map[string]any{
		"name": "a",
		"age":  0,
	}

	var p bindPerson

	report, err := DecodeWithBindings(bindingCfg(true), input, &p)
	if err == nil {
		t.Fatal("expected zero-ambiguity error")
	}

	if !report.Has(BindingZeroAmbiguous) {
		t.Fatalf("expected zero-ambiguous finding, got %#v", report.Errors())
	}

	var be *BindingError
	if !errors.As(report.Err(), &be) {
		t.Fatal("joined error must expose *BindingError via errors.As")
	}
}

func TestBinding_LegacyBindingsStillPass(t *testing.T) {
	t.Parallel()

	// A previously stable, fully-specified binding with no required tags must
	// not be rejected by the tightened layer.
	type legacy struct {
		Vstring string
		Vint    int
	}

	input := map[string]any{"Vstring": "ok", "Vint": 7}

	cfg := &BindingConfig{DecoderConfig: DecoderConfig{}}

	var out legacy

	report, err := DecodeWithBindings(cfg, input, &out)
	if err != nil {
		t.Fatalf("legacy binding must pass: %v", err)
	}

	if report.Len() != 0 {
		t.Fatalf("legacy binding must have no findings, got %#v", report.Errors())
	}

	if out.Vstring != "ok" || out.Vint != 7 {
		t.Fatalf("legacy values changed: %+v", out)
	}
}

func TestBinding_DeterministicOrderAcrossRuns(t *testing.T) {
	t.Parallel()

	input := map[string]any{
		"age":   42,
		"extra": "x",
		"other": "y",
		// name missing
	}

	run := func() []*BindingError {
		var p bindPerson

		report, err := DecodeWithBindings(bindingCfg(false), input, &p)
		if err == nil {
			t.Fatal("expected error")
		}

		return report.Errors()
	}

	first := run()
	second := run()

	if len(first) != len(second) {
		t.Fatalf("run lengths differ: %d vs %d", len(first), len(second))
	}

	for i := range first {
		if first[i].Error() != second[i].Error() {
			t.Fatalf("non-deterministic order/content at %d:\n%s\n%s",
				i, first[i].Error(), second[i].Error())
		}
	}
}

func TestBinding_LockAndVerifyDrift(t *testing.T) {
	t.Parallel()

	input := map[string]any{"age": "nope", "city": "x", "extra": 1}

	var p bindPerson

	cfg := bindingCfg(false)

	report, err := DecodeWithBindings(cfg, input, &p)
	if err == nil {
		t.Fatal("expected error")
	}

	lock, lerr := report.Lock(false)
	if lerr != nil {
		t.Fatal(lerr)
	}

	// Re-running on the same input with the same strategy passes the gate.
	if err := VerifyLock(bindingCfg(false), lock, input, &bindPerson{}); err != nil {
		t.Fatalf("lock re-verification should pass: %v", err)
	}

	// Flipping the weak-conversion switch invalidates the lock.
	if err := VerifyLock(bindingCfg(true), lock, input, &bindPerson{}); err == nil {
		t.Fatal("expected lock invalidation when weak switch changes")
	}
}
