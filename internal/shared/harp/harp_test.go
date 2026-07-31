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

// TestGenerateShortName_DrawsFromLongGroup pins U111-F03: the short (2-
// component) name task harp minting draws from used to draw from
// DefaultGroup — 443 adjectives × 1102 nouns ≈ 488k combinations, small
// enough that two branches minting independently collide at percent-level
// rates (0.5% at 50+50 tasks, per the unit review's own math), and the
// verified downstream consequence of a harp collision is a task store
// where every read fails loud. It must draw from the wider "long" group
// instead (1269×4396 ≈ 5.58M, an 11x improvement at the same word count).
func TestGenerateShortName_DrawsFromLongGroup(t *testing.T) {
	long := groups["long"]
	for range 50 {
		name := GenerateShortName()
		parts := strings.Split(name, "-")
		if len(parts) != 2 {
			t.Fatalf("GenerateShortName() = %q, want exactly 2 components", name)
		}
		if !inList(long.adjectives, parts[0]) {
			t.Errorf("GenerateShortName() adjective %q is not in the long group's adjective list", parts[0])
		}
		if !inList(long.nouns, parts[1]) {
			t.Errorf("GenerateShortName() noun %q is not in the long group's noun list", parts[1])
		}
	}
}

// TestUniqueFrom_ExhaustionReturnsError pins U111-F02: UniqueFrom's doc
// used to say it "returns one final unredeemed gen()" on exhaustion — a
// best-effort fallback that COULD be a duplicate, and neither of its two
// (former) callers checked the result against used as the doc told them
// to. The contract is now honest: exhaustion is a returned error, not a
// silently-possibly-colliding name.
func TestUniqueFrom_ExhaustionReturnsError(t *testing.T) {
	used := map[string]struct{}{"only-option": {}}
	gen := func() string { return "only-option" } // never NOT a duplicate

	got, err := UniqueFrom(used, gen)
	if err == nil {
		t.Fatalf("UniqueFrom with an unwinnable gen = (%q, nil), want a non-nil error", got)
	}
	if got != "" {
		t.Errorf("UniqueFrom on exhaustion returned %q, want empty string alongside the error", got)
	}
}

// TestUniqueFrom_SucceedsOnFirstNonDuplicate is the positive case: a gen
// that eventually produces something not in used must return it with no
// error, exactly as before this change.
func TestUniqueFrom_SucceedsOnFirstNonDuplicate(t *testing.T) {
	used := map[string]struct{}{"taken": {}}
	calls := 0
	gen := func() string {
		calls++
		if calls == 1 {
			return "taken"
		}
		return "free"
	}
	got, err := UniqueFrom(used, gen)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "free" {
		t.Errorf("UniqueFrom = %q, want %q", got, "free")
	}
}

func inList(list []string, s string) bool {
	return slices.Contains(list, s)
}

// TestValidate_RejectsNamesThatAreNotOnePathComponent pins U087-F04's
// mechanism: a harp name becomes a filesystem path component (paths.HarpDir),
// so anything that can escape that component — a separator, "..", a control
// character — must be refused at the validator, not discovered at MkdirAll.
func TestValidate_RejectsNamesThatAreNotOnePathComponent(t *testing.T) {
	bad := []string{
		"",
		".",
		"..",
		"../..",
		"../../etc/passwd",
		"a/b",
		`a\b`,
		"C:evil",
		"has\x00nul",
		"has\nnewline",
		"has\ttab",
		" leading",
		"trailing ",
	}
	for _, name := range bad {
		if err := Validate(name); err == nil {
			t.Errorf("Validate(%q) = nil, want error", name)
		}
	}
}

// TestValidate_AcceptsRealAndRenamedHarps guards the other direction: the
// validator must not break generated harps or the memorable names humans
// rename them to, which is why it is permissive on charset.
func TestValidate_AcceptsRealAndRenamedHarps(t *testing.T) {
	good := []string{
		GenerateName(),
		GenerateShortName(),
		"swift-amber-falcon",
		"my_session 2",
		"Fix Login Bug",
		"réunion-café",
		"a.b.c",
		"..hidden-but-not-dotdot",
	}
	for _, name := range good {
		if err := Validate(name); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", name, err)
		}
	}
}

