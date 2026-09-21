package mapstructure

// requiredTagOption is the struct-tag option that marks a field as required
// for the structured binding layer. It composes with the existing
// "mapstructure" tag, e.g. `mapstructure:"name,required"`.
const requiredTagOption = "required"
