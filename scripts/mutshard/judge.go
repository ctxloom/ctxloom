package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// report is one shard's result: what it was asked to cover, on which commit,
// and gremlins' own --output for the run (absent when the plan gave it nothing).
type report struct {
	Scope     string          `json:"scope"`
	Head      string          `json:"head"`
	MergeBase string          `json:"merge_base,omitempty"`
	Shard     int             `json:"shard"`
	Shards    int             `json:"shards"`
	Files     []string        `json:"files"`
	Gremlins  json.RawMessage `json:"gremlins,omitempty"`
}

// stamp is what every report of one run must agree on with the aggregate.
type stamp struct {
	scope, head, mergeBase string
}

// gremlinsOutput mirrors gremlins' --output format
// (internal/report/internal.OutputResult) — only the parts judged here.
type gremlinsOutput struct {
	Files      []gremlinsFile `json:"files"`
	Total      int            `json:"mutants_total"`
	Killed     int            `json:"mutants_killed"`
	Lived      int            `json:"mutants_lived"`
	NotViable  int            `json:"mutants_not_viable"`
	NotCovered int            `json:"mutants_not_covered"`
}

type gremlinsFile struct {
	Filename  string             `json:"file_name"`
	Mutations []gremlinsMutation `json:"mutations"`
}

type gremlinsMutation struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// gremlins' mutator.Status strings.
const (
	statusKilled     = "KILLED"
	statusLived      = "LIVED"
	statusNotCovered = "NOT COVERED"
	statusTimedOut   = "TIMED OUT"
	statusNotViable  = "NOT VIABLE"
	statusSkipped    = "SKIPPED"
	statusRunnable   = "RUNNABLE"
)

type thresholds struct {
	efficacy, mutantCoverage float64
}

type tally struct {
	killed, lived, notCovered, timedOut, notViable, skipped int
}

func (t *tally) add(o tally) {
	t.killed += o.killed
	t.lived += o.lived
	t.notCovered += o.notCovered
	t.timedOut += o.timedOut
	t.notViable += o.notViable
	t.skipped += o.skipped
}

// efficacy and mutantCoverage are gremlins' formulas (report.newReport),
// including 0 when nothing was killed.
func (t tally) efficacy() float64 {
	if t.killed == 0 {
		return 0
	}
	return float64(t.killed) / float64(t.killed+t.lived) * 100
}

func (t tally) mutantCoverage() float64 {
	if t.killed+t.lived == 0 {
		return 0
	}
	return float64(t.killed+t.lived) / float64(t.killed+t.lived+t.notCovered) * 100
}

var (
	errMissingShard    = errors.New("the union is incomplete: a shard's report is missing or duplicated")
	errStale           = errors.New("a shard's report is for a different run")
	errMeasuredNothing = errors.New("a shard measured nothing")
	errLeak            = errors.New("a shard mutated a file the plan gave another shard")
	errUnreadable      = errors.New("a shard's gremlins report cannot be read")
	errEfficacy        = errors.New("test efficacy is not above the threshold")
	errMutantCoverage  = errors.New("mutant coverage is not above the threshold")
)

// exitCode is gremlins' own for its two threshold failures, 1 for a refusal.
func exitCode(err error) int {
	switch {
	case errors.Is(err, errEfficacy):
		return 10
	case errors.Is(err, errMutantCoverage):
		return 11
	}
	return 1
}

