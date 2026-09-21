package mapstructure

import (
	stderrors "errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// BindingCombination is one deterministic row describing how a field behaves
// with respect to nesting, squashing, remain keys and empty-value omission.
type BindingCombination struct {
	// Path is the dotted field path, stable across runs and machines.
	Path string

	// Kind is the target value kind.
	Kind reflect.Kind

	// Nested marks a non-squashed struct field.
	Nested bool

	// Squashed marks a field flattened into its parent.
	Squashed bool

	// Remain marks the map that captures residual keys.
	Remain bool

	// OmitEmpty marks omitempty/omitzero fields.
	OmitEmpty bool

	// Required marks fields carrying the required tag option.
	Required bool

	// Expectation is the human readable, machine-stable expected behavior.
	Expectation string
}

// Signature is the deterministic fingerprint of one row.
func (c BindingCombination) Signature() string {
	return strings.Join([]string{
		c.Path,
		c.Kind.String(),
		fmt.Sprintf("nested=%t", c.Nested),
		fmt.Sprintf("squashed=%t", c.Squashed),
		fmt.Sprintf("remain=%t", c.Remain),
		fmt.Sprintf("omitempty=%t", c.OmitEmpty),
		fmt.Sprintf("required=%t", c.Required),
		c.Expectation,
	}, "|")
}

// BindingTable is the reproducible combination matrix for one target type.
type BindingTable struct {
	// Target is the resolved struct type the table describes.
	Target reflect.Type

	// Rows are sorted by path, unique, and never partially populated.
	Rows []BindingCombination
}

// Signature returns a stable fingerprint of the whole table so two machines
// can prove they generated the same matrix.
func (t *BindingTable) Signature() string {
	parts := make([]string, 0, len(t.Rows)+1)
	parts = append(parts, t.Target.String())

	for _, row := range t.Rows {
		parts = append(parts, row.Signature())
	}

	return stableFingerprint(strings.Join(parts, "\n"))
}

// GenerateBindingTable builds the nested/squash/remain/omit combination matrix
// for the pointer's element type. Generation is all-or-nothing: an empty
// result, duplicate path, or blank entry aborts with a clear error instead of
// returning a half-built table.
func GenerateBindingTable(pointer any, tagName string) (*BindingTable, error) {
	if pointer == nil {
		return nil, stderrors.New("binding table: target must not be nil")
	}

	target := reflect.ValueOf(pointer)
	if target.Kind() != reflect.Ptr {
		return nil, stderrors.New("binding table: target must be a pointer to a struct")
	}

	targetType := derefType(target.Elem().Type())
	if targetType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("binding table: target must point to a struct, got %s", targetType.Kind())
	}

	if tagName == "" {
		tagName = "mapstructure"
	}

	rows := []BindingCombination{}

	walkBindings(targetType, tagName, "", false, func(binding fieldBinding) {
		expectation := combinationExpectation(binding)
		rows = append(rows, BindingCombination{
			Path:        binding.path,
			Kind:        binding.kind,
			Nested:      binding.nested && !binding.squash,
			Squashed:    binding.squash,
			Remain:      binding.remain,
			OmitEmpty:   binding.omit,
			Required:    binding.required,
			Expectation: expectation,
		})
	})

	if len(rows) == 0 {
		return nil, stderrors.New("binding table: no bindable fields found; refusing to report an empty table as generated")
	}

	if err := validateGeneratedRows(rows); err != nil {
		return nil, err
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })

	return &BindingTable{Target: targetType, Rows: rows}, nil
}

func validateGeneratedRows(rows []BindingCombination) error {
	seen := map[string]struct{}{}

	for index, row := range rows {
		if strings.TrimSpace(row.Path) == "" {
			return fmt.Errorf("binding table: row %d has an empty path; generation halted", index)
		}

		if strings.TrimSpace(row.Expectation) == "" {
			return fmt.Errorf("binding table: row %q has an empty expectation; generation halted", row.Path)
		}

		if _, exists := seen[row.Path]; exists {
			return fmt.Errorf("binding table: duplicate combination for %q was not suppressed; generation halted", row.Path)
		}

		seen[row.Path] = struct{}{}
	}

	return nil
}

func combinationExpectation(binding fieldBinding) string {
	switch {
	case binding.remain:
		return "collects every residual input key into the remain map"
	case binding.squash:
		return "reads flattened keys from the parent object instead of a nested object"
	case binding.nested:
		return "requires a nested object and decodes its fields recursively"
	}

	parts := []string{"binds one input key to the typed field"}

	if binding.required {
		parts = append(parts, "missing key is a binding failure")
	}

	if binding.omit {
		parts = append(parts, "zero values are omitted when emitted back to a map")
	}

	return strings.Join(parts, "; ")
}
