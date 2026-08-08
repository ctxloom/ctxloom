//go:build acceptance

// Package acceptance: J12, cross-engine transcript capture
// (j12_transcript_capture.feature). Every hermetic scenario is a pure
// file->file conversion: seed the home session index with an entry whose
// transcript_path points at a vendor-native fixture, run `ctxloom session
// backfill`, then read the canonical transcript.jsonl back and assert on its
// REAL payload content and ordering — never on entry counts (memory
// "silent-no-op-failure-mode": a count assertion passes against an
// empty-but-well-formed file, which is exactly the bug class this suite
// exists to catch).
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/transcript"
)

// j12State accumulates this journey's fixture state across a scenario's
// steps. steps_fixture.go's own "a recorded session" step writes a single
// fresh index.yaml per call (fine for every OTHER journey, which only ever
// seeds one session per scenario) — the bulk-backfill scenario here needs
// SEVERAL coexisting index entries in one file, so this journey keeps its
// own running list of raw YAML fragments and rewrites the whole file on each
// addition instead of overwriting the previous entry.
type j12State struct {
	indexEntries []string          // raw "  - harp_name: ...\n" YAML fragments, in the order they were added
	saved        map[string]string // "remember/original" snapshots, keyed by name or "original:<harp>"
}

func j12From(w *World) *j12State {
	if w.j12 == nil {
		w.j12 = &j12State{saved: map[string]string{}}
	}
	return w.j12
}

// j12FixtureFile maps the feature file's <engine> token to the REAL, shipped
// reader testdata fixture vendorreader.VendorAdapter.Convert reads in
// production — the same file each package's own _test.go golden-compares
// against, never a hand-rolled duplicate.
var j12FixtureFile = map[string]string{
	"claude":      filepath.Join("internal", "transcript", "vendorreader", "claude", "testdata", "transcript-fixture.jsonl"),
	"codex":       filepath.Join("internal", "transcript", "vendorreader", "codex", "testdata", "rollout-fixture.jsonl"),
	"antigravity": filepath.Join("internal", "transcript", "vendorreader", "antigravity", "testdata", "transcript_full-fixture.jsonl"),
}

// j12RepoRoot resolves the repo root relative to THIS source file via
// runtime.Caller — steps_j8_guardrails.go's j8LtkLoadoutYAML precedent —
// so the shipped-fixture paths never drift from wherever the repo happens
// to be checked out (never assume the test binary's cwd).
func j12RepoRoot() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("resolve this source file's own path")
	}
	// tests/acceptance/steps_j12_transcript_capture.go -> repo root
	return filepath.Join(filepath.Dir(thisFile), "..", ".."), nil
}

func j12FixturePath(engineKey string) (string, error) {
	rel, ok := j12FixtureFile[engineKey]
	if !ok {
		return "", fmt.Errorf("no shipped vendor-transcript fixture known for engine %q", engineKey)
	}
	root, err := j12RepoRoot()
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, rel)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("shipped %s vendor-transcript fixture missing at %s: %w", engineKey, path, err)
	}
	return path, nil
}

// j12SeededEngineVersion is the engine version a seeded session claims to have
// run under. It is required, not decorative: reader selection is (engine,
// RECORDED version) -> adapter, and a session with no engine_version REFUSES
// to be read at all (vendorreader.SelectAdapter) — which is the correct
// production behaviour and would make every backfill scenario here fail for
// the wrong reason. The values are .github/engine-versions.env's pins, so a
// seeded session claims exactly the version ctxloom's readers are validated
// against. An unrecognised backend deliberately gets no version, so a
// scenario that WANTS the refusal can still ask for one.
func j12SeededEngineVersion(backend string) string {
	switch backend {
	case "claude-code":
		return "2.1.214"
	case "codex":
		return "0.144.6"
	case "antigravity":
		return "1.1.4"
	case "kiro":
		return "2.13.0"
	default:
		return ""
	}
}

