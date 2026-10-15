package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// scope is what a mutation run covers: the whole tree, or the changeset
// against a base — all of it, or only the packages under pkg.
type scope struct {
	base string
	tree bool
	// pkg narrows the scope to one directory (module-relative,
	// slash-separated, its subdirectories included); empty is the whole scope.
	pkg string
}

const (
	scopeTree       = "tree"
	scopeDiffPrefix = "diff:"
	// The SHA GitHub sends as `before` when a push creates a branch.
	zeroSHA = "0000000000000000000000000000000000000000"
)

var errScope = errors.New(`scope must be "tree" or "diff:<base>"`)

func parseScope(s string) (scope, error) {
	switch {
	case s == scopeTree:
		return scope{tree: true}, nil
	case strings.HasPrefix(s, scopeDiffPrefix):
		return scope{base: strings.TrimPrefix(s, scopeDiffPrefix)}, nil
	}
	return scope{}, fmt.Errorf("%w, got %q", errScope, s)
}

func (s scope) String() string {
	name := scopeTree
	if !s.tree {
		name = scopeDiffPrefix + s.base
	}
	if s.pkg != "" {
		name += " under " + s.pkg + "/"
	}
	return name
}

var errPkgNotADir = errors.New("the package is not a directory of the module")

// withPkg is s narrowed to the package directory pkg, named as `go test`
// takes it ("./x", "x/", "x/..."), relative to the module at root.
func (s scope) withPkg(root, pkg string) (scope, error) {
	pkg = strings.TrimSuffix(filepath.ToSlash(pkg), "...")
	pkg = path.Clean(strings.TrimPrefix(pkg, "./"))
	if pkg == "." {
		return s, nil
	}
	if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(pkg))); err != nil || !fi.IsDir() {
		return s, fmt.Errorf("%w: %q", errPkgNotADir, pkg)
	}
	s.pkg = pkg
	return s, nil
}

// covers reports whether the module-relative package directory dir is in the
// scope's package.
func (s scope) covers(dir string) bool {
	return s.pkg == "" || dir == s.pkg || strings.HasPrefix(dir, s.pkg+"/")
}

// skipReason is non-empty when the scope cannot name any work at all.
func (s scope) skipReason() string {
	if !s.tree && (s.base == "" || s.base == zeroSHA) {
		return "no base commit to diff against — skipping mutation testing"
	}
	return ""
}

// candidate is a package gomutants would mutate (its module-relative
// directory), with an estimate of what it costs.
type candidate struct {
	path string
	cost int
}

// assign deals candidates onto n shards, longest first onto the least-loaded
// shard (lowest index on a tie). Ties between candidates break on path, so the
// result depends only on the SET of candidates — every shard and the aggregate
// compute it independently and must agree.
func assign(cands []candidate, n int) [][]string {
	sorted := append([]candidate(nil), cands...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].cost != sorted[j].cost {
			return sorted[i].cost > sorted[j].cost
		}
		return sorted[i].path < sorted[j].path
	})
	shards := make([][]string, n)
	loads := make([]int, n)
	for _, c := range sorted {
		k := 0
		for i := 1; i < n; i++ {
			if loads[i] < loads[k] {
				k = i
			}
		}
		shards[k] = append(shards[k], c.path)
		loads[k] += c.cost
	}
	for _, s := range shards {
		sort.Strings(s)
	}
	return shards
}

// gomutantsConfig is the part of the project's .gomutants.yml this tool must
// interpret the way gomutants does: which files it would mutate. gomutants
// itself reads the file; nothing here is restated to it.
type gomutantsConfig struct {
	excludes []*regexp.Regexp
	tags     string
}

func loadGomutantsConfig(file string) (*gomutantsConfig, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var typed struct {
		ExcludeFiles []string `yaml:"exclude-files"`
		Tags         string   `yaml:"tags"`
	}
	if err := yaml.Unmarshal(raw, &typed); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	cfg := &gomutantsConfig{tags: typed.Tags}
	for _, e := range typed.ExcludeFiles {
		re, err := regexp.Compile(e)
		if err != nil {
			return nil, fmt.Errorf("%s: exclude-files %q: %w", file, e, err)
		}
		cfg.excludes = append(cfg.excludes, re)
	}
	return cfg, nil
}

