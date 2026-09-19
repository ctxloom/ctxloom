package claude

// argPair reports whether args contains flag immediately followed by value.
//
// It reads an argv that has ALREADY been built, which is the point: a decision
// that depends on what claude was actually told is only answerable from the
// argv itself. Asking a request flag that separately implies the same thing
// creates a second decision site free to disagree with the first.
func argPair(args []string, flag, value string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == value {
			return true
		}
	}
	return false
}
