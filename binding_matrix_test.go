package mapstructure

import "testing"

func matrixDecoder(t *testing.T, weak bool) *Decoder {
	t.Helper()

	var out map[string]any

	dec, err := NewDecoder(&DecoderConfig{
		Result:           &out,
		WeaklyTypedInput: weak,
	})
	if err != nil {
		t.Fatal(err)
	}

	return dec
}

func TestBindingMatrix_NestedSquashRemainOmitEmpty(t *testing.T) {
	t.Parallel()

	cases := []BindingCase{
		{
			Name:       "nested",
			Nested:     true,
			Input:      map[string]any{"child": map[string]any{"name": "a"}},
			ExpectKeys: []string{"child"},
		},
		{
			Name:       "squash",
			Squash:     true,
			Input:      map[string]any{"name": "a"},
			ExpectKeys: []string{"name"},
		},
		{
			Name:         "squash_with_remain",
			Squash:       true,
			Remain:       true,
			Input:        map[string]any{"name": "a", "leftover": 1},
			ExpectKeys:   []string{"leftover", "name"},
			ExpectRemain: []string{"leftover"},
		},
		{
			Name:      "omitempty_drops_empty_slice",
			OmitEmpty: true,
			Input: map[string]any{
				"child": map[string]any{"name": "a"},
				"tags":  []string{},
			},
			ExpectKeys: []string{"child"},
		},
		{
			// Control case: a non-empty slice survives omitempty, proving the
			// empty case above is dropped due to emptiness, not to the key name.
			Name:      "omitempty_keeps_nonempty_slice",
			OmitEmpty: true,
			Input: map[string]any{
				"child": map[string]any{"name": "a"},
				"tags":  []string{"x"},
			},
			ExpectKeys: []string{"child", "tags"},
		},
	}

	res := matrixDecoder(t, false).RunBindingMatrix(cases)
	if !res.Valid {
		t.Fatalf("matrix should be valid: %s", res.Reason)
	}

	if len(res.Findings) != len(cases) {
		t.Fatalf("expected %d findings maps, got %d", len(cases), len(res.Findings))
	}
}

func TestBindingMatrix_DeterministicRerun(t *testing.T) {
	t.Parallel()

	cases := []BindingCase{
		{Name: "squash", Squash: true, Input: map[string]any{"name": "a"}, ExpectKeys: []string{"name"}},
		{Name: "nested", Nested: true, Input: map[string]any{"child": map[string]any{"name": "a"}}, ExpectKeys: []string{"child"}},
	}

	first := matrixDecoder(t, false).RunBindingMatrix(cases)
	second := matrixDecoder(t, false).RunBindingMatrix(cases)

	if !first.Valid || !second.Valid {
		t.Fatalf("both runs must be valid: %q / %q", first.Reason, second.Reason)
	}

	if len(first.Findings) != len(second.Findings) {
		t.Fatal("matrix rerun produced different finding counts")
	}
}

func TestBindingMatrix_EmptyTableStops(t *testing.T) {
	t.Parallel()

	res := matrixDecoder(t, false).RunBindingMatrix(nil)
	if res.Valid {
		t.Fatal("empty matrix must not be valid")
	}

	if res.Reason == "" || res.Findings != nil {
		t.Fatalf("empty matrix must carry a reason and no findings: %+v", res)
	}
}

func TestBindingMatrix_DuplicateStopsWholeMatrix(t *testing.T) {
	t.Parallel()

	cases := []BindingCase{
		{Name: "dup", Squash: true, Input: map[string]any{"name": "a"}, ExpectKeys: []string{"name"}},
		{Name: "dup", Squash: true, Input: map[string]any{"name": "b"}, ExpectKeys: []string{"name"}},
	}

	res := matrixDecoder(t, false).RunBindingMatrix(cases)
	if res.Valid {
		t.Fatal("duplicate case must stop the whole matrix")
	}

	if res.Findings != nil {
		t.Fatal("a stopped matrix must not hand back partial findings")
	}
}

func TestBindingMatrix_EmptyCaseNameStops(t *testing.T) {
	t.Parallel()

	cases := []BindingCase{
		{Name: "  ", Squash: true, Input: map[string]any{"name": "a"}, ExpectKeys: []string{"name"}},
	}

	if res := matrixDecoder(t, false).RunBindingMatrix(cases); res.Valid {
		t.Fatal("empty-name case must stop the matrix")
	}
}

func TestBindingMatrix_NestedAndSquashContradictionStops(t *testing.T) {
	t.Parallel()

	cases := []BindingCase{
		{Name: "bad", Nested: true, Squash: true, Input: map[string]any{"name": "a"}},
	}

	res := matrixDecoder(t, false).RunBindingMatrix(cases)
	if res.Valid {
		t.Fatal("nested+squash contradiction must stop the matrix")
	}
}

func TestBindingMatrix_ExpectationMismatchStops(t *testing.T) {
	t.Parallel()

	cases := []BindingCase{
		// Squash yields "name", but the table wrongly expects "child".
		{Name: "wrong", Squash: true, Input: map[string]any{"name": "a"}, ExpectKeys: []string{"child"}},
	}

	res := matrixDecoder(t, false).RunBindingMatrix(cases)
	if res.Valid {
		t.Fatal("expectation mismatch must stop the matrix")
	}

	if res.Reason == "" {
		t.Fatal("mismatch must report its reason")
	}
}
