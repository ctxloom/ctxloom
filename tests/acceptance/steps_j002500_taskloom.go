//go:build acceptance

// J002500: "taskloom's tag surface is real across the process boundary"
// (j002500_taskloom_tags.feature). taskloom's tag-query grammar, fold, and
// handler I/O are already unit/op/handler-tested (tagma via
// internal/shared/tasks, internal/shared/tasks/operations) — this journey
// does not re-prove any of that. It drives the REAL `taskloom` binary as a
// separate OS process (tests/integration/testenv/taskloom_acceptance.go) —
// the seam ctxloom itself never exercises, since ctxloom only ever talks to
// taskloom's MCP tools in-process via its own agent surface — and, for the
// third scenario, the REAL `taskloom mcp` server over stdio, proving the
// agent-instruction surface an MCP-speaking agent actually sees.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// j002500State is J002500's fixture state: the name->harp mapping the tag-matching
// scenario builds, the last tag-query result, the last (possibly failing)
// taskloom invocation's exit/stderr, the harp the append-only scenario is
// mutating, its running sequence of on-disk log snapshots (one per
// operation, oldest first), and the last folded task state read back for the
// independent-re-fold comparison.
type j002500State struct {
	harpByName map[string]string // tag-matching scenario: task name -> harp id
	nameByHarp map[string]string // reverse of the above, for reporting query results by name

	lastQueryHarps []string // tag-matching scenario: harp ids the last successful query returned
	lastFailed     bool     // true after a "... expecting failure" step
	lastExitCode   int
	lastStderr     string

	harp      string      // append-only scenario: the one task being mutated
	snapshots [][]string  // append-only scenario: jsonl line snapshots, oldest first
	lastFold  *tasks.Task // append-only scenario: last folded task read back, for the re-fold comparison
}

func j002500Of(w *World) *j002500State {
	if w.j002500 == nil {
		w.j002500 = &j002500State{harpByName: map[string]string{}, nameByHarp: map[string]string{}}
	}
	return w.j002500
}

