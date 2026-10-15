package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

func TestParseScope(t *testing.T) {
	cases := []struct {
		in       string
		want     scope
		wantSkip bool
		wantErr  bool
	}{
		{in: "tree", want: scope{tree: true}},
		{in: "diff:origin/main", want: scope{base: "origin/main"}},
		// Nothing to diff against: a first push, or the all-zeroes SHA GitHub
		// sends for a branch creation. Skipped, never red.
		{in: "diff:", want: scope{}, wantSkip: true},
		{in: "diff:0000000000000000000000000000000000000000", want: scope{base: "0000000000000000000000000000000000000000"}, wantSkip: true},
		// A bare ref is refused rather than guessed at: "all" or "tree" as a
		// branch name must not silently change what gets mutated.
		{in: "origin/main", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, c := range cases {
		got, err := parseScope(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseScope(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if got != c.want {
			t.Errorf("parseScope(%q) = %+v, want %+v", c.in, got, c.want)
		}
		if (got.skipReason() != "") != c.wantSkip {
			t.Errorf("parseScope(%q).skipReason() = %q, wantSkip %v", c.in, got.skipReason(), c.wantSkip)
		}
	}
}

// The assignment is a DISJOINT COVER: every candidate lands in exactly one
// shard, whatever the shard count, and the same input always gives the same
// answer — shards and the aggregate each compute it independently, so any
// nondeterminism would read as a stale plan.
func TestAssign_IsADeterministicDisjointCover(t *testing.T) {
	var cands []candidate
	for i := 0; i < 57; i++ {
		cands = append(cands, candidate{path: fmt.Sprintf("p%02d", i), cost: (i * 37) % 23})
	}
	for _, n := range []int{1, 2, 3, 8, 57, 80} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			got := assign(cands, n)
			if len(got) != n {
				t.Fatalf("n=%d: %d shards", n, len(got))
			}
			checkDisjointCover(t, got, len(cands))
			// Shuffled input, same plan: the order candidates are discovered in
			// (go list's, git's) must not leak into the assignment.
			if again := assign(reversed(cands), n); !reflect.DeepEqual(again, got) {
				t.Errorf("n=%d: assignment depends on input order", n)
			}
		})
	}
}

// checkDisjointCover asserts every shard is sorted and the shards together
// hold each of want candidates exactly once.
func checkDisjointCover(t *testing.T, shards [][]string, want int) {
	t.Helper()
	seen := map[string]int{}
	for k, pkgs := range shards {
		if !sort.StringsAreSorted(pkgs) {
			t.Errorf("shard %d not sorted: %v", k, pkgs)
		}
		for _, p := range pkgs {
			if prev, dup := seen[p]; dup {
				t.Errorf("%s in shard %d and %d", p, prev, k)
			}
			seen[p] = k
		}
	}
	if len(seen) != want {
		t.Errorf("covered %d of %d candidates", len(seen), want)
	}
}

func reversed(cands []candidate) []candidate {
	rev := append([]candidate(nil), cands...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

// Longest-first onto the least-loaded shard: the heaviest shard is never more
// than the lightest plus the largest single candidate.
func TestAssign_BalancesByCost(t *testing.T) {
	cands := []candidate{{"a", 100}, {"b", 60}, {"c", 50}, {"d", 40}, {"e", 30}, {"f", 20}}
	cost := map[string]int{}
	for _, c := range cands {
		cost[c.path] = c.cost
	}
	got := assign(cands, 2)
	loads := make([]int, len(got))
	for k, pkgs := range got {
		for _, p := range pkgs {
			loads[k] += cost[p]
		}
	}
	// Greedy, not optimal (150/150 exists): 100+40+20 against 60+50+30.
	if loads[0] != 160 || loads[1] != 140 {
		t.Errorf("loads = %v, want [160 140] (shards: %v)", loads, got)
	}
}

const testGomutantsYAML = `exclude-files:
  - "\\.pb\\.go$"
  - "(^|/)mock_[^/]*\\.go$"
workers: 4
`

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	taskstest.Git(t, dir, taskstest.GitIdentity("t", "t@t"), args...)
}

// testModule is a module at a fresh root holding every shape the planner must
// tell apart, under testGomutantsYAML.
func testModule(t *testing.T) (string, *gomutantsConfig) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module lab\n\ngo 1.24\n")
	writeFile(t, root, gomutantsConfigFile, testGomutantsYAML)
	writeFile(t, root, "root.go", "package lab\n\nvar r = 1\n")
	writeFile(t, root, "a/a.go", "package a\n\nvar x = 1\n")
	writeFile(t, root, "a/two.go", "package a\n\nvar t = 2\n")
	writeFile(t, root, "a/a_test.go", "package a\n")
	// A package of nothing but tests: go list yields it with no GoFiles.
	writeFile(t, root, "only/only_test.go", "package only\n")
	writeFile(t, root, "a/api.pb.go", "package a\n")
	writeFile(t, root, "a/mock_store.go", "package a\n")
	writeFile(t, root, "b/b.go", "package b\n")
	// Invisible to go list: build-tagged off, testdata, a nested module, and
	// a package holding nothing but generated code.
	writeFile(t, root, "tagged/t.go", "//go:build acceptance\n\npackage tagged\n")
	writeFile(t, root, "a/testdata/d.go", "package d\n")
	writeFile(t, root, "nested/go.mod", "module nested\n\ngo 1.24\n")
	writeFile(t, root, "nested/n.go", "package nested\n")
	writeFile(t, root, "gen/gen.pb.go", "package gen\n")
	cfg, err := loadGomutantsConfig(filepath.Join(root, gomutantsConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	return root, cfg
}

// The planner's packages are what gomutants resolves for ./... — go list's
// view, so a build-tagged file, testdata and a nested module are never planned
// (gremlins' raw filesystem walk planned all three) — narrowed by
// .gomutants.yml's exclude-files, read from the file, never restated. A
// package left with no mutable file is no candidate at all.
func TestPackageFiles_AreWhatGomutantsResolves(t *testing.T) {
	root, cfg := testModule(t)
	got, err := packageFiles(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{".": {"root.go"}, "a": {"a/a.go", "a/two.go"}, "b": {"b/b.go"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("packages = %v, want %v", got, want)
	}

	tree, err := treeCandidates(root, got)
	if err != nil {
		t.Fatal(err)
	}
	if want := []candidate{{".", 3}, {"a", 6}, {"b", 1}}; !reflect.DeepEqual(sorted(tree), want) {
		t.Errorf("tree candidates = %+v, want %+v (cost: the lines of the package's mutable files)", tree, want)
	}

	// The config's build tags reach go list, as gomutants passes them on.
	cfg.tags = "acceptance"
	got, err = packageFiles(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["tagged"], []string{"tagged/t.go"}) {
		t.Errorf("with tags %q, packages = %v, want tagged/t.go planned", cfg.tags, got)
	}

	// A module go list refuses is an error, never an empty plan.
	writeFile(t, root, "broken/broken.go", "package broken\n\nimport \"no/such/module\"\n")
	if _, err := packageFiles(root, cfg); err == nil || !strings.Contains(err.Error(), "no/such/module") {
		t.Errorf("a module go list cannot load: err = %v, want go list's complaint", err)
	}
}

// sorted is cands in path order: the candidate functions promise none.
func sorted(cands []candidate) []candidate {
	out := append([]candidate(nil), cands...)
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// A whole-scope plan holds every package with mutable Go, and nothing else.
func TestMakePlan_TheWholeTreeIsEveryMutablePackage(t *testing.T) {
	root, cfg := testModule(t)
	p, err := makePlan(root, scope{tree: true}, 2, cfg)
	if err != nil || p.skip != "" {
		t.Fatalf("plan = %+v, err %v", p, err)
	}
	var planned []string
	for _, s := range p.shards {
		planned = append(planned, s...)
	}
	sort.Strings(planned)
	if want := []string{".", "a", "b"}; !reflect.DeepEqual(planned, want) {
		t.Errorf("planned = %q, want %q", planned, want)
	}
}

// The diff candidates are the packages gomutants --changed-since would
// mutate: its changeset (`git diff --merge-base`, the working tree's tracked
// edits included), narrowed to listed packages' mutable files, costed by the
// added lines. Deleted files are not candidates.
func TestDiffCandidates_AreWhatGomutantsWouldMutate(t *testing.T) {
	root, cfg := testModule(t)
	writeFile(t, root, "b/gone.go", "package b\n")
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "base")
	git(t, root, "checkout", "-q", "-b", "feature")
	writeFile(t, root, "a/a.go", "package a\n\nvar x = 1\nvar y = 2\nvar z = 3\n")
	writeFile(t, root, "a/two.go", "package a\n\nvar t = 2\nvar u = 3\n")
	writeFile(t, root, "a/a_test.go", "package a\n\n// more\n")
	writeFile(t, root, "a/api.pb.go", "package a\n\nvar p = 1\n")
	writeFile(t, root, "tagged/t.go", "//go:build acceptance\n\npackage tagged\n\nvar q = 1\n")
	if err := os.Remove(filepath.Join(root, "b", "gone.go")); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "change")
	// Uncommitted, as gomutants' merge-base diff sees it.
	writeFile(t, root, "b/b.go", "package b\n\nvar w = 1\n")

	pkgs, err := packageFiles(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := diffCandidates(root, "main", pkgs)
	if err != nil {
		t.Fatalf("diffCandidates: %v", err)
	}
	// a: two lines added to a.go and one to two.go — a package's cost sums its files.
	if want := []candidate{{"a", 3}, {"b", 2}}; !reflect.DeepEqual(sorted(got), want) {
		t.Errorf("candidates = %+v, want %+v", got, want)
	}

	if _, err := diffCandidates(root, "no-such-ref", pkgs); err == nil {
		t.Errorf("an unresolvable base must be an error, not an empty (skipped) plan")
	}
	if _, err := makePlan(root, scope{base: "no-such-ref"}, 1, cfg); err == nil {
		t.Errorf("makePlan over an unresolvable base must fail, not skip")
	}
}

func TestMakePlan_SkipsWithAReasonAndNeverAsAnError(t *testing.T) {
	root, cfg := testModule(t)
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "base")

	p, err := makePlan(root, scope{}, 3, cfg)
	if err != nil || !strings.Contains(p.skip, "no base") || len(p.shards) != 3 {
		t.Errorf("no base: plan=%+v err=%v", p, err)
	}
	// A diff with no mutable Go (here: a test-only change) is a skip, not a
	// run: there is nothing for gomutants to mutate.
	writeFile(t, root, "a/a_test.go", "package a\n\n// more\n")
	p, err = makePlan(root, scope{base: "main"}, 3, cfg)
	if err != nil || !strings.Contains(p.skip, "no mutable Go") {
		t.Errorf("test-only diff: plan=%+v err=%v", p, err)
	}
	if _, err := makePlan(root, scope{tree: true}, 0, cfg); !errors.Is(err, errShardCount) {
		t.Errorf("zero shards: err = %v, want errShardCount", err)
	}
}

// A package narrows either scope to the packages under it, and is refused when
// it names no directory: a typo would otherwise plan nothing and skip green.
// Its spellings normalise to the module-relative directory the plan matches,
// and it is part of the scope, so the stamp a package run's reports carry is
// not a whole-scope run's.
func TestScopeWithPkg(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "internal/x/x.go", "package x\n")
	for _, base := range []scope{{base: "main"}, {tree: true}} {
		for _, in := range []string{"internal/x", "./internal/x/", "internal/x/..."} {
			got, err := base.withPkg(root, in)
			if err != nil || got.pkg != "internal/x" {
				t.Errorf("%s withPkg(%q) = %+v, %v; want pkg internal/x", base, in, got, err)
			}
			if got.String() == base.String() {
				t.Errorf("%s withPkg(%q).String() = %q: a package run must not stamp as the whole scope", base, in, got.String())
			}
		}
		for _, in := range []string{"", ".", "./"} {
			if got, err := base.withPkg(root, in); err != nil || got != base {
				t.Errorf("%s withPkg(%q) = %+v, %v; want the whole scope", base, in, got, err)
			}
		}
		if _, err := base.withPkg(root, "internal/nope"); !errors.Is(err, errPkgNotADir) {
			t.Errorf("%s missing package: err = %v, want errPkgNotADir", base, err)
		}
	}
}

// A package scope plans the packages under it, the package itself included,
// and nothing else — a sibling sharing its name as a prefix is outside it.
func TestMakePlan_PkgNarrowsToThePackagesUnderIt(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module lab\n\ngo 1.24\n")
	writeFile(t, root, gomutantsConfigFile, testGomutantsYAML)
	dirs := []string{"sub", "sub/deep", "subway", "other"}
	for _, d := range dirs {
		writeFile(t, root, d+"/p.go", "package p\n")
	}
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "base")
	for _, d := range dirs {
		writeFile(t, root, d+"/p.go", "package p\n\nvar v = 1 + 2\n")
	}
	cfg, err := loadGomutantsConfig(filepath.Join(root, gomutantsConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []scope{{base: "main"}, {tree: true}} {
		sc, err := base.withPkg(root, "sub")
		if err != nil {
			t.Fatal(err)
		}
		p, err := makePlan(root, sc, 2, cfg)
		if err != nil || p.skip != "" {
			t.Fatalf("%s: plan = %+v, err %v", sc, p, err)
		}
		var planned []string
		for _, s := range p.shards {
			planned = append(planned, s...)
		}
		sort.Strings(planned)
		if want := []string{"sub", "sub/deep"}; !reflect.DeepEqual(planned, want) {
			t.Errorf("%s: planned = %q, want %q", sc, planned, want)
		}
	}

	untouched, err := scope{base: "main"}.withPkg(root, "other")
	if err != nil {
		t.Fatal(err)
	}
	git(t, root, "checkout", "-q", "--", "other")
	if p, err := makePlan(root, untouched, 1, cfg); err != nil || !strings.Contains(p.skip, "other") {
		t.Errorf("a package the diff does not touch must skip naming it: plan=%+v err=%v", p, err)
	}
}

// The scope's name is the stamp every shard report carries and the aggregate
// compares, so each shape must name itself distinctly.
func TestScopeString(t *testing.T) {
	for sc, want := range map[scope]string{
		{tree: true}:               "tree",
		{tree: true, pkg: "a/b"}:   "tree under a/b/",
		{base: "main"}:             "diff:main",
		{base: "main", pkg: "a/b"}: "diff:main under a/b/",
	} {
		if got := sc.String(); got != want {
			t.Errorf("%+v.String() = %q, want %q", sc, got, want)
		}
	}
}

// gomutants takes package patterns; the module's root package is ".", every
// other one is "./dir" — a bare "dir" would be read as an import path.
func TestPatterns(t *testing.T) {
	if got, want := patterns([]string{".", "a", "a/b"}), []string{".", "./a", "./a/b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("patterns = %q, want %q", got, want)
	}
}

// Every command reads -scope and -pkg the same way, against the module in the
// working directory, so a package run's shards and its aggregate plan alike —
// and a bad pair is refused by both, never planned.
func TestScopeFlags_ShardsAndAggregateAgree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "p/p.go", "package p\n")
	t.Chdir(root)
	good := []string{"-scope", "diff:main", "-pkg", "./p/"}
	sf, err := parseShardFlags(flag.NewFlagSet("run", flag.ContinueOnError), good, nil)
	if err != nil || sf.scope != (scope{base: "main", pkg: "p"}) {
		t.Fatalf("shard flags = %+v, %v", sf, err)
	}
	af, err := parseAggregateFlags(good)
	if err != nil || af.scope != sf.scope {
		t.Fatalf("aggregate scope = %+v, %v; the shards planned %+v", af.scope, err, sf.scope)
	}
	for _, bad := range [][]string{
		{"-scope", "diff:main", "-pkg", "no-such-dir"},
		{"-scope", "main"},
	} {
		if _, err := parseShardFlags(flag.NewFlagSet("run", flag.ContinueOnError), bad, nil); err == nil {
			t.Errorf("shard flags %q: accepted", bad)
		}
		if _, err := parseAggregateFlags(bad); err == nil {
			t.Errorf("aggregate flags %q: accepted", bad)
		}
	}
}

// The ratchet judges a WHOLE package: a diff is a different mutant set every
// time, and the whole tree has no row. -update without -ratchet would record
// nothing while looking like it had.
func TestAggregateFlags_RatchetNeedsAWholePackage(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "p/p.go", "package p\n")
	t.Chdir(root)
	af, err := parseAggregateFlags([]string{"-scope", "tree", "-pkg", "p", "-reports", "r", "-ratchet", "b.txt", "-update", "-efficacy", "80", "-mcover", "5"})
	if err != nil || af.reports != "r" || af.ratchet != "b.txt" || !af.update || af.th != (thresholds{80, 5}) {
		t.Fatalf("aggregate flags = %+v, %v", af, err)
	}
	// No threshold flag judges no threshold: a zero threshold is never judged.
	if af, err := parseAggregateFlags([]string{"-scope", "tree"}); err != nil || af.th != (thresholds{}) {
		t.Errorf("default thresholds = %+v, %v; want none", af.th, err)
	}
	for _, bad := range [][]string{
		{"-scope", "tree", "-ratchet", "b.txt"},
		{"-scope", "diff:main", "-pkg", "p", "-ratchet", "b.txt"},
	} {
		if _, err := parseAggregateFlags(bad); !errors.Is(err, errRatchetScope) {
			t.Errorf("%q: err = %v, want errRatchetScope", bad, err)
		}
	}
	if _, err := parseAggregateFlags([]string{"-scope", "tree", "-pkg", "p", "-update"}); err == nil {
		t.Errorf("-update without -ratchet: accepted")
	}
}