// j12AddIndexEntry appends one session to the home index.yaml, preserving
// every entry a prior call in this scenario already wrote (see j12State's
// doc comment). project_dir is the live project so current-project listing
// finds it too, mirroring steps_fixture.go's "a recorded session" step.
func j12AddIndexEntry(w *World, harp, backend, transcriptPath string) error {
	st := j12From(w)
	entry := fmt.Sprintf("  - harp_name: %s\n"+
		"    session_id: seeded-%s\n"+
		"    backend: %s\n"+
		"    project_dir: %s\n"+
		"    started_at: 2026-01-01T00:00:00Z\n"+
		"    transcript_path: %q\n"+
		"    engine_version: %s\n"+
		"    summary: seeded acceptance session\n",
		harp, harp, backend, w.env.ProjectDir, transcriptPath, j12SeededEngineVersion(backend))
	st.indexEntries = append(st.indexEntries, entry)
	body := "sessions:\n" + strings.Join(st.indexEntries, "")
	return w.env.WriteHomeFile(".ctxloom/sessions/index.yaml", body)
}

// j12CanonicalTranscriptRelPath resolves harp's captured canonical
// transcript's path relative to the isolated HOME, preferring the current
// leaf name (paths.CanonicalTranscriptFileName) but falling back to the
// pre-rename leaf — mirroring paths.ResolveHarpCanonicalTranscriptPath's own
// two-name fallback (internal/paths/paths.go:282), reimplemented locally
// because that resolver reads the REAL process's home dir via
// os.UserHomeDir, not this scenario's isolated one (w.env.HomeDir): the CLI
// subprocess and this test process do not share a HOME.
func j12CanonicalTranscriptRelPath(w *World, harp string) (string, error) {
	current := ".ctxloom/sessions/" + harp + "/persist/transcript.jsonl"
	if w.env.HomeFileExists(current) {
		return current, nil
	}
	legacy := ".ctxloom/sessions/" + harp + "/persist/transcript.acp.jsonl"
	if w.env.HomeFileExists(legacy) {
		return legacy, nil
	}
	return "", fmt.Errorf("no canonical transcript captured for harp %q (checked %s and legacy %s)", harp, current, legacy)
}

func j12ReadCanonicalTranscript(w *World, harp string) (string, error) {
	rel, err := j12CanonicalTranscriptRelPath(w, harp)
	if err != nil {
		return "", err
	}
	return w.env.ReadHomeFile(rel)
}

// j12ReadCanonicalRecords parses harp's canonical transcript into the real
// production transcript.Record type — the same schema the Recorder writes
// and every other reader (distill, resume) consumes — rather than a
// hand-rolled duplicate shape that could silently drift from it.
func j12ReadCanonicalRecords(w *World, harp string) ([]transcript.Record, error) {
	body, err := j12ReadCanonicalTranscript(w, harp)
	if err != nil {
		return nil, err
	}
	var recs []transcript.Record
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r transcript.Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("parse canonical transcript line for %q: %w\nline: %s", harp, err, line)
		}
		recs = append(recs, r)
	}
	return recs, nil
}

// j12FindEntry returns the index and payload of the first KindEntry record
// matching pred, in file order.
func j12FindEntry(recs []transcript.Record, pred func(transcript.EntryPayload) bool) (int, *transcript.EntryPayload, bool) {
	for i, r := range recs {
		if r.Kind == transcript.KindEntry && r.Entry != nil && pred(*r.Entry) {
			return i, r.Entry, true
		}
	}
	return -1, nil, false
}

// j12EngineTurnCheck is one real, grounded assertion about a shipped
// fixture's conversation — checked in order, each index required to be
// strictly greater than the previous one (real seq order preserved).
type j12EngineTurnCheck struct {
	label string
	match func(transcript.EntryPayload) bool
}

