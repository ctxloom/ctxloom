package harp

import (
	"slices"
	"strings"
	"testing"
)

// TestWordListCounts pins the embedded word list sizes. The "long" group
// tracks the harp-core release counts; if they drift, the lists were
// refreshed and the embed should be regenerated from the upstream repo.
// The "default" group is derived locally (long words ≤5 chars plus a
// curated expansion), so its counts pin the derivation, not upstream.
func TestWordListCounts(t *testing.T) {
	cases := []struct {
		group         string
		adjs, nounCnt int
	}{
		{"long", 1269, 4396},
		{"default", 443, 1102},
	}
	for _, c := range cases {
		g, ok := groups[c.group]
		if !ok {
			t.Errorf("group %q not loaded", c.group)
			continue
		}
		if got := len(g.adjectives); got != c.adjs {
			t.Errorf("%s adjectives: want %d entries, got %d", c.group, c.adjs, got)
		}
		if got := len(g.nouns); got != c.nounCnt {
			t.Errorf("%s nouns: want %d entries, got %d", c.group, c.nounCnt, got)
		}
	}
}

// TestDefaultGroupWordLength verifies every default-group word is ≤5 chars —
// the defining property of the group.
func TestDefaultGroupWordLength(t *testing.T) {
	g := groups[DefaultGroup]
	for _, w := range slices.Concat(g.adjectives, g.nouns) {
		if len(w) > 5 {
			t.Errorf("default group word %q exceeds 5 chars", w)
		}
	}
}

func TestGenerateName_Default(t *testing.T) {
	g := groups[DefaultGroup]
	name := GenerateName()
	parts := strings.Split(name, "-")
	if len(parts) != 3 {
		t.Fatalf("want 3 components, got %d: %q", len(parts), name)
	}
	for _, p := range parts[:2] {
		if !inList(g.adjectives, p) {
			t.Errorf("part %q not in adjectives list", p)
		}
	}
	if !inList(g.nouns, parts[2]) {
		t.Errorf("part %q not in nouns list", parts[2])
	}
}

func TestGenerateName_Components(t *testing.T) {
	g := groups[DefaultGroup]
	for _, n := range []int{2, 3, 4, 8, 16} {
		got := GenerateNameWithOptions(Options{Components: n})
		parts := strings.Split(got, "-")
		if len(parts) != n {
			t.Errorf("components=%d: want %d parts, got %d (%q)", n, n, len(parts), got)
		}
		// Last part is a noun; all prior parts are adjectives.
		if !inList(g.nouns, parts[n-1]) {
			t.Errorf("components=%d: last part %q not in nouns", n, parts[n-1])
		}
		for _, p := range parts[:n-1] {
			if !inList(g.adjectives, p) {
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

// TestGenerateName_Group draws from the long group and verifies the words
// come from that group's lists.
func TestGenerateName_Group(t *testing.T) {
	g := groups["long"]
	got := GenerateNameWithOptions(Options{Group: "long"})
	parts := strings.Split(got, "-")
	for _, p := range parts[:len(parts)-1] {
		if !inList(g.adjectives, p) {
			t.Errorf("part %q not in long adjectives", p)
		}
	}
	if !inList(g.nouns, parts[len(parts)-1]) {
		t.Errorf("part %q not in long nouns", parts[len(parts)-1])
	}
}

// TestGenerateName_UnknownGroupFallsBack ensures an unknown group degrades to
// the default group rather than panicking.
func TestGenerateName_UnknownGroupFallsBack(t *testing.T) {
	g := groups[DefaultGroup]
	got := GenerateNameWithOptions(Options{Group: "does-not-exist"})
	parts := strings.Split(got, "-")
	if !inList(g.nouns, parts[len(parts)-1]) {
		t.Errorf("unknown group should fall back to default; noun %q not in default", parts[len(parts)-1])
	}
}

func TestGroups(t *testing.T) {
	got := Groups()
	slices.Sort(got)
	want := []string{"default", "long"}
	if !slices.Equal(got, want) {
		t.Errorf("Groups() = %v, want %v", got, want)
	}
}

// TestGenerateName_CollisionRate is a sanity check on the RNG: 10k
// generations should produce >9990 unique results. The default name space
// is 443^2 * 1102 ≈ 216 million, so collisions in 10k samples are vanishingly
// rare (birthday-paradox expected collisions ≈ 0.2).
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
