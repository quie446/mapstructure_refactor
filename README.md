# mapstructure

[![GitHub Workflow Status](https://img.shields.io/github/actions/workflow/status/go-viper/mapstructure/ci.yaml?style=flat-square)](https://github.com/go-viper/mapstructure/actions/workflows/ci.yaml)
[![go.dev reference](https://img.shields.io/badge/go.dev-reference-007d9c?logo=go&logoColor=white&style=flat-square)](https://pkg.go.dev/mod/github.com/go-viper/mapstructure/v2)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/go-viper/mapstructure?style=flat-square&color=61CFDD)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/go-viper/mapstructure/badge?style=flat-square)](https://deps.dev/go/github.com%252Fgo-viper%252Fmapstructure%252Fv2)

mapstructure is a Go library for decoding generic map values to structures
and vice versa, while providing helpful error handling.

This library is most useful when decoding values from some data stream (JSON,
Gob, etc.) where you don't _quite_ know the structure of the underlying data
until you read a part of it. You can therefore read a `map[string]interface{}`
and use this library to decode it into the proper underlying native Go
structure.

## Installation

```shell
go get github.com/go-viper/mapstructure/v2
```

## Migrating from `github.com/mitchellh/mapstructure`

[@mitchehllh](https://github.com/mitchellh) announced his intent to archive some of his unmaintained projects (see [here](https://gist.github.com/mitchellh/90029601268e59a29e64e55bab1c5bdc) and [here](https://github.com/mitchellh/mapstructure/issues/349)). This is a repository achieved the "blessed fork" status.

You can migrate to this package by changing your import paths in your Go files to `github.com/go-viper/mapstructure/v2`.
The API is the same, so you don't need to change anything else.

Here is a script that can help you with the migration:

```shell
sed -i 's|github.com/mitchellh/mapstructure|github.com/go-viper/mapstructure/v2|g' $(find . -type f -name '*.go')
```

If you need more time to migrate your code, that is absolutely fine.

Some of the latest fixes are backported to the v1 release branch of this package, so you can use the Go modules `replace` feature until you are ready to migrate:

```shell
replace github.com/mitchellh/mapstructure => github.com/go-viper/mapstructure v1.6.0
```

## Usage & Example

For usage and examples see the [documentation](https://pkg.go.dev/mod/github.com/go-viper/mapstructure/v2).

The `Decode` function has examples associated with it there.

## But Why?!

Go offers fantastic standard libraries for decoding formats such as JSON.
The standard method is to have a struct pre-created, and populate that struct
from the bytes of the encoded format. This is great, but the problem is if
you have configuration or an encoding that changes slightly depending on
specific fields. For example, consider this JSON:

```json
{
  "type": "person",
  "name": "Mitchell"
}
```

Perhaps we can't populate a specific structure without first reading
the "type" field from the JSON. We could always do two passes over the
decoding of the JSON (reading the "type" first, and the rest later).
However, it is much simpler to just decode this into a `map[string]interface{}`
structure, read the "type" key, then use something like this library
to decode it into the proper structure.

## Structured binding corrections

On top of the ordinary `Decode` flow, an opt-in binding layer classifies
decoding outcomes into four separately inspectable error classes without
introducing a second decoder:

- `BindingMissingRequired` — a field tagged `mapstructure:"...,required"` had
  no matching input key.
- `BindingTypeMismatch` — a value could not be converted to the field type;
  each finding records whether `WeaklyTypedInput` was on, so strict and weak
  runs are directly comparable.
- `BindingUnusedKey` — an input key no field consumed.
- `BindingZeroAmbiguous` — a present key decoded to the zero value, where an
  explicit zero cannot be distinguished from an absent-field default.

Use `DecodeWithBindings` for the classified report, `TwoPassBinding` for the
generic-map → strong-type two-pass path, and `(*BindingReport).Lock` together
with `VerifyLock` to freeze and re-verify the ordered conclusion across
machines. `Decoder.RunBindingMatrix` runs a table of
nested/squash/remain/omitempty combinations and stops the whole batch on an
empty table, duplicate case, or expectation mismatch.

These entry points never change the decoded result and never affect the
historical `Decode`/`WeakDecode` behavior; enabling them only adds findings.

## Credits

Mapstructure was originally created by [@mitchellh](https://github.com/mitchellh).
This is a maintained fork of the original library.

Read more about the reasons for the fork [here](https://github.com/mitchellh/mapstructure/issues/349).

## License

The project is licensed under the [MIT License](LICENSE).
