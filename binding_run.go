package mapstructure

import (
	"reflect"
	"sort"
	"strings"

	"github.com/go-viper/mapstructure/v2/internal/errors"
)

// BindingConfig configures the structured binding entry points. It reuses the
// ordinary DecoderConfig for the actual walk and only adds binding-layer
// switches, so a single decode path serves everything.
type BindingConfig struct {
	// DecoderConfig is used for the underlying walk. Result and Metadata are
	// managed by the binding entry points.
	DecoderConfig DecoderConfig

	// ReportZeroAmbiguous enables the zero-value vs absent-field class.
	ReportZeroAmbiguous bool
}

// DecodeWithBindings decodes input into result through the existing decoder and
// returns a structured, classified report. The returned error is the report's
// ordered join; nil means every binding class passed.
//
// Historical stability is preserved: this entry only reports required-tag,
// unused-key and (optionally) zero-ambiguity findings and never changes the
// decoded result.
func DecodeWithBindings(cfg *BindingConfig, input map[string]any, result any) (*BindingReport, error) {
	dec, err := prepareBindingDecoder(cfg, result)
	if err != nil {
		return nil, err
	}

	return dec.runBinding(input)
}

// prepareBindingDecoder builds a Decoder whose struct walk shares the binding
// state. Metadata is always collected so unused keys come from the same walk.
func prepareBindingDecoder(cfg *BindingConfig, result any) (*Decoder, error) {
	if cfg == nil {
		return nil, errors.New("binding config must not be nil")
	}

	if result == nil {
		return nil, errors.New("binding result must not be nil")
	}

	dcfg := cfg.DecoderConfig
	dcfg.Result = result
	dcfg.Metadata = &Metadata{}

	dec, err := NewDecoder(&dcfg)
	if err != nil {
		return nil, err
	}

	dec.binding = &bindingState{
		report:        &BindingReport{},
		required:      map[string]bool{},
		present:       map[string]bool{},
		unused:        map[string]bool{},
		decodedOK:     map[string]bool{},
		zeroAmbiguous: cfg.ReportZeroAmbiguous,
	}

	dec.collectRequiredFields("", reflect.TypeOf(result).Elem(), dec.binding.required)

	return dec, nil
}

// collectRequiredFields registers the fully qualified path of every field
// carrying the ",required" option. Squashed embedded structs contribute fields
// at the parent level, matching decodeStructFromMap name resolution.
func (d *Decoder) collectRequiredFields(prefix string, typ reflect.Type, into map[string]bool) {
	typ = derefType(typ)
	if typ.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" {
			continue
		}

		tagParts := getTagParts(f, d.config.TagName)
		if len(tagParts) == 0 {
			tagParts = []string{""}
		}

		squash := d.config.Squash && f.Anonymous
		required := false

		for _, opt := range tagParts[1:] {
			if opt == d.config.SquashTagOption {
				squash = true
			}

			if opt == requiredTagOption {
				required = true
			}
		}

		if squash {
			d.collectRequiredFields(prefix, f.Type, into)
			continue
		}

		name := f.Name
		if tv := strings.SplitN(tagParts[0], ",", 2)[0]; tv != "" {
			name = tv
		} else {
			name = d.config.MapFieldName(name)
		}

		path := joinPath(prefix, name)

		if required {
			into[path] = true
		}

		d.collectRequiredFields(path, f.Type, into)
	}
}

// runBinding decodes and then classifies every outcome into distinct classes.
func (d *Decoder) runBinding(input map[string]any) (*BindingReport, error) {
	b := d.binding

	decodeErr := d.decode(d.config.RootName, input,
		reflect.ValueOf(d.config.Result).Elem())

	for path := range b.required {
		if !b.present[path] {
			b.report.add(
				BindingMissingRequired, path,
				"field is marked required but no matching key was present",
				d, nil, nil)
		}
	}

	for _, key := range sortedKeySet(b.unused) {
		b.report.add(BindingUnusedKey, key,
			"input key was not consumed by any field", d, nil, nil)
	}

	if b.zeroAmbiguous {
		d.classifyZeroAmbiguous("", reflect.ValueOf(d.config.Result).Elem(), b.report)
	}

	d.classifyDecodeErrors(decodeErr, b.report)

	b.report.sortLocked()

	return b.report, b.report.Err()
}