// mutable reports whether gomutants would mutate the module-relative file f, a
// production file of a listed package: no exclude-files regexp matches it
// (unanchored, against the module-relative path, as gomutants matches it).
func (c *gomutantsConfig) mutable(f string) bool {
	for _, re := range c.excludes {
		if re.MatchString(f) {
			return false
		}
	}
	return true
}

// packageFiles is every package of the module at root, by module-relative
// directory, with the module-relative paths of its mutable files. It is what
// gomutants resolves: `go list` under the config's build tags, so a
// tag-gated file, a nested module and a testdata tree are never planned.
func packageFiles(root string, cfg *gomutantsConfig) (map[string][]string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	args := []string{"list", "-f", "{{.Dir}}\t{{join .GoFiles \"\\t\"}}"}
	if cfg.tags != "" {
		args = append(args, "-tags", cfg.tags)
	}
	cmd := exec.Command("go", append(args, "./...")...)
	cmd.Dir = abs
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list ./...: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	pkgs := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Split(line, "\t")
		rel, err := filepath.Rel(abs, fields[0])
		if err != nil {
			return nil, err
		}
		dir := filepath.ToSlash(rel)
		for _, name := range fields[1:] {
			if f := path.Join(dir, name); name != "" && cfg.mutable(f) {
				pkgs[dir] = append(pkgs[dir], f)
			}
		}
	}
	return pkgs, nil
}

// treeCandidates is every package with a mutable file, costed by the line
// count of those files, in no particular order: assign's result does not
// depend on it. So for diffCandidates.
func treeCandidates(root string, pkgs map[string][]string) ([]candidate, error) {
	var cands []candidate
	for dir, files := range pkgs {
		cost := 0
		for _, f := range files {
			b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
			if err != nil {
				return nil, err
			}
			cost += bytes.Count(b, []byte("\n"))
		}
		cands = append(cands, candidate{path: dir, cost: cost})
	}
	return cands, nil
}

// diffCandidates is every package holding a mutable file of the changeset
// gomutants --changed-since would see — `git diff --merge-base <base>`, so the
// working tree's tracked edits count — costed by the lines added to them, since
// only changed lines are mutated.
func diffCandidates(root, base string, pkgs map[string][]string) ([]candidate, error) {
	cmd := exec.Command("git", "diff", "--merge-base", base, "--numstat", "-z", "--no-renames", "--diff-filter=d", "--", "*.go")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --merge-base %s: %w: %s", base, err, strings.TrimSpace(stderr.String()))
	}
	owner := map[string]string{}
	for dir, files := range pkgs {
		for _, f := range files {
			owner[f] = dir
		}
	}
	cost := map[string]int{}
	for _, rec := range strings.Split(string(out), "\x00") {
		fields := strings.SplitN(rec, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		dir, ok := owner[fields[2]]
		if !ok {
			continue
		}
		added, _ := strconv.Atoi(fields[0]) // "-" (binary) counts as 0
		cost[dir] += added
	}
	var cands []candidate
	for dir, c := range cost {
		cands = append(cands, candidate{path: dir, cost: c})
	}
	return cands, nil
}

// plan is every shard's packages for one scope and shard count, or why there
// is no work at all.
type plan struct {
	shards [][]string
	skip   string
}

var errShardCount = errors.New("shard count must be at least 1")

func makePlan(root string, sc scope, n int, cfg *gomutantsConfig) (plan, error) {
	if n < 1 {
		return plan{}, fmt.Errorf("%w, got %d", errShardCount, n)
	}
	p := plan{shards: make([][]string, n)}
	if p.skip = sc.skipReason(); p.skip != "" {
		return p, nil
	}
	pkgs, err := packageFiles(root, cfg)
	if err != nil {
		return plan{}, err
	}
	var cands []candidate
	if sc.tree {
		cands, err = treeCandidates(root, pkgs)
	} else {
		cands, err = diffCandidates(root, sc.base, pkgs)
	}
	if err != nil {
		return plan{}, err
	}
	var inside []candidate
	for _, c := range cands {
		if sc.covers(c.path) {
			inside = append(inside, c)
		}
	}
	if len(inside) == 0 {
		p.skip = "no mutable Go in " + sc.String() + " — skipping mutation testing"
		return p, nil
	}
	p.shards = assign(inside, n)
	return p, nil
}

// patterns is the gomutants package pattern for each module-relative package
// directory.
func patterns(dirs []string) []string {
	out := make([]string, len(dirs))
	for i, d := range dirs {
		out[i] = "./" + d
		if d == "." {
			out[i] = "."
		}
	}
	return out
}