// j12EngineTurnChecks pins the Scenario Outline's per-<engine> real-turn
// assertions, grounded in each package's own shipped golden.transcript.acp.jsonl
// (internal/transcript/vendorreader/{codex,antigravity}/testdata/): codex's
// exec_command call/result/reply carry the real "HELLO_FROM_TOOL_CALL_42"
// sentinel; antigravity's carry the real "XXX-OVERWRITTEN-XXX" one. kiro is
// deliberately absent — see the feature file's own deferral note.
var j12EngineTurnChecks = map[string][]j12EngineTurnCheck{
	"codex": {
		{"the real user prompt", func(e transcript.EntryPayload) bool {
			return e.Type == "user" && strings.Contains(e.Content, "Run `echo HELLO_FROM_TOOL_CALL_42` and tell me what it printed.")
		}},
		{"the real exec_command tool call", func(e transcript.EntryPayload) bool {
			return e.Type == "tool_use" && e.ToolName == "exec_command" && strings.Contains(string(e.ToolInput), "echo HELLO_FROM_TOOL_CALL_42")
		}},
		{"the real tool result output", func(e transcript.EntryPayload) bool {
			return e.Type == "tool_result" && strings.Contains(e.ToolOutput, "HELLO_FROM_TOOL_CALL_42")
		}},
		{"the real final reply", func(e transcript.EntryPayload) bool {
			return e.Type == "assistant" && strings.Contains(e.Content, "HELLO_FROM_TOOL_CALL_42")
		}},
	},
	"antigravity": {
		{"the real user request", func(e transcript.EntryPayload) bool {
			return e.Type == "user" && strings.Contains(e.Content, "XXX-OVERWRITTEN-XXX")
		}},
		{"the real first assistant step", func(e transcript.EntryPayload) bool {
			return e.Type == "assistant" && strings.Contains(e.Content, "I will start by checking the current permissions")
		}},
		{"the real final assistant summary", func(e transcript.EntryPayload) bool {
			return e.Type == "assistant" && strings.Contains(e.Content, "I have successfully written the exact text")
		}},
	},
}

// --- The attribution fixture (queue-interleaved scenario) -----------------
//
// Hand-built, NOT a real captured session (memory "derive a small one
// preserving the record SHAPE" — never commit a real session as a fixture),
// but its shape is grounded in a real one: claude's queue-operation records
// ("enqueue"/"dequeue"/"remove", each carrying the queued message's own
// "content" field) were inspected directly on a real 2298-record session on
// this box. A "remove" never gets a corresponding "user" line of its own —
// confirmed by inspecting every removed entry's neighbors in that real file
// — so j12RemovedPrompt below must never appear anywhere in the converted
// output. A "dequeue" IS immediately followed by a real "user" line carrying
// the same content — confirmed the same way — which is what
// j12QueueInterleavedFixture reproduces for the delivered second prompt.
const (
	j12SecondPrompt  = "Also, what's the capital of France?"
	j12RemovedPrompt = "nevermind, skip that, I changed my mind"
	j12FirstAnswer   = "6 times 7 is 42."
)