func registerJ002500Steps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a fresh taskloom store$`, func(c context.Context) error {
		w := worldFrom(c)
		// A bare `list --json` on a brand-new HOME mints the project identity
		// (first-sight registration) and confirms the store starts empty —
		// the same seeding TestCrossProcessConcurrentAdd's newEnv does before
		// any concurrency, here just to establish a known-clean baseline
		// before the scenario's own writes.
		out, stderr, err := w.env.RunTaskloom(nil, "list", "--json")
		if err != nil {
			return fmt.Errorf("seed taskloom store: %w: %s", err, stderr)
		}
		var got []tasks.Task
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			return fmt.Errorf("seed taskloom store: not valid JSON: %s", out)
		}
		if len(got) != 0 {
			return fmt.Errorf("expected a fresh store, got %d tasks", len(got))
		}
		return nil
	})

	// --- Scenario 1: tag-query exact sets + malformed-query fail-loud ------

	ctx.Step(`^taskloom has tasks:$`, func(c context.Context, table *godog.Table) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		for _, row := range table.Rows[1:] { // skip header
			if len(row.Cells) != 2 {
				return fmt.Errorf("tasks table rows must have exactly 2 cells, got %d", len(row.Cells))
			}
			name := row.Cells[0].Value
			args := []string{"add", name}
			for _, tag := range splitCSV(row.Cells[1].Value) {
				args = append(args, "--tag", tag)
			}
			out, stderr, err := w.env.RunTaskloom(nil, args...)
			if err != nil {
				return fmt.Errorf("add task %q: %w: %s", name, err, stderr)
			}
			harpID, perr := parseTaskloomAddHarp(out)
			if perr != nil {
				return fmt.Errorf("add task %q: %w", name, perr)
			}
			j002500.harpByName[name] = harpID
			j002500.nameByHarp[harpID] = name
		}
		return nil
	})

	ctx.Step(`^taskloom lists tag query "([^"]*)"$`, func(c context.Context, query string) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		out, stderr, err := w.env.RunTaskloom(nil, "list", "--tag-query", query, "--json")
		if err != nil {
			return fmt.Errorf("tag query %q unexpectedly failed: %w: %s", query, err, stderr)
		}
		var got []tasks.Task
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			return fmt.Errorf("tag query %q: not valid JSON: %s", query, out)
		}
		harps := make([]string, 0, len(got))
		for _, t := range got {
			harps = append(harps, t.HarpID)
		}
		j002500.lastQueryHarps = harps
		j002500.lastFailed = false
		return nil
	})

	ctx.Step(`^taskloom lists tag query "([^"]*)" expecting failure$`, func(c context.Context, query string) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		out, stderr, err := w.env.RunTaskloom(nil, "list", "--tag-query", query, "--json")
		if err == nil {
			return fmt.Errorf("tag query %q unexpectedly succeeded: %s", query, out)
		}
		j002500.lastFailed = true
		j002500.lastExitCode = exitCodeOf(err)
		j002500.lastStderr = stderr
		return nil
	})

	ctx.Step(`^the tag query result is exactly "([^"]*)"$`, func(c context.Context, wantCSV string) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		want := splitCSV(wantCSV)
		sort.Strings(want)

		got := make([]string, 0, len(j002500.lastQueryHarps))
		for _, h := range j002500.lastQueryHarps {
			name, ok := j002500.nameByHarp[h]
			if !ok {
				name = h // unrecognized harp — report raw rather than silently dropping it
			}
			got = append(got, name)
		}
		sort.Strings(got)

		if !reflect.DeepEqual(want, got) {
			return fmt.Errorf("tag query result = %v, want %v", got, want)
		}
		return nil
	})

	ctx.Step(`^the taskloom command fails with a nonzero exit$`, func(c context.Context) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		if !j002500.lastFailed {
			return fmt.Errorf("expected the last taskloom command to have failed")
		}
		if j002500.lastExitCode == 0 {
			return fmt.Errorf("expected a nonzero exit code, got 0")
		}
		return nil
	})

	ctx.Step(`^the taskloom command's error output mentions "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		if !strings.Contains(j002500.lastStderr, want) {
			return fmt.Errorf("taskloom stderr does not contain %q; stderr:\n%s", want, j002500.lastStderr)
		}
		return nil
	})

	// --- Scenario 2: append-only lifecycle ---------------------------------

	ctx.Step(`^taskloom adds a task "([^"]*)" with tags "([^"]*)"$`, func(c context.Context, text, tagsCSV string) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		args := []string{"add", text}
		for _, tag := range splitCSV(tagsCSV) {
			args = append(args, "--tag", tag)
		}
		out, stderr, err := w.env.RunTaskloom(nil, args...)
		if err != nil {
			return fmt.Errorf("add task: %w: %s", err, stderr)
		}
		harpID, perr := parseTaskloomAddHarp(out)
		if perr != nil {
			return fmt.Errorf("add task: %w", perr)
		}
		j002500.harp = harpID
		return j002500.snapshotTaskLog(w)
	})

	// --- Scenario: tagma-adoption phase 2 (tag-schema + scalar-collapse) ---

	ctx.Step(`^taskloom adds a task "([^"]*)" with tags "([^"]*)" expecting failure$`, func(c context.Context, text, tagsCSV string) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		args := []string{"add", text}
		for _, tag := range splitCSV(tagsCSV) {
			args = append(args, "--tag", tag)
		}
		out, stderr, err := w.env.RunTaskloom(nil, args...)
		if err == nil {
			return fmt.Errorf("add task %q with tags %q unexpectedly succeeded: %s", text, tagsCSV, out)
		}
		j002500.lastFailed = true
		j002500.lastExitCode = exitCodeOf(err)
		j002500.lastStderr = stderr
		return nil
	})

	ctx.Step(`^the on-disk task log records an untag of "([^"]*)" and a tag of "([^"]*)" for it$`, func(c context.Context, untagged, tagged string) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		events, err := j002500.taskEvents(w)
		if err != nil {
			return err
		}
		var sawUntag, sawTag bool
		for _, ev := range events {
			switch {
			case ev.Op == "untag" && len(ev.Tags) == 1 && ev.Tags[0] == untagged:
				sawUntag = true
			case ev.Op == "tag" && len(ev.Tags) == 1 && ev.Tags[0] == tagged:
				sawTag = true
			}
		}
		if !sawUntag {
			return fmt.Errorf("log events %+v do not record an untag of %q", events, untagged)
		}
		if !sawTag {
			return fmt.Errorf("log events %+v do not record a tag of %q", events, tagged)
		}
		return nil
	})

	ctx.Step(`^the on-disk task log records no untag of "([^"]*)" for it$`, func(c context.Context, value string) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		events, err := j002500.taskEvents(w)
		if err != nil {
			return err
		}
		for _, ev := range events {
			if ev.Op == "untag" {
				for _, tag := range ev.Tags {
					if tag == value {
						return fmt.Errorf("expected no untag of %q, but found one: %+v", value, ev)
					}
				}
			}
		}
		return nil
	})

	ctx.Step(`^taskloom tags it adding "([^"]*)"$`, func(c context.Context, add string) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		args := []string{"tag", j002500.harp}
		for _, tag := range splitCSV(add) {
			args = append(args, "--add", tag)
		}
		_, stderr, err := w.env.RunTaskloom(nil, args...)
		if err != nil {
			return fmt.Errorf("tag task: %w: %s", err, stderr)
		}
		return j002500.snapshotTaskLog(w)
	})

	ctx.Step(`^taskloom untags it removing "([^"]*)"$`, func(c context.Context, remove string) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		args := []string{"tag", j002500.harp}
		for _, tag := range splitCSV(remove) {
			args = append(args, "--remove", tag)
		}
		_, stderr, err := w.env.RunTaskloom(nil, args...)
		if err != nil {
			return fmt.Errorf("untag task: %w: %s", err, stderr)
		}
		return j002500.snapshotTaskLog(w)
	})

	ctx.Step(`^taskloom sets its status to "([^"]*)"$`, func(c context.Context, status string) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		_, stderr, err := w.env.RunTaskloom(nil, "status", j002500.harp, status)
		if err != nil {
			return fmt.Errorf("set status: %w: %s", err, stderr)
		}
		return j002500.snapshotTaskLog(w)
	})

	ctx.Step(`^taskloom edits its text to "([^"]*)"$`, func(c context.Context, text string) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		_, stderr, err := w.env.RunTaskloom(nil, "edit", j002500.harp, text)
		if err != nil {
			return fmt.Errorf("edit task: %w: %s", err, stderr)
		}
		return j002500.snapshotTaskLog(w)
	})

	ctx.Step(`^the on-disk task log grew by exactly one line after each operation$`, func(c context.Context) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		if len(j002500.snapshots) < 2 {
			return fmt.Errorf("expected at least 2 snapshots to compare, got %d", len(j002500.snapshots))
		}
		for i := 1; i < len(j002500.snapshots); i++ {
			prev, cur := j002500.snapshots[i-1], j002500.snapshots[i]
			if len(cur) != len(prev)+1 {
				return fmt.Errorf("snapshot %d has %d lines, snapshot %d has %d lines — expected exactly +1", i-1, len(prev), i, len(cur))
			}
		}
		return nil
	})

	ctx.Step(`^every previously written line is byte-identical across all snapshots$`, func(c context.Context) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		for i := 1; i < len(j002500.snapshots); i++ {
			prev, cur := j002500.snapshots[i-1], j002500.snapshots[i]
			for lineNo, want := range prev {
				if cur[lineNo] != want {
					return fmt.Errorf("line %d changed between snapshot %d and %d:\nwas:  %s\nnow:  %s", lineNo, i-1, i, want, cur[lineNo])
				}
			}
		}
		return nil
	})

	ctx.Step(`^the folded task has text "([^"]*)", status "([^"]*)", and tags "([^"]*)"$`, func(c context.Context, text, status, tagsCSV string) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		got, err := j002500.foldedTask(w)
		if err != nil {
			return err
		}
		j002500.lastFold = got
		if got.Text != text {
			return fmt.Errorf("folded task text = %q, want %q", got.Text, text)
		}
		if got.Status != status {
			return fmt.Errorf("folded task status = %q, want %q", got.Status, status)
		}
		wantTags := splitCSV(tagsCSV)
		sort.Strings(wantTags)
		gotTags := append([]string(nil), got.Tags...)
		sort.Strings(gotTags)
		if !reflect.DeepEqual(gotTags, wantTags) {
			return fmt.Errorf("folded task tags = %v, want %v", gotTags, wantTags)
		}
		return nil
	})

	ctx.Step(`^an independent re-fold reproduces the same task state$`, func(c context.Context) error {
		w := worldFrom(c)
		j002500 := j002500Of(w)
		if j002500.lastFold == nil {
			return fmt.Errorf("no prior folded task to compare against")
		}
		again, err := j002500.foldedTask(w)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(*j002500.lastFold, *again) {
			return fmt.Errorf("independent re-fold differs:\nfirst: %+v\nagain: %+v", *j002500.lastFold, *again)
		}
		return nil
	})

	// --- Scenario 3: agent-instruction MCP surface --------------------------

	ctx.Step(`^the agent connects to the taskloom MCP server$`, func(c context.Context) error {
		w := worldFrom(c)
		client, err := w.env.StartTaskloomMCP()
		if err != nil {
			return fmt.Errorf("start taskloom mcp: %w", err)
		}
		if err := client.Initialize(); err != nil {
			_ = client.Close()
			return fmt.Errorf("initialize taskloom mcp: %w", err)
		}
		w.tlMCP = client
		return nil
	})

	ctx.Step(`^the server instructions mention "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		if w.tlMCP == nil {
			return fmt.Errorf("not connected to the taskloom MCP server")
		}
		instructions := w.tlMCP.Instructions()
		if !strings.Contains(instructions, want) {
			return fmt.Errorf("server instructions do not mention %q; instructions:\n%s", want, instructions)
		}
		return nil
	})

	ctx.Step(`^the tool "([^"]*)" input schema mentions "([^"]*)"$`, func(c context.Context, toolName, want string) error {
		w := worldFrom(c)
		detail, err := j002500ToolDetail(w, toolName)
		if err != nil {
			return err
		}
		if !strings.Contains(detail.SchemaJSON, want) {
			return fmt.Errorf("tool %q input schema does not mention %q; schema:\n%s", toolName, want, detail.SchemaJSON)
		}
		return nil
	})

	ctx.Step(`^the tool "([^"]*)" description mentions "([^"]*)"$`, func(c context.Context, toolName, want string) error {
		w := worldFrom(c)
		detail, err := j002500ToolDetail(w, toolName)
		if err != nil {
			return err
		}
		if !strings.Contains(detail.Description, want) {
			return fmt.Errorf("tool %q description does not mention %q; description:\n%s", toolName, want, detail.Description)
		}
		return nil
	})
}

// snapshotTaskLog reads the current on-disk task log (split into lines, empty
// trailing line dropped) and appends it to j002500's running sequence of
// snapshots. Called once after every taskloom mutation in the append-only
// scenario.
func (j002500 *j002500State) snapshotTaskLog(w *World) error {
	lines, err := readTaskLogLines(w, false) // pre-mutation baseline: absence is legitimate
	if err != nil {
		return err
	}
	j002500.snapshots = append(j002500.snapshots, lines)
	return nil
}

// foldedTask runs a fresh `taskloom list --all --json` (a new OS process —
// an independent re-fold of the on-disk log, not a cached in-memory view)
// and returns the one task under mutation.
func (j002500 *j002500State) foldedTask(w *World) (*tasks.Task, error) {
	out, stderr, err := w.env.RunTaskloom(nil, "list", "--all", "--json")
	if err != nil {
		return nil, fmt.Errorf("list --all --json: %w: %s", err, stderr)
	}
	var got []tasks.Task
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		return nil, fmt.Errorf("list --all --json: not valid JSON: %s", out)
	}
	for i := range got {
		if got[i].HarpID == j002500.harp {
			return &got[i], nil
		}
	}
	return nil, fmt.Errorf("task %s not found in %v", j002500.harp, got)
}

// taskEvents reads the current on-disk task log and returns every event
// belonging to j002500.harp, in file order — the raw seam the scalar-collapse
// scenarios assert against directly (proving the log records BOTH a
// collapsing untag and the new tag, not just the folded Task.Tags view).
//
// This used to decode into a local j002500RawEvent subset-of-Event
// type, justified by a comment claiming "this file doesn't need to import
// the tasks package for a build-tagged acceptance test" — false even at the
// time it was written, since the file already imports
// internal/shared/tasks for tasks.Task (foldedTask, above). Decoding
// straight into tasks.Event removes a duplicate that could silently drift
// from the real on-disk shape.
func (j002500 *j002500State) taskEvents(w *World) ([]tasks.Event, error) {
	lines, err := readTaskLogLines(w, true) // called only after a mutation this scenario performed: absence is a real failure
	if err != nil {
		return nil, err
	}
	var out []tasks.Event
	for _, line := range lines {
		var ev tasks.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, fmt.Errorf("unmarshal event line %q: %w", line, err)
		}
		if ev.Task == j002500.harp {
			out = append(out, ev)
		}
	}
	return out, nil
}

// readTaskLogLines reads the isolated env's single per-project task log and
// splits it into non-empty lines. Returns an empty (nil) slice, not an
// error, when the log does not exist yet (before the first mutation).
func readTaskLogLines(w *World, mustExist bool) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(w.env.HomeDir, ".ctxloom", "tasks", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("glob task log: %w", err)
	}
	if len(matches) == 0 {
		// Absence used to be unconditionally tolerant ((nil, nil)),
		// which is right for the pre-mutation baseline (snapshotTaskLog, before
		// the first taskloom mutation ever ran) but wrong for taskEvents, which
		// is only ever called AFTER a mutation this scenario itself just
		// performed — there a missing log is indistinguishable from "the
		// mutation wrote nothing", and a negative assertion ("no untag of X")
		// would pass vacuously against zero events either way. mustExist lets
		// each caller declare which case it is in.
		if mustExist {
			return nil, fmt.Errorf("expected a per-project task log under %s after a taskloom mutation, found none", filepath.Join(w.env.HomeDir, ".ctxloom", "tasks"))
		}
		return nil, nil
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("expected exactly one per-project task log, found %v", matches)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		return nil, fmt.Errorf("read task log %s: %w", matches[0], err)
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// j002500ToolDetail fetches tools/list from the (already-connected) taskloom MCP
// client and returns the named tool's detail. Refetches every call rather
// than caching — tools/list is cheap and this keeps each assertion
// independent of step ordering.
func j002500ToolDetail(w *World, toolName string) (*testenv.ToolDetail, error) {
	if w.tlMCP == nil {
		return nil, fmt.Errorf("not connected to the taskloom MCP server")
	}
	tools, err := w.tlMCP.ListToolDetails()
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	for i := range tools {
		if tools[i].Name == toolName {
			return &tools[i], nil
		}
	}
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return nil, fmt.Errorf("tool %q not found; available: %v", toolName, names)
}

// parseTaskloomAddHarp extracts the harp id `taskloom add` reported. Shared
// by both "taskloom has tasks:" (a table of several) and "taskloom adds a
// task ..." (a single one), which previously duplicated this verbatim.
//
// `taskloom add` is never asked for a format at these call sites, and
// taskloom shares ctxloom's cliemit.Resolve, so off a terminal (this harness,
// always) its stdout is the JSON tasks.Task object, not the tab-separated
// text line this used to parse. That text-only shape is gone now, not just
// no-longer-default: reverting to a TSV-first parse would break exactly as
// hard as this JSON-only one breaks a raw text line, so there is no format
// worth branching on here — decode the one shape the CLI now emits.
func parseTaskloomAddHarp(out string) (string, error) {
	var t struct {
		HarpID string `json:"harp_id"`
	}
	if err := json.Unmarshal([]byte(out), &t); err != nil {
		return "", fmt.Errorf("could not parse harp id from output %q: %w", out, err)
	}
	if t.HarpID == "" {
		return "", fmt.Errorf("could not parse harp id from output %q: empty harp_id", out)
	}
	return t.HarpID, nil
}

// splitCSV splits a comma-separated cell value into trimmed, non-empty
// tokens. An empty or whitespace-only input yields an empty (nil) slice.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// exitCodeOf extracts the process exit code from a taskloom invocation's
// error: -1 when the process never produced an exit code at all (it failed
// to start), the code cobra's os.Exit(1) (or a normal process exit) actually
// returned otherwise.
func exitCodeOf(err error) int {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}
