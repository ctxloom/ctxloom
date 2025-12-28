// Package collections provides generic data structure utilities.
//
// # Set Implementation
//
// This package provides a lightweight generic Set type that wraps map[T]struct{}.
// It offers cleaner syntax for common set operations without external dependencies.
//
// # When to Consider a Library
//
// If set usage expands significantly, consider switching to a dedicated library:
//
//   - github.com/deckarep/golang-set/v2
//     Battle-tested, used by Docker/Ethereum/HashiCorp/1Password.
//     Features: thread-safe option, JSON marshaling, full set algebra.
//     Go 1.18+
//
//   - github.com/hashicorp/go-set
//     HashSet (custom hash), TreeSet (sorted), standard Set.
//     Features: Min/Max/TopK for TreeSet, custom hash functions.
//     Go 1.23+, not thread-safe.
//
// Switch triggers:
//   - Need thread-safe sets for concurrent access
//   - Need JSON marshaling/unmarshaling of sets
//   - Need set algebra (Union, Intersection, Difference, SymmetricDifference)
//   - Need sorted iteration (TreeSet)
//   - Need custom hash functions for complex structs
package collections

// Set is a generic set implementation backed by a map.
// The zero value is not usable; create sets with NewSet or NewSetFrom.
type Set[T comparable] map[T]struct{}

// NewSet creates an empty set.
func NewSet[T comparable]() Set[T] {
	return make(Set[T])
}

// NewSetFrom creates a set containing the given elements.
func NewSetFrom[T comparable](elements ...T) Set[T] {
	s := make(Set[T], len(elements))
	for _, e := range elements {
		s[e] = struct{}{}
	}
	return s
}

// Add inserts an element into the set.
func (s Set[T]) Add(v T) {
	s[v] = struct{}{}
}

// AddAll inserts multiple elements into the set.
func (s Set[T]) AddAll(values ...T) {
	for _, v := range values {
		s[v] = struct{}{}
	}
}

// Has returns true if the element is in the set.
func (s Set[T]) Has(v T) bool {
	_, ok := s[v]
	return ok
}

// Remove deletes an element from the set.
func (s Set[T]) Remove(v T) {
	delete(s, v)
}

// Len returns the number of elements in the set.
func (s Set[T]) Len() int {
	return len(s)
}

// Clear removes all elements from the set.
func (s Set[T]) Clear() {
	for k := range s {
		delete(s, k)
	}
}

// Items returns all elements as a slice.
// Order is not guaranteed.
func (s Set[T]) Items() []T {
	result := make([]T, 0, len(s))
	for k := range s {
		result = append(result, k)
	}
	return result
}

// Clone returns a shallow copy of the set.
func (s Set[T]) Clone() Set[T] {
	result := make(Set[T], len(s))
	for k := range s {
		result[k] = struct{}{}
	}
	return result
}