// j12QueueInterleavedFixture builds a claude-shaped vendor transcript where:
//  1. the user asks "What is 6 times 7?"
//  2. the assistant starts responding (a thinking block, message id msg_first)
//  3. a second prompt is enqueued, then a THIRD is enqueued and removed
//     (never delivered) BEFORE the second is dequeued
//  4. the second prompt is dequeued and delivered — its "user" line lands
//     HERE, spliced into the middle of msg_first's still-open response
//  5. msg_first's response concludes (same message id) with the REAL answer
//     to the FIRST prompt, positioned in the file AFTER the interleaved
//     second prompt
//  6. a second response (message id msg_second) answers the second prompt
//
// A downstream reader that naively pairs "the assistant text right after a
// user line answers that user line" would attribute step 5's answer to the
// second prompt, not the first — the sequence order proved by this fixture
// is exactly what defeats that.
func j12QueueInterleavedFixture(sessionID string) string {
	lines := []string{
		fmt.Sprintf(`{"type":"user","sessionId":%q,"message":{"role":"user","content":"What is 6 times 7?"}}`, sessionID),
		fmt.Sprintf(`{"type":"assistant","sessionId":%q,"message":{"model":"claude-haiku-4-5-20251001","id":"msg_first","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"Computing 6 times 7."}],"stop_reason":null,"usage":{"input_tokens":5,"output_tokens":1}}}`, sessionID),
		fmt.Sprintf(`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-07-25T13:00:01.000Z","sessionId":%q,"content":%q}`, sessionID, j12SecondPrompt),
		fmt.Sprintf(`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-07-25T13:00:02.000Z","sessionId":%q,"content":%q}`, sessionID, j12RemovedPrompt),
		fmt.Sprintf(`{"type":"queue-operation","operation":"remove","timestamp":"2026-07-25T13:00:03.000Z","sessionId":%q,"content":%q}`, sessionID, j12RemovedPrompt),
		fmt.Sprintf(`{"type":"queue-operation","operation":"dequeue","timestamp":"2026-07-25T13:00:04.000Z","sessionId":%q}`, sessionID),
		fmt.Sprintf(`{"type":"user","sessionId":%q,"message":{"role":"user","content":%q}}`, sessionID, j12SecondPrompt),
		fmt.Sprintf(`{"type":"assistant","sessionId":%q,"message":{"model":"claude-haiku-4-5-20251001","id":"msg_first","type":"message","role":"assistant","content":[{"type":"text","text":%q}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":6}}}`, sessionID, j12FirstAnswer),
		fmt.Sprintf(`{"type":"assistant","sessionId":%q,"message":{"model":"claude-haiku-4-5-20251001","id":"msg_second","type":"message","role":"assistant","content":[{"type":"text","text":"Paris is the capital of France."}],"stop_reason":"end_turn","usage":{"input_tokens":6,"output_tokens":6}}}`, sessionID),
	}
	return strings.Join(lines, "\n") + "\n"
}