// judge applies the gate to the UNION of the shards' results, exactly as one
// unsharded gremlins run applies it to its own: the counts are summed and the
// thresholds are judged once (report.assess — failing AT the threshold, not
// only below it). Before any arithmetic it refuses a union it cannot vouch
// for: a missing, duplicated or stale shard, a shard whose run left no
// evidence of having looked, and a shard that tested a file it was not given.
func judge(shards [][]string, st stamp, reports []report, th thresholds) (tally, error) {
	var total tally
	byShard := map[int]report{}
	for _, r := range reports {
		if r.Shards != len(shards) {
			return total, fmt.Errorf("%w: shard %d was cut for %d shards, the plan has %d", errStale, r.Shard, r.Shards, len(shards))
		}
		if _, dup := byShard[r.Shard]; dup || r.Shard < 0 || r.Shard >= len(shards) {
			return total, fmt.Errorf("%w: shard %d reported more than once or out of range", errMissingShard, r.Shard)
		}
		byShard[r.Shard] = r
	}
	for k, files := range shards {
		r, ok := byShard[k]
		if !ok {
			return total, fmt.Errorf("%w: no report from shard %d of %d", errMissingShard, k, len(shards))
		}
		if r.Scope != st.scope || r.Head != st.head || r.MergeBase != st.mergeBase {
			return total, fmt.Errorf("%w: shard %d ran %s at %s (merge base %q), the aggregate is judging %s at %s (merge base %q)",
				errStale, k, r.Scope, r.Head, r.MergeBase, st.scope, st.head, st.mergeBase)
		}
		if !reflect.DeepEqual(nonNil(r.Files), nonNil(files)) {
			return total, fmt.Errorf("%w: shard %d covered %v, the plan gives it %v", errStale, k, r.Files, files)
		}
		if len(files) == 0 {
			continue
		}
		t, err := shardTally(k, files, r.Gremlins)
		if err != nil {
			return total, err
		}
		total.add(t)
	}
	if th.efficacy > 0 && total.efficacy() <= th.efficacy {
		return total, fmt.Errorf("%w: %.2f%% <= %.2f%%", errEfficacy, total.efficacy(), th.efficacy)
	}
	if th.mutantCoverage > 0 && total.mutantCoverage() <= th.mutantCoverage {
		return total, fmt.Errorf("%w: %.2f%% <= %.2f%%", errMutantCoverage, total.mutantCoverage(), th.mutantCoverage)
	}
	return total, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// shardTally reads one shard's gremlins report into counts. Evidence that the
// shard LOOKED is a report with at least one mutation record: every shard
// walks the whole tree bar other shards' files, so a healthy run reports
// thousands, SKIPPED included. gremlins writes no report at all when it found
// no mutant anywhere.
func shardTally(k int, files []string, raw json.RawMessage) (tally, error) {
	var t tally
	if len(raw) == 0 {
		return t, fmt.Errorf("%w: shard %d was given %d file(s) and carries no gremlins report", errMeasuredNothing, k, len(files))
	}
	var out gremlinsOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return t, fmt.Errorf("%w: shard %d: %v", errUnreadable, k, err)
	}
	mine := map[string]bool{}
	for _, f := range files {
		mine[f] = true
	}
	records := 0
	for _, f := range out.Files {
		for _, m := range f.Mutations {
			records++
			switch m.Status {
			case statusKilled:
				t.killed++
			case statusLived:
				t.lived++
			case statusNotCovered:
				t.notCovered++
			case statusTimedOut:
				t.timedOut++
			case statusNotViable:
				t.notViable++
			case statusSkipped:
				t.skipped++
				continue // outside the diff: not tested, wherever it is
			case statusRunnable:
				// A dry run's status; a real run never leaves one.
				return t, fmt.Errorf("%w: shard %d: %s:%d is RUNNABLE — the run was never executed", errMeasuredNothing, k, f.Filename, m.Line)
			default:
				return t, fmt.Errorf("%w: shard %d: unknown status %q at %s:%d", errUnreadable, k, m.Status, f.Filename, m.Line)
			}
			if !mine[f.Filename] {
				return t, fmt.Errorf("%w: shard %d scored %s %s:%d", errLeak, k, m.Status, f.Filename, m.Line)
			}
		}
	}
	if records == 0 {
		return t, fmt.Errorf("%w: shard %d's gremlins report holds no mutation at all", errMeasuredNothing, k)
	}
	if t.killed != out.Killed || t.lived != out.Lived || t.notCovered != out.NotCovered || t.notViable != out.NotViable {
		return t, fmt.Errorf("%w: shard %d: records say %d/%d/%d/%d killed/lived/not-covered/not-viable, gremlins' counters say %d/%d/%d/%d",
			errUnreadable, k, t.killed, t.lived, t.notCovered, t.notViable, out.Killed, out.Lived, out.NotCovered, out.NotViable)
	}
	return t, nil
}
