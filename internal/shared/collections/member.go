package collections

// Member resolves name against a closed vocabulary's members by their string
// spelling: the member and true on a match, the zero value and false on a
// miss. It is the one membership test under a vocabulary's Parse: a
// consumer that re-spells the comparison cannot follow the vocabulary when a
// member is added or renamed.
func Member[T ~string](members []T, name string) (T, bool) {
	for _, m := range members {
		if string(m) == name {
			return m, true
		}
	}
	var zero T
	return zero, false
}