func registerJ12Steps(ctx *godog.ScenarioContext) {
	// --- Given: fixtures -----------------------------------------------

	ctx.Step(`^a recorded "([^"]*)" session "([^"]*)" bound to the shipped (\S+) vendor-transcript fixture$`,
		func(c context.Context, backend, harp, engineKey string) error {
			w := worldFrom(c)
			path, err := j12FixturePath(engineKey)
			if err != nil {
				return err
			}
			return j12AddIndexEntry(w, harp, backend, path)
		})

	ctx.Step(`^a recorded "([^"]*)" session "([^"]*)"$`, func(c context.Context, backend, harp string) error {
		return j12AddIndexEntry(worldFrom(c), harp, backend, "")
	})

	ctx.Step(`^a recorded "([^"]*)" session "([^"]*)" that already has a canonical transcript and a shipped (\S+) vendor fixture$`,
		func(c context.Context, backend, harp, engineKey string) error {
			w := worldFrom(c)
			path, err := j12FixturePath(engineKey)
			if err != nil {
				return err
			}
			if err := j12AddIndexEntry(w, harp, backend, path); err != nil {
				return err
			}
			original := j12SeedCanonicalTranscript(w, harp)
			j12From(w).saved["original:"+harp] = original
			return nil
		})

	ctx.Step(`^a recorded "([^"]*)" session "([^"]*)" bound to a vendor log where a second prompt is delivered mid-response and a third is queued then removed$`,
		func(c context.Context, backend, harp string) error {
			w := worldFrom(c)
			rel := filepath.Join("j12-fixtures", harp+".jsonl")
			if err := w.env.WriteFile(rel, j12QueueInterleavedFixture(harp)); err != nil {
				return err
			}
			abs := filepath.Join(w.env.ProjectDir, rel)
			return j12AddIndexEntry(w, harp, backend, abs)
		})

	// --- Then: transcript content + attribution -------------------------

	ctx.Step(`^the canonical transcript for "([^"]*)" replays the fixture's real turns in order: the user goal, the assistant reply, and the Glob and Bash tool calls with their real inputs$`,
		func(c context.Context, harp string) error {
			w := worldFrom(c)
			recs, err := j12ReadCanonicalRecords(w, harp)
			if err != nil {
				return err
			}
			iUser, _, ok := j12FindEntry(recs, func(e transcript.EntryPayload) bool {
				return e.Type == "user" && strings.Contains(e.Content, "GOAL: Truthfulness audit")
			})
			if !ok {
				return fmt.Errorf("canonical transcript for %q: no user entry carries the real GOAL prompt", harp)
			}
			iAsst, _, ok := j12FindEntry(recs, func(e transcript.EntryPayload) bool {
				return e.Type == "assistant" && strings.Contains(e.Content, "I'll conduct this truthfulness audit")
			})
			if !ok {
				return fmt.Errorf("canonical transcript for %q: no assistant entry carries the real reply text", harp)
			}
			iGlob, globEntry, ok := j12FindEntry(recs, func(e transcript.EntryPayload) bool {
				return e.Type == "tool_use" && e.ToolName == "Glob" && strings.Contains(string(e.ToolInput), "website/src/content/docs/**/*.md")
			})
			if !ok {
				return fmt.Errorf("canonical transcript for %q: no Glob tool_use carries the real pattern input", harp)
			}
			iBash, bashEntry, ok := j12FindEntry(recs, func(e transcript.EntryPayload) bool {
				return e.Type == "tool_use" && e.ToolName == "Bash" && strings.Contains(string(e.ToolInput), "ls -la ~/.ctxloom/sessions/slow-pulpy-borax/acp-hub-design.plan.md")
			})
			if !ok {
				return fmt.Errorf("canonical transcript for %q: no Bash tool_use carries the real command input", harp)
			}
			if !(iUser < iAsst && iAsst < iGlob && iGlob < iBash) {
				return fmt.Errorf("canonical transcript for %q: real turns are out of order (user=%d assistant=%d glob=%d bash=%d)", harp, iUser, iAsst, iGlob, iBash)
			}
			// Stashed for the next Then step's ATTRIBUTION check: which
			// tool_call_id the rejected result must (and must not) match.
			st := j12From(w)
			st.saved["glob-call-id:"+harp] = globEntry.ToolCallID
			st.saved["bash-call-id:"+harp] = bashEntry.ToolCallID
			return nil
		})

	// ATTRIBUTION, not just presence: the is_error tool_result must carry the
	// BASH call's tool_call_id specifically, not merely exist somewhere and
	// not the Glob call's — a swapped/misattributed result would read
	// identically under a presence-only check.
	ctx.Step(`^the canonical transcript for "([^"]*)" preserves the tool_result marked is_error from the rejected call$`,
		func(c context.Context, harp string) error {
			w := worldFrom(c)
			recs, err := j12ReadCanonicalRecords(w, harp)
			if err != nil {
				return err
			}
			_, result, ok := j12FindEntry(recs, func(e transcript.EntryPayload) bool {
				return e.Type == "tool_result" && e.IsError && strings.Contains(e.ToolOutput, "The user doesn't want to proceed with this tool use")
			})
			if !ok {
				return fmt.Errorf("canonical transcript for %q: no is_error tool_result carries the real rejection text", harp)
			}
			st := j12From(w)
			bashID := st.saved["bash-call-id:"+harp]
			globID := st.saved["glob-call-id:"+harp]
			if bashID == "" {
				return fmt.Errorf("canonical transcript for %q: Bash tool_use id not captured — run the turn-order step first", harp)
			}
			if result.ToolCallID != bashID {
				return fmt.Errorf("canonical transcript for %q: is_error tool_result carries tool_call_id %q, not the rejected Bash call's %q — misattributed", harp, result.ToolCallID, bashID)
			}
			if result.ToolCallID == globID {
				return fmt.Errorf("canonical transcript for %q: is_error tool_result wrongly attributed to the Glob call", harp)
			}
			return nil
		})

	ctx.Step(`^every line of the canonical transcript for "([^"]*)" is stamped engine "([^"]*)"$`,
		func(c context.Context, harp, engine string) error {
			w := worldFrom(c)
			recs, err := j12ReadCanonicalRecords(w, harp)
			if err != nil {
				return err
			}
			if len(recs) == 0 {
				return fmt.Errorf("canonical transcript for %q has no records — an empty file trivially satisfies \"every line\" and must not pass silently", harp)
			}
			for i, r := range recs {
				if r.Engine != engine {
					return fmt.Errorf("canonical transcript for %q: line %d carries engine %q, want %q", harp, i, r.Engine, engine)
				}
			}
			return nil
		})

	ctx.Step(`^the canonical transcript for "([^"]*)" replays the (\S+) fixture's real conversation turns in seq order$`,
		func(c context.Context, harp, engine string) error {
			w := worldFrom(c)
			recs, err := j12ReadCanonicalRecords(w, harp)
			if err != nil {
				return err
			}
			checks, ok := j12EngineTurnChecks[engine]
			if !ok {
				return fmt.Errorf("no known real-turn assertions for engine %q", engine)
			}
			last := -1
			for _, chk := range checks {
				idx, _, found := j12FindEntry(recs, chk.match)
				if !found {
					return fmt.Errorf("canonical transcript for %q: missing real turn %q", harp, chk.label)
				}
				if idx <= last {
					return fmt.Errorf("canonical transcript for %q: turn %q is out of seq order (index %d, previous %d)", harp, chk.label, idx, last)
				}
				last = idx
			}
			return nil
		})

	// --- Idempotency (byte-identical re-run) -----------------------------

	ctx.Step(`^I remember the canonical transcript for "([^"]*)" as "([^"]*)"$`, func(c context.Context, harp, name string) error {
		w := worldFrom(c)
		body, err := j12ReadCanonicalTranscript(w, harp)
		if err != nil {
			return err
		}
		j12From(w).saved[name] = body
		return nil
	})

	ctx.Step(`^the canonical transcript for "([^"]*)" is unchanged from "([^"]*)"$`, func(c context.Context, harp, name string) error {
		w := worldFrom(c)
		want, ok := j12From(w).saved[name]
		if !ok {
			return fmt.Errorf("no remembered canonical-transcript snapshot named %q", name)
		}
		got, err := j12ReadCanonicalTranscript(w, harp)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("canonical transcript for %q changed after re-running backfill:\n--- before ---\n%s\n--- after ---\n%s", harp, want, got)
		}
		return nil
	})

	ctx.Step(`^the canonical transcript for "([^"]*)" still holds its original captured content$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		want, ok := j12From(w).saved["original:"+harp]
		if !ok {
			return fmt.Errorf("no original canonical transcript was seeded for %q", harp)
		}
		got, err := j12ReadCanonicalTranscript(w, harp)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("canonical transcript for %q was modified by backfill (expected untouched):\n--- original ---\n%s\n--- now ---\n%s", harp, want, got)
		}
		return nil
	})

	// --- Backfill report (converted/skipped/failed) ----------------------

	ctx.Step(`^the backfill run reports "([^"]*)" as skipped, not converted$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		if strings.Contains(out, "converted: "+harp) {
			return fmt.Errorf("backfill run reports %q as converted, want skipped; output:\n%s", harp, out)
		}
		if !strings.Contains(out, "0 converted, 1 skipped, 0 failed") {
			return fmt.Errorf("backfill run does not show a clean single-entry skip for %q; output:\n%s", harp, out)
		}
		return nil
	})

	ctx.Step(`^the backfill summary reports 1 converted and at least 1 skipped$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		re := regexp.MustCompile(`(\d+) converted, (\d+) skipped, (\d+) failed`)
		m := re.FindStringSubmatch(out)
		if m == nil {
			return fmt.Errorf("backfill output has no summary line; output:\n%s", out)
		}
		converted, _ := strconv.Atoi(m[1])
		skipped, _ := strconv.Atoi(m[2])
		if converted != 1 {
			return fmt.Errorf("backfill summary reports %d converted, want 1; output:\n%s", converted, out)
		}
		if skipped < 1 {
			return fmt.Errorf("backfill summary reports %d skipped, want at least 1; output:\n%s", skipped, out)
		}
		return nil
	})

	ctx.Step(`^no canonical transcript is written for "([^"]*)"$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		current := ".ctxloom/sessions/" + harp + "/persist/transcript.jsonl"
		legacy := ".ctxloom/sessions/" + harp + "/persist/transcript.acp.jsonl"
		if w.env.HomeFileExists(current) {
			return fmt.Errorf("canonical transcript unexpectedly written for %q at %s", harp, current)
		}
		if w.env.HomeFileExists(legacy) {
			return fmt.Errorf("canonical transcript unexpectedly written for %q at legacy path %s", harp, legacy)
		}
		return nil
	})

	// --- Attribution (queue-interleaved fixture) --------------------------

	ctx.Step(`^the canonical transcript for "([^"]*)" places the real answer to the first prompt after the interleaved second prompt, exactly as the vendor log ordered them$`,
		func(c context.Context, harp string) error {
			w := worldFrom(c)
			recs, err := j12ReadCanonicalRecords(w, harp)
			if err != nil {
				return err
			}
			iSecondPrompt, _, ok := j12FindEntry(recs, func(e transcript.EntryPayload) bool {
				return e.Type == "user" && strings.Contains(e.Content, j12SecondPrompt)
			})
			if !ok {
				return fmt.Errorf("canonical transcript for %q: the interleaved second prompt never arrived", harp)
			}
			iAnswer, _, ok := j12FindEntry(recs, func(e transcript.EntryPayload) bool {
				return e.Type == "assistant" && strings.Contains(e.Content, j12FirstAnswer)
			})
			if !ok {
				return fmt.Errorf("canonical transcript for %q: the real answer to the first prompt never arrived", harp)
			}
			if iAnswer < iSecondPrompt {
				return fmt.Errorf("canonical transcript for %q: the vendor log's real order was not preserved — the first prompt's answer (index %d) landed BEFORE the interleaved second prompt (index %d), which the real vendor log never did", harp, iAnswer, iSecondPrompt)
			}
			return nil
		})

	ctx.Step(`^the canonical transcript for "([^"]*)" contains no trace of the removed, never-delivered prompt$`,
		func(c context.Context, harp string) error {
			w := worldFrom(c)
			body, err := j12ReadCanonicalTranscript(w, harp)
			if err != nil {
				return err
			}
			if strings.Contains(body, j12RemovedPrompt) {
				return fmt.Errorf("canonical transcript for %q leaked the removed, never-delivered prompt %q into a captured turn", harp, j12RemovedPrompt)
			}
			return nil
		})
}

