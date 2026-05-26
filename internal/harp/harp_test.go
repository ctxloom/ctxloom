package harp

import (
	"slices"
	"strings"
	"testing"
)

// TestWordListCounts pins the embedded word list sizes to the
// harp-core release counts. If they drift, the lists were refreshed and
// the embed should be regenerated from the upstream repo.
func TestWordListCounts(t *testing.T) {
	if got, want := len(adjectives), 1269; got != want {
		t.Errorf("adjectives: want %d entries, got %d", want, got)
	}
	if got, want := len(nouns), 4396; got != want {
		t.Errorf("nouns: want %d entries, got %d", want, got)
	}
}

func TestGenerateName_Default(t *testing.T) {
	name := GenerateName()
	parts := strings.Split(name, "-")
	if len(parts) != 3 {
		t.Fatalf("want 3 components, got %d: %q", len(parts), name)
	}
	for _, p := range parts[:2] {
		if !inList(adjectives, p) {
			t.Errorf("part %q not in adjectives list", p)
		}
	}
	if !inList(nouns, parts[2]) {
		t.Errorf("part %q not in nouns list", parts[2])
	}
}

func TestGenerateName_Components(t *testing.T) {
	for _, n := range []int{2, 3, 4, 8, 16} {
		got := GenerateNameWithOptions(Options{Components: n})
		parts := strings.Split(got, "-")
		if len(parts) != n {
			t.Errorf("components=%d: want %d parts, got %d (%q)", n, n, len(parts), got)
		}
		// Last part is a noun; all prior parts are adjectives.
		if !inList(nouns, parts[n-1]) {
			t.Errorf("components=%d: last part %q not in nouns", n, parts[n-1])
		}
		for _, p := range parts[:n-1] {
			if !inList(adjectives, p) {
				t.Errorf("components=%d: part %q not in adjectives", n, p)
			}
		}
	}
}

func TestGenerateName_ComponentsClamped(t *testing.T) {
	// Below 2 clamps to default 3 (so 2 separators).
	if got := strings.Count(GenerateNameWithOptions(Options{Components: 1}), "-"); got != 2 {
		t.Errorf("Components=1 should clamp to 3 (2 separators), got %d", got)
	}
	// Above 16 clamps to 16 (so 15 separators).
	if got := strings.Count(GenerateNameWithOptions(Options{Components: 99}), "-"); got != 15 {
		t.Errorf("Components=99 should clamp to 16 (15 separators), got %d", got)
	}
}

func TestGenerateName_Separator(t *testing.T) {
	got := GenerateNameWithOptions(Options{Separator: "_"})
	if strings.Contains(got, "-") {
		t.Errorf("expected no dashes with separator=_, got %q", got)
	}
	if strings.Count(got, "_") != 2 {
		t.Errorf("expected 2 underscores, got %q", got)
	}
}

func TestGenerateName_MaxElementLength(t *testing.T) {
	got := GenerateNameWithOptions(Options{MaxElementLength: 5})
	for p := range strings.SplitSeq(got, "-") {
		if len(p) > 5 {
			t.Errorf("part %q exceeds max length 5", p)
		}
	}
}

// TestGenerateName_CollisionRate is a sanity check on the RNG: 10k
// generations should produce >9990 unique results. The default name space
// is 1269^2 * 4396 ≈ 7 billion, so collisions in 10k samples are vanishingly
// rare (birthday-paradox expected collisions < 1e-5).
func TestGenerateName_CollisionRate(t *testing.T) {
	const n = 10000
	seen := make(map[string]struct{}, n)
	for range n {
		seen[GenerateName()] = struct{}{}
	}
	if len(seen) < n-10 {
		t.Errorf("collision rate too high: %d unique in %d (want >%d)", len(seen), n, n-10)
	}
}

func inList(list []string, s string) bool {
	return slices.Contains(list, s)
}
