package tasks

import "testing"

// TestSemverComparatorCanonicalChain pins semverComparator.Compare against
// SemVer 2.0.0 §11's own worked precedence example: an increasing chain of
// pre-release/release forms of the same 1.0.0 core. Every adjacent pair must
// compare strictly less-than, and the comparator must agree in both
// directions (a<b and b>a).
func TestSemverComparatorCanonicalChain(t *testing.T) {
	chain := []string{
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-alpha.beta",
		"1.0.0-beta",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0",
	}
	cmp := semverComparator{}
	for i := 0; i < len(chain)-1; i++ {
		a, b := chain[i], chain[i+1]
		result, ok := cmp.Compare(a, b)
		if !ok {
			t.Fatalf("Compare(%q, %q): NotComparable, want ok", a, b)
		}
		if result != -1 {
			t.Errorf("Compare(%q, %q) = %d, want -1 (%q < %q per SemVer 2.0.0 §11)", a, b, result, a, b)
		}
		reverse, ok := cmp.Compare(b, a)
		if !ok {
			t.Fatalf("Compare(%q, %q): NotComparable, want ok", b, a)
		}
		if reverse != 1 {
			t.Errorf("Compare(%q, %q) = %d, want 1", b, a, reverse)
		}
	}
}

// TestSemverComparatorEqualVersionsCompareZero pins the reflexive case: a
// version compared against an identical string is Equal.
func TestSemverComparatorEqualVersionsCompareZero(t *testing.T) {
	result, ok := semverComparator{}.Compare("1.2.3", "1.2.3")
	if !ok {
		t.Fatal("Compare(1.2.3, 1.2.3): NotComparable, want ok")
	}
	if result != 0 {
		t.Errorf("Compare(1.2.3, 1.2.3) = %d, want 0", result)
	}
}

// TestSemverComparatorBuildMetadataEquality pins SemVer 2.0.0 §10: build
// metadata (a "+..." suffix) MUST be ignored when determining precedence, so
// two versions differing only in build metadata compare Equal — verified
// against the actual Masterminds/semver/v3 library behavior (this package's
// dependency), never assumed.
func TestSemverComparatorBuildMetadataEquality(t *testing.T) {
	result, ok := semverComparator{}.Compare("1.0.0+a", "1.0.0+b")
	if !ok {
		t.Fatal("Compare(1.0.0+a, 1.0.0+b): NotComparable, want ok")
	}
	if result != 0 {
		t.Errorf("Compare(1.0.0+a, 1.0.0+b) = %d, want 0 (build metadata excluded from precedence, SemVer 2.0.0 §10)", result)
	}
}

// TestSemverComparatorNotComparableOnGarbage pins tagma's own TypeComparator
// contract (SPEC.md §9): an operand that doesn't parse as a strict SemVer
// 2.0.0 version is NotComparable — (0, false), never an error and never a
// panic — regardless of which side is malformed.
func TestSemverComparatorNotComparableOnGarbage(t *testing.T) {
	cases := []struct {
		name string
		a, b string
	}{
		{"garbage vs valid", "not-a-version", "1.0.0"},
		{"valid vs garbage", "1.0.0", "not-a-version"},
		{"both garbage", "nope", "also-nope"},
		{"two-component (not strict semver)", "1.2", "1.0.0"},
		{"v-prefixed (not strict semver)", "v1.2.3", "1.0.0"},
		{"empty string", "", "1.0.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result, ok := semverComparator{}.Compare(c.a, c.b)
			if ok {
				t.Fatalf("Compare(%q, %q) = (%d, true), want NotComparable (0, false)", c.a, c.b, result)
			}
		})
	}
}
