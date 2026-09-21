package mapstructure

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/go-viper/mapstructure/v2/internal/errors"
)

// BindingCase is one row of the nested-combination matrix: a single
// combination of nesting, squashing, remainder keys and empty-value omission
// expressed as something runnable and comparable on both passes.
type BindingCase struct {
	// Name is the unique case identifier; duplicates stop the whole matrix.
	Name string

	// Input is the generic map fed to the decoder.
	Input map[string]any

	// Nested keeps the child fields under a "child" struct key.
	Nested bool

	// Squash flattens the child struct into the parent. Mutually exclusive with
	// Nested; setting both is a matrix-level rejection.
	Squash bool

	// Remain collects leftover keys in a remain-tagged map field.
	Remain bool

	// OmitEmpty omits the child field on the struct->map pass when empty.
	OmitEmpty bool

	// ExpectKeys are the flattened top-level keys that must survive the
	// struct->map pass, sorted.
	ExpectKeys []string

	// ExpectRemain lists the keys that must land in the remainder field.
	ExpectRemain []string
}

// BindingMatrixResult is the outcome of running a matrix. When Valid is false
// the matrix stopped before producing anything and Reason explains why; in that
// case Findings must not be treated as generated output.
type BindingMatrixResult struct {
	Valid    bool
	Reason   string
	Findings map[string]*BindingReport
}

// RunBindingMatrix validates and runs a combination table. It rejects the table
// as a whole (no half output) when it is empty, contains a duplicate/empty
// name, contradicts itself (Nested+Squash), or any case disagrees with its
// expected flattening/remainder.
func (d *Decoder) RunBindingMatrix(cases []BindingCase) BindingMatrixResult {
	if len(cases) == 0 {
		return BindingMatrixResult{
			Valid:  false,
			Reason: "binding matrix is empty: nothing to generate",
		}
	}

	seen := map[string]struct{}{}
	findings := make(map[string]*BindingReport, len(cases))

	for _, tc := range cases {
		if strings.TrimSpace(tc.Name) == "" {
			return BindingMatrixResult{
				Valid:  false,
				Reason: "binding matrix contains a case with an empty name",
			}
		}

		if _, dup := seen[tc.Name]; dup {
			return BindingMatrixResult{
				Valid:  false,
				Reason: fmt.Sprintf("binding matrix contains duplicate case %q", tc.Name),
			}
		}

		seen[tc.Name] = struct{}{}

		report, err := d.runBindingCase(tc)
		if err != nil {
			return BindingMatrixResult{
				Valid:  false,
				Reason: fmt.Sprintf("case %q stopped: %s", tc.Name, err.Error()),
			}
		}

		findings[tc.Name] = report
	}

	return BindingMatrixResult{Valid: true, Findings: findings}
}

// runBindingCase builds the dynamic target, runs map->struct->map with the
// matrix owner's configuration, and compares against the expectation.
func (d *Decoder) runBindingCase(tc BindingCase) (*BindingReport, error) {
	if tc.Nested && tc.Squash {
		return nil, errors.New("Nested and Squash are mutually exclusive")
	}

	targetType := buildBindingCaseType(tc)
	target := reflect.New(targetType).Interface()

	cfg := &BindingConfig{
		DecoderConfig:       *d.config,
		ReportZeroAmbiguous: d.binding != nil && d.binding.zeroAmbiguous,
	}
	cfg.DecoderConfig.Metadata = nil

	dec, err := prepareBindingDecoder(cfg, target)
	if err != nil {
		return nil, err
	}

	report, err := dec.runBinding(tc.Input)
	if err != nil {
		return nil, err
	}

	// Second pass: struct -> generic map.
	out := map[string]any{}

	outCfg := cfg.DecoderConfig
	outCfg.Result = &out
	outCfg.Metadata = nil

	outDec, err := NewDecoder(&outCfg)
	if err != nil {
		return nil, err
	}

	if err := outDec.Decode(target); err != nil {
		return nil, err
	}

	topKeys := make([]string, 0, len(out))

	for k := range out {
		topKeys = append(topKeys, k)
	}

	sort.Strings(topKeys)

	if !sameOrderedStrings(topKeys, dedupSortedStrings(tc.ExpectKeys)) {
		return nil, fmt.Errorf(
			"flattened keys %v do not match expected %v",
			topKeys, dedupSortedStrings(tc.ExpectKeys))
	}

	var remainKeys []string

	if tc.Remain {
		if rv := reflect.ValueOf(target).Elem().FieldByName("Rest"); rv.IsValid() {
			remainKeys = mapKeysSorted(rv.Interface())
		}
	}

	if !sameStringSet(remainKeys, tc.ExpectRemain) {
		return nil, fmt.Errorf(
			"remainder keys %v do not match expected %v",
			remainKeys, dedupSortedStrings(tc.ExpectRemain))
	}

	return report, nil
}

func mapKeysSorted(v any) []string {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Map {
		return nil
	}

	out := make([]string, 0, rv.Len())

	for _, k := range rv.MapKeys() {
		if s, ok := k.Interface().(string); ok {
			out = append(out, s)
		}
	}

	sort.Strings(out)

	return out
}

// buildBindingCaseType constructs a struct for one combination:
//
//	type Child struct { Name string `mapstructure:"name"` }
//	type Row   struct {
//	    Child Child            `mapstructure:"child"` | `mapstructure:",squash"`
//	    Rest  map[string]any   `mapstructure:",remain"` (optional)
//	}
func buildBindingCaseType(tc BindingCase) reflect.Type {
	child := reflect.StructOf([]reflect.StructField{
		{
			Name: "Name",
			Type: reflect.TypeOf(""),
			Tag:  reflect.StructTag(`mapstructure:"name"`),
		},
	})

	tag := `mapstructure:"child"`

	if tc.Squash {
		tag = `mapstructure:",squash"`
	} else if tc.OmitEmpty {
		tag = `mapstructure:"child,omitempty"`
	}

	fields := []reflect.StructField{
		{
			Name:      "Child",
			Type:      child,
			Tag:       reflect.StructTag(tag),
			Anonymous: tc.Squash,
		},
	}

	if tc.OmitEmpty {
		// Tags is an empty-able slice so omitempty has an observable effect on
		// the struct->map pass (empty structs are not omitted by isEmptyValue).
		fields = append(fields, reflect.StructField{
			Name: "Tags",
			Type: reflect.TypeOf([]string{}),
			Tag:  reflect.StructTag(`mapstructure:"tags,omitempty"`),
		})
	}

	if tc.Remain {
		fields = append(fields, reflect.StructField{
			Name: "Rest",
			Type: reflect.TypeOf(map[string]any{}),
			Tag:  reflect.StructTag(`mapstructure:",remain"`),
		})
	}

	return reflect.StructOf(fields)
}
