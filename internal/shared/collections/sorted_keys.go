package collections

import "slices"

// SortedKeys returns m's keys in sorted order — the deterministic iteration
// order a text/table render, hash-stable output, or any other order-sensitive
// consumer needs over an otherwise order-nondeterministic map. Nil in, empty
// (non-nil) slice out.
func SortedKeys[K ~string, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