// j12SeedCanonicalTranscript writes a minimal, realistic already-captured
// canonical transcript for harp (one real transcript.Record, marshaled
// through the production type) so ConvertVendorTranscript's
// hasCanonicalTranscript guard sees it as already-captured and skips —
// exercising the "never clobber a live capture" invariant. Returns the exact
// bytes written, so the caller can assert byte-for-byte non-modification
// later.
func j12SeedCanonicalTranscript(w *World, harp string) string {
	rec := transcript.Record{
		V:      transcript.SchemaVersion,
		Harp:   harp,
		Engine: "claude-code",
		Seq:    0,
		Kind:   transcript.KindEntry,
		Entry: &transcript.EntryPayload{
			Type:    "user",
			Content: "ORIGINAL CAPTURED CONTENT for " + harp + " — backfill must never overwrite this",
		},
	}
	data, err := json.Marshal(rec)
	if err != nil {
		// A marshal failure here is a bug in this fixture builder, not
		// something a scenario should silently limp past.
		panic(fmt.Sprintf("j12SeedCanonicalTranscript: marshal record: %v", err))
	}
	line := string(data) + "\n"
	rel := ".ctxloom/sessions/" + harp + "/persist/transcript.jsonl"
	if err := w.env.WriteHomeFile(rel, line); err != nil {
		panic(fmt.Sprintf("j12SeedCanonicalTranscript: write %s: %v", rel, err))
	}
	return line
}
