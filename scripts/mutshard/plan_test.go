package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
		cands = append(cands, candidate{path: fmt.Sprintf("p%02d/f.go", i), cost: (i * 37) % 23})
	}
	for _, n := range []int{1, 2, 3, 8, 57, 80} {
		got := assign(cands, n)
		if len(got) != n {
			t.Fatalf("n=%d: %d shards", n, len(got))
		}
		seen := map[string]int{}
		for k, files := range got {
			if !sort.StringsAreSorted(files) {
				t.Errorf("n=%d shard %d not sorted: %v", n, k, files)
			}
			for _, f := range files {
				if prev, dup := seen[f]; dup {
					t.Errorf("n=%d: %s in shard %d and %d", n, f, prev, k)
				}
				seen[f] = k
			}
		}
		if len(seen) != len(cands) {
			t.Errorf("n=%d: covered %d of %d candidates", n, len(seen), len(cands))
		}
		// Shuffled input, same plan: the order candidates are discovered in
		// (git's, the filesystem's) must not leak into the assignment.
		rev := append([]candidate(nil), cands...)
		for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
			rev[i], rev[j] = rev[j], rev[i]
		}
		if again := assign(rev, n); !reflect.DeepEqual(again, got) {
			t.Errorf("n=%d: assignment depends on input order", n)
		}
	}
}

// Longest-first onto the least-loaded shard: the heaviest shard is never more
// than the lightest plus the largest single candidate.
func TestAssign_BalancesByCost(t *testing.T) {
	cands := []candidate{{"a.go", 100}, {"b.go", 60}, {"c.go", 50}, {"d.go", 40}, {"e.go", 30}, {"f.go", 20}}
	cost := map[string]int{}
	for _, c := range cands {
		cost[c.path] = c.cost
	}
	got := assign(cands, 2)
	loads := make([]int, len(got))
	for k, files := range got {
		for _, f := range files {
			loads[k] += cost[f]
		}
	}
	// Greedy, not optimal (150/150 exists): 100+40+20 against 60+50+30.
	if loads[0] != 160 || loads[1] != 140 || loads[0]-loads[1] > 100 {
		t.Errorf("loads = %v, want [160 140] (shards: %v)", loads, got)
	}
}

