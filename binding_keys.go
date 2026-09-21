package mapstructure

import "sort"

// sortedStringKeySetOf returns the sorted set of string keys in a generic map.
func sortedStringKeySetOf(m map[string]any) []string {
	out := make([]string, 0, len(m))

	for k := range m {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}