// classifyZeroAmbiguous flags present keys whose decoded value is the zero
// value, where explicit zero cannot be distinguished from an absent default.
func (d *Decoder) classifyZeroAmbiguous(prefix string, val reflect.Value, report *BindingReport) {
	val = reflect.Indirect(val)
	if !val.IsValid() || val.Kind() != reflect.Struct {
		return
	}

	typ := val.Type()

	for i := 0; i < typ.NumField(); i++ {
		fv := val.Field(i)
		if !fv.CanInterface() {
			continue
		}

		sf := typ.Field(i)

		tagParts := getTagParts(sf, d.config.TagName)
		if len(tagParts) == 0 {
			tagParts = []string{""}
		}

		squash := d.config.Squash && sf.Anonymous

		for _, opt := range tagParts[1:] {
			if opt == d.config.SquashTagOption {
				squash = true
			}
		}

		name := sf.Name
		if tv := strings.SplitN(tagParts[0], ",", 2)[0]; tv != "" {
			name = tv
		} else {
			name = d.config.MapFieldName(name)
		}

		path := joinPath(prefix, name)
		indirect := reflect.Indirect(fv)

		if squash && indirect.IsValid() && indirect.Kind() == reflect.Struct {
			d.classifyZeroAmbiguous(prefix, indirect, report)
			continue
		}

		if d.binding.present[path] && d.binding.decodedOK[path] && fv.IsZero() {
			report.add(BindingZeroAmbiguous, path,
				"key was present but decoded to the zero value; cannot distinguish explicit zero from absent default",
				d, fv.Type(), fv.Interface())
		}

		if indirect.IsValid() && indirect.Kind() == reflect.Struct {
			d.classifyZeroAmbiguous(path, indirect, report)
		}
	}
}

// classifyDecodeErrors turns each leaf error of the walk into a classified
// type-mismatch finding, recognizing the exported markers and retaining the
// original cause so nothing is silently dropped.
func (d *Decoder) classifyDecodeErrors(err error, report *BindingReport) {
	if err == nil {
		return
	}

	for _, leaf := range flattenErrors(err) {
		var (
			path   = errorPath(leaf)
			reason = leaf.Error()
			wanted reflect.Type
			got    any
		)

		var unconvertible *UnconvertibleTypeError
		if errors.As(leaf, &unconvertible) {
			wanted = unconvertible.Expected.Type()
			got = unconvertible.Value
			reason = unconvertible.Error()
		}

		var parseErr *ParseError
		if errors.As(leaf, &parseErr) {
			wanted = parseErr.Expected.Type()
			got = parseErr.Value
			reason = parseErr.Error()
		}

		report.add(BindingTypeMismatch, path, reason, d, wanted, got)
	}
}

// flattenErrors expands joined errors while keeping single errors intact.
func flattenErrors(err error) []error {
	if err == nil {
		return nil
	}

	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		var out []error

		for _, e := range joined.Unwrap() {
			out = append(out, flattenErrors(e)...)
		}

		return out
	}

	return []error{err}
}

// errorPath extracts the qualified field name from a DecodeError when present.
func errorPath(err error) string {
	var de *DecodeError
	if errors.As(err, &de) {
		return de.Name()
	}

	return ""
}

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	return t
}

func sortedKeySet(m map[string]bool) []string {
	out := make([]string, 0, len(m))

	for k := range m {
		if k != "" {
			out = append(out, k)
		}
	}

	sort.Strings(out)

	return out
}
