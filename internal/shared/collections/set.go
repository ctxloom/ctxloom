// Package collections provides generic data structure utilities.
//
// It provides a lightweight generic Set type that wraps map[T]struct{}, for
// cleaner syntax at the "seen"/"visited" idiom's call sites.
package collections

// Set is a generic set implementation backed by a map.
//
// The zero value is a nil map, and Set inherits Go's map semantics verbatim:
// every READ works on it — Has reports false, Items returns an empty slice,
// Clone returns an empty writable set, len is 0 — while every WRITE PANICS
// ("assignment to entry in nil map"). Reading a zero Set is a supported idiom
// and is used in this tree (a result set a switch may leave unassigned, then
// only queried with Has). Anything that may write must be constructed with
// NewSet or NewSetFrom.
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