// TestGroups_ReturnsSortedNamesOnEveryCall pins Groups' order as a contract.
// The registry is a Go map, and map iteration order is randomized per
// process, so before the sort the same binary printed its --group help text
// (and a rejected --group's "have: ..." list) in a different order on every
// run. A single call can be sorted by luck with only a handful of groups, so
// the assertion is repeated until an unsorted implementation fails with
// certainty rather than probability.
func TestGroups_ReturnsSortedNamesOnEveryCall(t *testing.T) {
	want := make([]string, 0, len(groups))
	for name := range groups {
		want = append(want, name)
	}
	slices.Sort(want)

	// Below two groups no ordering assertion can ever fail, so the rest of
	// this test would be vacuous.
	if len(want) < 2 {
		t.Fatalf("this test needs at least two word-list groups to mean anything, have %d", len(want))
	}

	for range 200 {
		if got := Groups(); !slices.Equal(got, want) {
			t.Fatalf("Groups() must be sorted and stable on every call: got %v, want %v", got, want)
		}
	}
}

// TestGenerateName_UnsatisfiableMaxElementLengthStaysRandom pins the
// degradation for a cap no word in the group can meet. pickWord used to retry
// 1000 times and then return words[0], which makes every generated name the
// SAME CONSTANT — `harp --max-len 2 -n 5` printed one name five times — and
// takes UniqueFrom with it: an allocator that generates until it finds an
// unused id cannot possibly succeed when its generator has exactly one
// output.
//
// The shortest word in every shipped list is 3 characters, so maxLen 2 is
// unsatisfiable by construction; the loop below asserts that from the code
// under test's own vantage point rather than trusting the word lists to stay
// as they are.
func TestGenerateName_UnsatisfiableMaxElementLengthStaysRandom(t *testing.T) {
	const maxLen = 2
	g := groups[DefaultGroup]
	for _, w := range append(append([]string{}, g.adjectives...), g.nouns...) {
		if len(w) <= maxLen {
			t.Fatalf("fixture is not hostile: %q already satisfies maxLen=%d, so nothing here tests the unsatisfiable path", w, maxLen)
		}
	}

	gen := func() string { return GenerateNameWithOptions(Options{MaxElementLength: maxLen}) }

	seen := map[string]bool{}
	for range 30 {
		seen[gen()] = true
	}
	if len(seen) < 2 {
		t.Fatalf("an unsatisfiable cap must degrade to a random name, not a constant: 30 draws produced %d distinct name(s) %v", len(seen), seen)
	}

	// The downstream consequence the library's own UniqueFrom doc depends on:
	// a generator that can only emit one value makes allocation impossible.
	used := map[string]struct{}{gen(): {}}
	id, err := UniqueFrom(used, gen)
	if err != nil {
		t.Fatalf("UniqueFrom must still be able to allocate under an unsatisfiable cap: %v", err)
	}
	if id == "" {
		t.Fatal("UniqueFrom returned an empty id with no error")
	}
}

// TestRandIndex_RefusesAnEmptyRange pins the caller-bug path. [0, n) is empty
// for n < 1, so there is no value randIndex can honestly return; it used to
// return 0, which is a perfectly plausible index and which the only caller
// then used to index the very slice whose emptiness produced it. The failure
// surfaced as "index out of range [0] with length 0" a frame away from the
// real fault, naming neither the group nor the cause.
func TestRandIndex_RefusesAnEmptyRange(t *testing.T) {
	for _, n := range []int{0, -1} {
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("randIndex(%d) must refuse an empty range, not return a plausible index", n)
				}
				if msg, ok := r.(string); !ok || !strings.Contains(msg, "randIndex") {
					t.Errorf("the panic must name what was called and why, got %v", r)
				}
			}()
			_ = randIndex(n)
		}()
	}

	// A single-element range is the smallest LEGAL one and must still work,
	// or the guard is off by one and every two-word group breaks.
	if got := randIndex(1); got != 0 {
		t.Errorf("randIndex(1) must be 0, got %d", got)
	}
}
