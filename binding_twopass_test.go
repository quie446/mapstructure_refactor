package mapstructure

import "testing"

type bindOrder struct {
	Item string `mapstructure:"item,required"`
	Qty  int    `mapstructure:"qty"`
}

func TestTwoPassBinding_HappyPath(t *testing.T) {
	t.Parallel()

	input := map[string]any{"item": "book", "qty": 3}

	cfg := &BindingConfig{DecoderConfig: DecoderConfig{}}

	var out bindOrder

	report, err := TwoPassBinding(cfg, input, &out)
	if err != nil {
		t.Fatalf("two-pass happy path should succeed: %v", err)
	}

	if report.Len() != 0 {
		t.Fatalf("expected no findings, got %#v", report.Errors())
	}

	if out.Item != "book" || out.Qty != 3 {
		t.Fatalf("second pass lost fields: %+v", out)
	}
}

func TestTwoPassBinding_EmptyPathIsError(t *testing.T) {
	t.Parallel()

	cfg := &BindingConfig{DecoderConfig: DecoderConfig{}}

	var out bindOrder

	_, err := TwoPassBinding(cfg, map[string]any{}, &out)
	if err == nil {
		t.Fatal("empty two-pass path must be an error, not a silent success")
	}

	_, err = TwoPassBinding(cfg, nil, &out)
	if err == nil {
		t.Fatal("nil two-pass path must be an error")
	}
}

func TestTwoPassBinding_WeakBoundaryDriftReported(t *testing.T) {
	t.Parallel()

	// With weak conversion off, qty string -> int fails on the strong pass but
	// the generic pass keeps it; this must surface as a type mismatch rather
	// than a silently skipped field.
	input := map[string]any{"item": "book", "qty": "lots"}

	cfg := &BindingConfig{DecoderConfig: DecoderConfig{WeaklyTypedInput: false}}

	var out bindOrder

	report, err := TwoPassBinding(cfg, input, &out)
	if err == nil {
		t.Fatal("expected weak boundary drift to be reported")
	}

	if !report.Has(BindingTypeMismatch) {
		t.Fatalf("expected type mismatch on two-pass drift, got %#v", report.Errors())
	}
}

func TestTwoPassBinding_MissingRequiredReported(t *testing.T) {
	t.Parallel()

	input := map[string]any{"qty": 1}

	cfg := &BindingConfig{DecoderConfig: DecoderConfig{}}

	var out bindOrder

	report, err := TwoPassBinding(cfg, input, &out)
	if err == nil {
		t.Fatal("expected missing required error")
	}

	if !report.Has(BindingMissingRequired) {
		t.Fatalf("expected missing-required finding, got %#v", report.Errors())
	}
}
