// Package contextmetrics persists and reads a per-session series of
// context-window occupancy samples, so that "how full is this conversation"
// becomes a MEASUREMENT an agent can take rather than a feeling it acts on.
//
// The writer is the statusline callback (`ctxloom hook hud`), which the
// harness re-runs on every refresh and hands the session JSON that carries the
// engine's own context accounting. The reader is the `context_status` MCP
// tool. The two meet only at this package's Sample shape and at one file per
// session — ~/.ctxloom/sessions/<harp>/persist/context-metrics.jsonl.
//
// The file is under persist/ rather than ephemeral/ deliberately: a context
// series describes the SESSION, and outliving a workspace teardown is the
// whole point of being able to ask "was I already at 80% before the last
// compaction?".
package contextmetrics

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// FileName is the persist/ leaf holding one session's context-occupancy
// series: one JSON object per line, append-only, oldest first.
const FileName = "context-metrics.jsonl"

// Sample is one observation of a session's context-window occupancy.
//
// ContextPct is the ENGINE's own percentage, not one recomputed here. Claude
// Code (2.1.229, verified against the shipped statusline payload builder)
// reports `context_window.used_percentage` already rounded to an integer and
// clamped to 0..100 — and reports it as JSON null, not 0, while a session has
// yet to accumulate any usage. A sample only exists for an observation the
// engine actually made: the absence of data is represented by the absence of
// a sample, never by a zero, because a zero here reads as "plenty of room".
type Sample struct {
	TS         time.Time `json:"ts"`
	Harp       string    `json:"harp,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
	ContextPct float64   `json:"context_pct"`
	TokensUsed int       `json:"tokens_used"`
	Window     int       `json:"window"`
	Model      string    `json:"model,omitempty"`
}

// Sampling rule. A statusline refreshes far more often than the number
// changes — many times per assistant message — so writing every refresh would
// grow an unbounded file that says the same thing over and over. A sample is
// kept only when it CARRIES NEWS: the percentage moved by at least a point
// (the engine's own resolution: it reports whole integers, so a smaller delta
// is not observable), or enough wall time passed that a flat stretch is worth
// recording as evidence of its own.
const (
	// MinPctDelta is the movement that makes a sample worth keeping.
	MinPctDelta = 1.0
	// MinInterval is how long a stationary percentage may go unrecorded.
	MinInterval = 60 * time.Second
)

// Path returns the metrics file for harp. It does not create anything.
func Path(harp string) (string, error) {
	if harp == "" {
		return "", fmt.Errorf("context metrics: no session harp")
	}
	dir, err := paths.HarpPersistDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// ShouldAppend reports whether next carries news relative to prev — the
// sampling rule above. A nil prev (no series yet) always does.
//
// Deliberately pure and exported: it is the whole of the policy, and the
// alternative — inlining the two comparisons at the one call site — leaves
// the rule assertable only by writing files and counting lines.
func ShouldAppend(prev *Sample, next Sample) bool {
	if prev == nil {
		return true
	}
	if math.Abs(next.ContextPct-prev.ContextPct) >= MinPctDelta {
		return true
	}
	return next.TS.Sub(prev.TS) >= MinInterval
}

// Append writes one sample to harp's series, creating the persist dir if
// needed.
//
// One marshalled line per write call, O_APPEND: concurrent statusline
// processes (a refresh can overlap its predecessor) interleave whole lines
// rather than corrupting each other, and no writer ever rewrites what another
// wrote. There is no locking and none is wanted — a duplicate line from two
// racing refreshes is a harmless repetition of the truth, whereas a lock is a
// way for a statusline to hang.
func Append(harp string, s Sample) error {
	path, err := Path(harp)
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		return fmt.Errorf("context metrics: create session dir: %w", mkErr)
	}
	line, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("context metrics: marshal sample: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("context metrics: open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("context metrics: append to %s: %w", path, err)
	}
	return nil
}

// Read returns harp's whole series, oldest first.
//
// A line that does not parse is SKIPPED rather than failing the read: this
// file is written by concurrent appenders and read by a tool whose entire
// purpose is to answer under pressure, so one torn or half-written line must
// not cost the caller every good sample around it. A missing file is not an
// error — it is an empty series, which is exactly what the caller must be
// able to distinguish from a series of zeroes.
func Read(harp string) ([]Sample, error) {
	path, err := Path(harp)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("context metrics: open %s: %w", path, err)
	}
	defer f.Close()

	var out []Sample
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var s Sample
		if err := json.Unmarshal(line, &s); err != nil {
			continue
		}
		out = append(out, s)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("context metrics: read %s: %w", path, err)
	}
	return out, nil
}

// Tail returns the last n samples of harp's series, oldest first. n <= 0
// returns nothing.
func Tail(harp string, n int) ([]Sample, error) {
	all, err := Read(harp)
	if err != nil {
		return nil, err
	}
	if n <= 0 || len(all) == 0 {
		return nil, nil
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

// Last returns harp's most recent sample, or nil when the series is empty.
func Last(harp string) (*Sample, error) {
	all, err := Read(harp)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, nil
	}
	last := all[len(all)-1]
	return &last, nil
}

// Record applies the sampling rule and appends only if next carries news,
// reporting whether it wrote. It is the writer's whole entry point.
func Record(harp string, next Sample) (bool, error) {
	prev, err := Last(harp)
	if err != nil {
		return false, err
	}
	if !ShouldAppend(prev, next) {
		return false, nil
	}
	if err := Append(harp, next); err != nil {
		return false, err
	}
	return true, nil
}