const testGremlinsYAML = `silent: false
unleash:
  exclude-files:
    - ".*_test\\.go$"
    - ".*\\.pb\\.go$"
    - "tests/acceptance/.*"
  workers: 4
  timeout-coefficient: 30
  threshold:
    efficacy: 80
    mutant-coverage: 0
mutants:
  arithmetic-base:
    enabled: true
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

func loadTestConfig(t *testing.T) *gremlinsConfig {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, ".gremlins.yaml", testGremlinsYAML)
	cfg, err := loadGremlinsConfig(filepath.Join(dir, ".gremlins.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

// THE TRAP this guards: gremlins reads exclude-files as ONE setting, so a
// shard that narrowed its run with `--exclude-files` on the command line would
// REPLACE the project's list, not extend it — and start mutating generated
// protobuf, the acceptance suite and test files. The shard's config must carry
// every project exclusion plus its own, keep every other setting (the timeout
// coefficient decides TIMED OUT verdicts), and hand the pass/fail thresholds to
// the aggregate, which is the only place the union exists.
func TestShardConfig_ExtendsTheProjectExclusionsAndDefersTheThresholds(t *testing.T) {
	cfg := loadTestConfig(t)
	if cfg.efficacy != 80 || cfg.mutantCoverage != 0 {
		t.Fatalf("thresholds = %v/%v, want 80/0", cfg.efficacy, cfg.mutantCoverage)
	}
	out, err := cfg.shardConfig([]string{"internal/b/b.go", "internal/a/a+.go"})
	if err != nil {
		t.Fatalf("shardConfig: %v", err)
	}
	var doc struct {
		Silent  bool `yaml:"silent"`
		Unleash struct {
			ExcludeFiles       []string `yaml:"exclude-files"`
			Workers            int      `yaml:"workers"`
			TimeoutCoefficient int      `yaml:"timeout-coefficient"`
			Threshold          struct {
				Efficacy       float64 `yaml:"efficacy"`
				MutantCoverage float64 `yaml:"mutant-coverage"`
			} `yaml:"threshold"`
		} `yaml:"unleash"`
		Mutants map[string]map[string]bool `yaml:"mutants"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("shard config is not YAML: %v\n%s", err, out)
	}
	ex := doc.Unleash.ExcludeFiles
	if len(ex) != 4 || ex[0] != `.*_test\.go$` || ex[1] != `.*\.pb\.go$` || ex[2] != "tests/acceptance/.*" {
		t.Fatalf("exclude-files = %q: the project's list must survive intact, with the shard's rule appended", ex)
	}
	if doc.Unleash.Workers != 4 || doc.Unleash.TimeoutCoefficient != 30 || !doc.Mutants["arithmetic-base"]["enabled"] {
		t.Errorf("settings other than exclusions and thresholds changed: %+v", doc)
	}
	if doc.Unleash.Threshold.Efficacy != 0 || doc.Unleash.Threshold.MutantCoverage != 0 {
		t.Errorf("a shard must not judge its slice alone: thresholds = %+v", doc.Unleash.Threshold)
	}
	shardRule := regexpMust(t, ex[3])
	for _, p := range []string{"internal/b/b.go", "internal/a/a+.go"} {
		if !shardRule.MatchString(p) {
			t.Errorf("shard rule %q does not exclude %s", ex[3], p)
		}
	}
	for _, p := range []string{"internal/b/b.go.orig", "xinternal/b/b.go", "internal/a/aa.go", "internal/c/c.go"} {
		if shardRule.MatchString(p) {
			t.Errorf("shard rule %q excludes %s, which is not another shard's file", ex[3], p)
		}
	}

	// One shard: nothing belongs to anyone else, so no rule is added.
	solo, err := cfg.shardConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(solo, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Unleash.ExcludeFiles) != 3 {
		t.Errorf("single shard exclude-files = %q", doc.Unleash.ExcludeFiles)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// The diff candidates are exactly what gremlins would mutate for that base:
// its changeset (`git diff --merge-base`, working tree included), narrowed by
// its own file rule and the project's exclusions — read from the config, never
// restated. Deleted files are not candidates.
func TestDiffCandidates_AreWhatGremlinsWouldMutate(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-q", "-b", "main")
	writeFile(t, root, ".gremlins.yaml", testGremlinsYAML)
	writeFile(t, root, "keep.go", "package x\n")
	writeFile(t, root, "gone.go", "package x\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "base")
	git(t, root, "checkout", "-q", "-b", "feature")
	writeFile(t, root, "keep.go", "package x\n\nvar a = 1\nvar b = 2\n")
	writeFile(t, root, "internal/new.go", "package y\n\nvar c = 3\n")
	writeFile(t, root, "internal/new_test.go", "package y\n")
	writeFile(t, root, "api/x.pb.go", "package api\n")
	writeFile(t, root, "tests/acceptance/steps.go", "package acceptance\n")
	writeFile(t, root, "notes.md", "prose\n")
	if err := os.Remove(filepath.Join(root, "gone.go")); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "change")
	// Uncommitted, as gremlins' `git diff --merge-base` sees it.
	writeFile(t, root, "dirty.go", "package x\n")
	git(t, root, "add", "dirty.go")

	cfg, err := loadGremlinsConfig(filepath.Join(root, ".gremlins.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := diffCandidates(root, "main", cfg)
	if err != nil {
		t.Fatalf("diffCandidates: %v", err)
	}
	want := []candidate{{"dirty.go", 1}, {"internal/new.go", 3}, {"keep.go", 3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("candidates = %+v, want %+v", got, want)
	}

	if _, err := diffCandidates(root, "no-such-ref", cfg); err == nil {
		t.Errorf("an unresolvable base must be an error, not an empty (skipped) plan")
	}
}

func TestTreeCandidates_WalkTheFilesystemLikeGremlins(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\n\nvar x = 1\n")
	writeFile(t, root, "a_test.go", "package a\n")
	writeFile(t, root, "sub/b.go", "package b\n")
	writeFile(t, root, "sub/b.pb.go", "package b\n")
	writeFile(t, root, "tests/acceptance/c.go", "package c\n")
	writeFile(t, root, "README.md", "x\n")
	cfg := loadTestConfig(t)
	got, err := treeCandidates(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []candidate{{"a.go", 3}, {"sub/b.go", 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("candidates = %+v, want %+v", got, want)
	}
}

func TestMakePlan_SkipsWithAReasonAndNeverAsAnError(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-q", "-b", "main")
	writeFile(t, root, ".gremlins.yaml", testGremlinsYAML)
	writeFile(t, root, "a_test.go", "package a\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "base")
	cfg, err := loadGremlinsConfig(filepath.Join(root, ".gremlins.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	p, err := makePlan(root, scope{}, 3, cfg)
	if err != nil || !strings.Contains(p.skip, "no base") || len(p.shards) != 3 {
		t.Errorf("no base: plan=%+v err=%v", p, err)
	}
	// A diff with no mutable Go (here: a test-only change) is a skip, not a
	// run: gremlins scores an empty changeset 0%% and would fail the gate for
	// having nothing to test.
	writeFile(t, root, "a_test.go", "package a\n\n// more\n")
	p, err = makePlan(root, scope{base: "main"}, 3, cfg)
	if err != nil || !strings.Contains(p.skip, "no mutable Go") {
		t.Errorf("test-only diff: plan=%+v err=%v", p, err)
	}
	if _, err := makePlan(root, scope{tree: true}, 0, cfg); !errors.Is(err, errShardCount) {
		t.Errorf("zero shards: err = %v, want errShardCount", err)
	}
}

func regexpMust(t *testing.T, expr string) *regexp.Regexp {
	t.Helper()
	re, err := regexp.Compile(expr)
	if err != nil {
		t.Fatalf("compile %q: %v", expr, err)
	}
	return re
}
