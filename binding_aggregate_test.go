package mapstructure

import "testing"

type bindInner struct {
	Token string `mapstructure:"token,required"`
}

type bindOuter struct {
	Inner bindInner `mapstructure:"inner"`
	Label string    `mapstructure:"label,required"`
}

func TestBinding_NestedRequiredAndExtraTogether(t *testing.T) {
	t.Parallel()

	// Missing both label and inner.token, plus an extra nested key.
	input := map[string]any{
		"inner": map[string]any{"rogue": true},
	}

	cfg := &BindingConfig{DecoderConfig: DecoderConfig{}}

	var out bindOuter

	report, err := DecodeWithBindings(cfg, input, &out)
	if err == nil {
		t.Fatal("expected aggregate error")
	}

	missing := report.ByKind(BindingMissingRequired)

	paths := map[string]bool{}

	for _, e := range missing {
		paths[e.Path] = true
	}

	if !paths["label"] || !paths["inner.token"] {
		t.Fatalf("expected nested+root required paths, got %#v", missing)
	}

	if !report.Has(BindingUnusedKey) {
		t.Fatalf("expected nested unused key, got %#v", report.Errors())
	}

	// Findings must be individually inspectable in a fixed order.
	errs := report.Errors()

	for i := 1; i < len(errs); i++ {
		if errs[i-1].Path > errs[i].Path {
			t.Fatalf("findings not ordered: %q before %q",
				errs[i-1].Path, errs[i].Path)
		}
	}
}
