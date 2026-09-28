package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// scope is what a mutation run covers: the whole tree, or the changeset
// against a base.
type scope struct {
	base string
	tree bool
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
	if s.tree {
		return scopeTree
	}
	return scopeDiffPrefix + s.base
}

// skipReason is non-empty when the scope cannot name any work at all.
func (s scope) skipReason() string {
	if !s.tree && (s.base == "" || s.base == zeroSHA) {
		return "no base commit to diff against — skipping mutation testing"
	}
	return ""
}

// candidate is a file gremlins would mutate, with an estimate of what it costs.
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

// gremlinsConfig is the project's .gremlins.yaml: the document itself, so a
// shard's config can be derived from it without restating any setting, and the
// parts this tool must interpret the way gremlins does.
type gremlinsConfig struct {
	doc            map[string]any
	excludes       []*regexp.Regexp
	efficacy       float64
	mutantCoverage float64
}

func loadGremlinsConfig(path string) (*gremlinsConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var typed struct {
		Unleash struct {
			ExcludeFiles []string `yaml:"exclude-files"`
			Threshold    struct {
				Efficacy       float64 `yaml:"efficacy"`
				MutantCoverage float64 `yaml:"mutant-coverage"`
			} `yaml:"threshold"`
		} `yaml:"unleash"`
	}
	if err := yaml.Unmarshal(raw, &typed); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg := &gremlinsConfig{doc: doc, efficacy: typed.Unleash.Threshold.Efficacy, mutantCoverage: typed.Unleash.Threshold.MutantCoverage}
	for _, e := range typed.Unleash.ExcludeFiles {
		re, err := regexp.Compile(e)
		if err != nil {
			return nil, fmt.Errorf("%s: exclude-files %q: %w", path, e, err)
		}
		cfg.excludes = append(cfg.excludes, re)
	}
	return cfg, nil
}

// mutable is gremlins' own file rule (engine.Run: a .go file that is not a
// test) followed by the project's exclusions, matched against the same
// module-relative slash path gremlins walks.
func (c *gremlinsConfig) mutable(path string) bool {
	if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
		return false
	}
	for _, re := range c.excludes {
		if re.MatchString(path) {
			return false
		}
	}
	return true
}

// shardConfig is the project config for a shard that must leave others' files
// alone: the project's exclusions EXTENDED (gremlins reads exclude-files as one
// setting, so a command-line --exclude-files would replace the list), and the
// thresholds zeroed, because a shard's slice is not the thing being judged.
func (c *gremlinsConfig) shardConfig(others []string) ([]byte, error) {
	var doc map[string]any
	// Round-trip for a deep copy: the loaded document is shared.
	raw, err := yaml.Marshal(c.doc)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	unleash, _ := doc["unleash"].(map[string]any)
	if unleash == nil {
		unleash = map[string]any{}
		doc["unleash"] = unleash
	}
	if len(others) > 0 {
		quoted := make([]string, len(others))
		for i, p := range others {
			quoted[i] = regexp.QuoteMeta(p)
		}
		excl, _ := unleash["exclude-files"].([]any)
		unleash["exclude-files"] = append(excl, "^(?:"+strings.Join(quoted, "|")+")$")
	}
	unleash["threshold"] = map[string]any{"efficacy": 0, "mutant-coverage": 0}
	return yaml.Marshal(doc)
}

// diffCandidates is gremlins' changeset for base — it runs `git diff
// --merge-base <base>`, so the working tree counts — narrowed to mutable files.
// Cost is the added-line count: --diff mutates only changed lines.
func diffCandidates(root, base string, cfg *gremlinsConfig) ([]candidate, error) {
	cmd := exec.Command("git", "diff", "--merge-base", base, "--numstat", "-z", "--no-renames", "--diff-filter=d", "--", "*.go")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --merge-base %s: %w: %s", base, err, strings.TrimSpace(stderr.String()))
	}
	var cands []candidate
	for _, rec := range strings.Split(string(out), "\x00") {
		fields := strings.SplitN(rec, "\t", 3)
		if len(fields) != 3 || !cfg.mutable(fields[2]) {
			continue
		}
		added, _ := strconv.Atoi(fields[0]) // "-" (binary) counts as 0
		cands = append(cands, candidate{path: fields[2], cost: added})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].path < cands[j].path })
	return cands, nil
}

// treeCandidates walks the filesystem from root as gremlins does — every
// directory, tracked or not — and costs each mutable file by its line count.
func treeCandidates(root string, cfg *gremlinsConfig) ([]candidate, error) {
	var cands []candidate
	err := fs.WalkDir(os.DirFS(root), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !cfg.mutable(path) {
			return nil // gremlins ignores walk errors too
		}
		b, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return err
		}
		cands = append(cands, candidate{path: path, cost: bytes.Count(b, []byte("\n"))})
		return nil
	})
	return cands, err
}

// plan is every shard's files for one scope and shard count, or why there is
// no work at all.
type plan struct {
	shards [][]string
	skip   string
}

var errShardCount = errors.New("shard count must be at least 1")

func makePlan(root string, sc scope, n int, cfg *gremlinsConfig) (plan, error) {
	if n < 1 {
		return plan{}, fmt.Errorf("%w, got %d", errShardCount, n)
	}
	p := plan{shards: make([][]string, n)}
	if p.skip = sc.skipReason(); p.skip != "" {
		return p, nil
	}
	var cands []candidate
	var err error
	if sc.tree {
		cands, err = treeCandidates(root, cfg)
	} else {
		cands, err = diffCandidates(root, sc.base, cfg)
	}
	if err != nil {
		return plan{}, err
	}
	if len(cands) == 0 {
		p.skip = "no mutable Go files in " + sc.String() + " — skipping mutation testing"
		return p, nil
	}
	p.shards = assign(cands, n)
	return p, nil
}
