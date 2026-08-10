//go:build acceptance

// Package acceptance: J001000, cross-engine transcript capture
// (j001000_transcript_capture.feature). Every hermetic scenario is a pure
// file->file conversion driven through the RECALL trigger: seed the home
// session index with an entry whose transcript_path points at a vendor-native
// fixture, ask for that session's memory over MCP, then read the canonical
// transcript.jsonl back and assert on its REAL payload content and ordering —
// never on entry counts alone (memory "silent-no-op-failure-mode": a count
// assertion passes against an empty-but-well-formed file, which is exactly the
// bug class this suite exists to catch).
//
// WHY MCP AND NOT A CLI VERB. operations.ConvertVendorTranscript has exactly
// two call sites — the interactive-pty exit seam and the recover_session tool
// — and only the second is reachable without a real engine binary and a pty.
// The tool is therefore the honest hermetic door to the conversion, not a
// stand-in for one.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/transcript"
)

// j001000State accumulates this journey's fixture state across a scenario's
// steps. steps_fixture.go's own "a recorded session" step writes a single
// fresh index.yaml per call (fine for every OTHER journey, which only ever
// seeds one session per scenario) — so this journey keeps its own running list
// of raw YAML fragments and rewrites the whole file on each addition instead
// of overwriting the previous entry.
type j001000State struct {
	indexEntries []string          // raw "  - harp_name: ...\n" YAML fragments, in the order they were added
	backends     map[string]string // harp -> the backend registry name its index entry claims
	saved        map[string]string // "remember/original" snapshots, keyed by name or "original:<harp>"
	noted        map[string][]int  // harp -> the seq of every record its transcript held when noted
}

func j001000From(w *World) *j001000State {
	if w.j001000 == nil {
		w.j001000 = &j001000State{
			backends: map[string]string{},
			saved:    map[string]string{},
			noted:    map[string][]int{},
		}
	}
	return w.j001000
}

// j001000FixtureFile maps the feature file's <engine> token to the REAL, shipped
// reader testdata fixture vendorreader.VendorAdapter.Convert reads in
// production — the same file each package's own _test.go golden-compares
// against, never a hand-rolled duplicate.
var j001000FixtureFile = map[string]string{
	"claude":      filepath.Join("internal", "transcript", "vendorreader", "claude", "testdata", "transcript-fixture.jsonl"),
	"codex":       filepath.Join("internal", "transcript", "vendorreader", "codex", "testdata", "rollout-fixture.jsonl"),
	"antigravity": filepath.Join("internal", "transcript", "vendorreader", "antigravity", "testdata", "transcript_full-fixture.jsonl"),
}

// j001000RepoRoot resolves the repo root relative to THIS source file via
// runtime.Caller — steps_j001800_guardrails.go's j001800LtkLoadoutYAML precedent —
// so the shipped-fixture paths never drift from wherever the repo happens
// to be checked out (never assume the test binary's cwd).
func j001000RepoRoot() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("resolve this source file's own path")
	}
	// tests/acceptance/steps_j001000_transcript_capture.go -> repo root
	return filepath.Join(filepath.Dir(thisFile), "..", ".."), nil
}

func j001000FixturePath(engineKey string) (string, error) {
	rel, ok := j001000FixtureFile[engineKey]
	if !ok {
		return "", fmt.Errorf("no shipped vendor-transcript fixture known for engine %q", engineKey)
	}
	root, err := j001000RepoRoot()
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, rel)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("shipped %s vendor-transcript fixture missing at %s: %w", engineKey, path, err)
	}
	return path, nil
}

// j001000SeededEngineVersion is the engine version a seeded session claims to have
// run under. It is required, not decorative: reader selection is (engine,
// RECORDED version) -> adapter, and a session with no engine_version REFUSES
// to be read at all (vendorreader.SelectAdapter) — which is the correct
// production behaviour and would make every conversion scenario here fail for
// the wrong reason. The values are .github/engine-versions.env's pins, so a
// seeded session claims exactly the version ctxloom's readers are validated
// against. An unrecognised backend deliberately gets no version, so a
// scenario that WANTS the refusal can still ask for one.
func j001000SeededEngineVersion(backend string) string {
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

// j001000SessionID is the backend-native session id a seeded entry binds. It is
// deliberately NOT the harp: that is the production shape, where a caller
// addressing a session by its native id makes the read resolve THROUGH the
// harp, and it is the shape the memory tools are exercised against everywhere
// else in this suite.
func j001000SessionID(harp string) string { return "seeded-" + harp }

// j001000AddIndexEntry appends one session to the home index.yaml, preserving
// every entry a prior call in this scenario already wrote (see j001000State's
// doc comment). project_dir is the live project so current-project listing
// finds it too, mirroring steps_fixture.go's "a recorded session" step.
func j001000AddIndexEntry(w *World, harp, backend, transcriptPath string) error {
	st := j001000From(w)
	entry := fmt.Sprintf("  - harp_name: %s\n"+
		"    session_id: %s\n"+
		"    backend: %s\n"+
		"    project_dir: %s\n"+
		"    started_at: 2026-01-01T00:00:00Z\n"+
		"    transcript_path: %q\n"+
		"    engine_version: %s\n"+
		"    summary: seeded acceptance session\n",
		harp, j001000SessionID(harp), backend, w.env.ProjectDir, transcriptPath, j001000SeededEngineVersion(backend))
	st.indexEntries = append(st.indexEntries, entry)
	st.backends[harp] = backend
	body := "sessions:\n" + strings.Join(st.indexEntries, "")
	return w.env.WriteHomeFile(".ctxloom/sessions/index.yaml", body)
}

// j001000RecallArgs builds the memory-tool arguments for harp. The engine is
// named EXPLICITLY rather than left to the project's default backend: the read
// must resolve through the canonical store for the engine the session actually
// ran on, and the four reader engines are retired-scraper backends, so a
// canonical miss is a clean "nothing captured" that triggers the conversion
// instead of a scrape attempt that would mask it.
func j001000RecallArgs(w *World, harp string) (map[string]any, error) {
	backend, ok := j001000From(w).backends[harp]
	if !ok {
		return nil, fmt.Errorf("no session was seeded for harp %q, so there is nothing to recall", harp)
	}
	return map[string]any{"session_id": j001000SessionID(harp), "backend": backend}, nil
}

// j001000CanonicalTranscriptRelPath resolves harp's captured canonical
// transcript's path relative to the isolated HOME, preferring the current
// leaf name (paths.CanonicalTranscriptFileName) but falling back to the
// pre-rename leaf — mirroring paths.ResolveHarpCanonicalTranscriptPath's own
// two-name fallback, reimplemented locally because that resolver reads the
// REAL process's home dir via os.UserHomeDir, not this scenario's isolated one
// (w.env.HomeDir): the ctxloom subprocess and this test process do not share a
// HOME.
func j001000CanonicalTranscriptRelPath(w *World, harp string) (string, error) {
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

func j001000ReadCanonicalTranscript(w *World, harp string) (string, error) {
	rel, err := j001000CanonicalTranscriptRelPath(w, harp)
	if err != nil {
		return "", err
	}
	return w.env.ReadHomeFile(rel)
}

// j001000ReadCanonicalRecords parses harp's canonical transcript into the real
// production transcript.Record type — the same schema the Recorder writes
// and every other reader (distill, resume) consumes — rather than a
// hand-rolled duplicate shape that could silently drift from it.
func j001000ReadCanonicalRecords(w *World, harp string) ([]transcript.Record, error) {
	body, err := j001000ReadCanonicalTranscript(w, harp)
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

// j001000FindEntry returns the index and payload of the first KindEntry record
// matching pred, in file order.
func j001000FindEntry(recs []transcript.Record, pred func(transcript.EntryPayload) bool) (int, *transcript.EntryPayload, bool) {
	for i, r := range recs {
		if r.Kind == transcript.KindEntry && r.Entry != nil && pred(*r.Entry) {
			return i, r.Entry, true
		}
	}
	return -1, nil, false
}

// j001000EngineTurnCheck is one real, grounded assertion about a shipped
// fixture's conversation — checked in order, each index required to be
// strictly greater than the previous one (real seq order preserved).
type j001000EngineTurnCheck struct {
	label string
	match func(transcript.EntryPayload) bool
}

// j001000EngineTurnChecks pins the Scenario Outline's per-<engine> real-turn
// assertions, grounded in each package's own shipped golden
// (internal/transcript/vendorreader/{codex,antigravity}/testdata/): codex's
// exec_command call/result/reply carry the real "HELLO_FROM_TOOL_CALL_42"
// sentinel; antigravity's carry the real "XXX-OVERWRITTEN-XXX" one. kiro is
// deliberately absent — see the feature file's own deferral note.
var j001000EngineTurnChecks = map[string][]j001000EngineTurnCheck{
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
// — so j001000RemovedPrompt below must never appear anywhere in the converted
// output. A "dequeue" IS immediately followed by a real "user" line carrying
// the same content — confirmed the same way — which is what
// j001000QueueInterleavedFixture reproduces for the delivered second prompt.
const (
	j001000SecondPrompt  = "Also, what's the capital of France?"
	j001000RemovedPrompt = "nevermind, skip that, I changed my mind"
	j001000FirstAnswer   = "6 times 7 is 42."
)

// j001000QueueInterleavedFixture builds a claude-shaped vendor transcript where:
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
func j001000QueueInterleavedFixture(sessionID string) string {
	lines := []string{
		fmt.Sprintf(`{"type":"user","sessionId":%q,"message":{"role":"user","content":"What is 6 times 7?"}}`, sessionID),
		fmt.Sprintf(`{"type":"assistant","sessionId":%q,"message":{"model":"claude-haiku-4-5-20251001","id":"msg_first","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"Computing 6 times 7."}],"stop_reason":null,"usage":{"input_tokens":5,"output_tokens":1}}}`, sessionID),
		fmt.Sprintf(`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-07-25T13:00:01.000Z","sessionId":%q,"content":%q}`, sessionID, j001000SecondPrompt),
		fmt.Sprintf(`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-07-25T13:00:02.000Z","sessionId":%q,"content":%q}`, sessionID, j001000RemovedPrompt),
		fmt.Sprintf(`{"type":"queue-operation","operation":"remove","timestamp":"2026-07-25T13:00:03.000Z","sessionId":%q,"content":%q}`, sessionID, j001000RemovedPrompt),
		fmt.Sprintf(`{"type":"queue-operation","operation":"dequeue","timestamp":"2026-07-25T13:00:04.000Z","sessionId":%q}`, sessionID),
		fmt.Sprintf(`{"type":"user","sessionId":%q,"message":{"role":"user","content":%q}}`, sessionID, j001000SecondPrompt),
		fmt.Sprintf(`{"type":"assistant","sessionId":%q,"message":{"model":"claude-haiku-4-5-20251001","id":"msg_first","type":"message","role":"assistant","content":[{"type":"text","text":%q}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":6}}}`, sessionID, j001000FirstAnswer),
		fmt.Sprintf(`{"type":"assistant","sessionId":%q,"message":{"model":"claude-haiku-4-5-20251001","id":"msg_second","type":"message","role":"assistant","content":[{"type":"text","text":"Paris is the capital of France."}],"stop_reason":"end_turn","usage":{"input_tokens":6,"output_tokens":6}}}`, sessionID),
	}
	return strings.Join(lines, "\n") + "\n"
}

func registerJ001000Steps(ctx *godog.ScenarioContext) {
	// --- Given: fixtures -----------------------------------------------

	ctx.Step(`^a recorded "([^"]*)" session "([^"]*)" bound to the shipped (\S+) vendor-transcript fixture$`,
		func(c context.Context, backend, harp, engineKey string) error {
			w := worldFrom(c)
			path, err := j001000FixturePath(engineKey)
			if err != nil {
				return err
			}
			return j001000AddIndexEntry(w, harp, backend, path)
		})

	ctx.Step(`^a recorded "([^"]*)" session "([^"]*)"$`, func(c context.Context, backend, harp string) error {
		return j001000AddIndexEntry(worldFrom(c), harp, backend, "")
	})

	ctx.Step(`^a recorded "([^"]*)" session "([^"]*)" that already has a canonical transcript and a shipped (\S+) vendor fixture$`,
		func(c context.Context, backend, harp, engineKey string) error {
			w := worldFrom(c)
			path, err := j001000FixturePath(engineKey)
			if err != nil {
				return err
			}
			if err := j001000AddIndexEntry(w, harp, backend, path); err != nil {
				return err
			}
			original := j001000SeedCanonicalTranscript(w, harp)
			j001000From(w).saved["original:"+harp] = original
			return nil
		})

	ctx.Step(`^a recorded "([^"]*)" session "([^"]*)" bound to a vendor log where a second prompt is delivered mid-response and a third is queued then removed$`,
		func(c context.Context, backend, harp string) error {
			w := worldFrom(c)
			rel := filepath.Join("j001000-fixtures", harp+".jsonl")
			if err := w.env.WriteFile(rel, j001000QueueInterleavedFixture(harp)); err != nil {
				return err
			}
			abs := filepath.Join(w.env.ProjectDir, rel)
			return j001000AddIndexEntry(w, harp, backend, abs)
		})

	// --- When: the two recall doors -------------------------------------
	//
	// recover_session is the LIVE door — the session you are sitting in,
	// whose vendor source may still be growing, so it re-reads that source
	// even when a canonical transcript already exists. load_session is the
	// ARCHIVED door — a finished session, served from whatever ctxloom
	// already owns. Both run the same conversion when nothing was captured;
	// they differ only on what to do when something was.

	ctx.Step(`^the assistant recovers the memory of session "([^"]*)"$`, func(c context.Context, harp string) error {
		args, err := j001000RecallArgs(worldFrom(c), harp)
		if err != nil {
			return err
		}
		return callTool(c, "recover_session", args)
	})

	ctx.Step(`^the assistant loads the finished session "([^"]*)"$`, func(c context.Context, harp string) error {
		args, err := j001000RecallArgs(worldFrom(c), harp)
		if err != nil {
			return err
		}
		return callTool(c, "load_session", args)
	})

	// --- Then: transcript content + attribution -------------------------

	ctx.Step(`^the canonical transcript for "([^"]*)" replays the fixture's real turns in order: the user goal, the assistant reply, and the Glob and Bash tool calls with their real inputs$`,
		func(c context.Context, harp string) error {
			w := worldFrom(c)
			recs, err := j001000ReadCanonicalRecords(w, harp)
			if err != nil {
				return err
			}
			iUser, _, ok := j001000FindEntry(recs, func(e transcript.EntryPayload) bool {
				return e.Type == "user" && strings.Contains(e.Content, "GOAL: Truthfulness audit")
			})
			if !ok {
				return fmt.Errorf("canonical transcript for %q: no user entry carries the real GOAL prompt", harp)
			}
			iAsst, _, ok := j001000FindEntry(recs, func(e transcript.EntryPayload) bool {
				return e.Type == "assistant" && strings.Contains(e.Content, "I'll conduct this truthfulness audit")
			})
			if !ok {
				return fmt.Errorf("canonical transcript for %q: no assistant entry carries the real reply text", harp)
			}
			iGlob, globEntry, ok := j001000FindEntry(recs, func(e transcript.EntryPayload) bool {
				return e.Type == "tool_use" && e.ToolName == "Glob" && strings.Contains(string(e.ToolInput), "website/src/content/docs/**/*.md")
			})
			if !ok {
				return fmt.Errorf("canonical transcript for %q: no Glob tool_use carries the real pattern input", harp)
			}
			iBash, bashEntry, ok := j001000FindEntry(recs, func(e transcript.EntryPayload) bool {
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
			st := j001000From(w)
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
			recs, err := j001000ReadCanonicalRecords(w, harp)
			if err != nil {
				return err
			}
			_, result, ok := j001000FindEntry(recs, func(e transcript.EntryPayload) bool {
				return e.Type == "tool_result" && e.IsError && strings.Contains(e.ToolOutput, "The user doesn't want to proceed with this tool use")
			})
			if !ok {
				return fmt.Errorf("canonical transcript for %q: no is_error tool_result carries the real rejection text", harp)
			}
			st := j001000From(w)
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
			recs, err := j001000ReadCanonicalRecords(w, harp)
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
			recs, err := j001000ReadCanonicalRecords(w, harp)
			if err != nil {
				return err
			}
			checks, ok := j001000EngineTurnChecks[engine]
			if !ok {
				return fmt.Errorf("no known real-turn assertions for engine %q", engine)
			}
			last := -1
			for _, chk := range checks {
				idx, _, found := j001000FindEntry(recs, chk.match)
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

	// --- Never doubled ---------------------------------------------------
	//
	// The invariant is STRUCTURAL, not byte-for-byte. Every conversion stamps
	// its own record timestamps, so demanding identical bytes would demand
	// something the design never promises. What a second copy bolted onto the
	// first WOULD produce is a doubled record count and a seq that restarts
	// partway down the file, and both are caught below.

	ctx.Step(`^I note the conversation the canonical transcript for "([^"]*)" holds$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		recs, err := j001000ReadCanonicalRecords(w, harp)
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			return fmt.Errorf("canonical transcript for %q holds no records, so noting it would measure nothing", harp)
		}
		seqs := make([]int, 0, len(recs))
		for _, r := range recs {
			seqs = append(seqs, r.Seq)
		}
		j001000From(w).noted[harp] = seqs
		return nil
	})

	ctx.Step(`^the canonical transcript for "([^"]*)" still holds exactly that conversation, not a second copy of it$`,
		func(c context.Context, harp string) error {
			w := worldFrom(c)
			want, ok := j001000From(w).noted[harp]
			if !ok {
				return fmt.Errorf("nothing was noted about %q's canonical transcript, so this assertion measured nothing", harp)
			}
			recs, err := j001000ReadCanonicalRecords(w, harp)
			if err != nil {
				return err
			}
			if len(recs) != len(want) {
				return fmt.Errorf("canonical transcript for %q holds %d records after a second recall, not the %d it held before — "+
					"a re-read that appends instead of replacing leaves the whole conversation in the file twice", harp, len(recs), len(want))
			}
			prev := -1
			for i, r := range recs {
				if r.Seq <= prev {
					return fmt.Errorf("canonical transcript for %q: record %d carries seq %d after seq %d — the sequence restarts partway down the "+
						"file, which is what a second copy appended onto the first looks like", harp, i, r.Seq, prev)
				}
				prev = r.Seq
			}
			return nil
		})

	ctx.Step(`^the canonical transcript for "([^"]*)" still holds its original captured content$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		want, ok := j001000From(w).saved["original:"+harp]
		if !ok {
			return fmt.Errorf("no original canonical transcript was seeded for %q", harp)
		}
		got, err := j001000ReadCanonicalTranscript(w, harp)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("canonical transcript for %q was rewritten from the engine's own copy (expected untouched):\n--- original ---\n%s\n--- now ---\n%s", harp, want, got)
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
			recs, err := j001000ReadCanonicalRecords(w, harp)
			if err != nil {
				return err
			}
			iSecondPrompt, _, ok := j001000FindEntry(recs, func(e transcript.EntryPayload) bool {
				return e.Type == "user" && strings.Contains(e.Content, j001000SecondPrompt)
			})
			if !ok {
				return fmt.Errorf("canonical transcript for %q: the interleaved second prompt never arrived", harp)
			}
			iAnswer, _, ok := j001000FindEntry(recs, func(e transcript.EntryPayload) bool {
				return e.Type == "assistant" && strings.Contains(e.Content, j001000FirstAnswer)
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
			body, err := j001000ReadCanonicalTranscript(w, harp)
			if err != nil {
				return err
			}
			if strings.Contains(body, j001000RemovedPrompt) {
				return fmt.Errorf("canonical transcript for %q leaked the removed, never-delivered prompt %q into a captured turn", harp, j001000RemovedPrompt)
			}
			return nil
		})
}

// j001000SeedCanonicalTranscript writes a minimal, realistic already-captured
// canonical transcript for harp (one real transcript.Record, marshaled
// through the production type) so the archived read finds a captured session
// and never re-derives it from the engine's own copy. Returns the exact bytes
// written, so the caller can assert byte-for-byte non-modification later.
func j001000SeedCanonicalTranscript(w *World, harp string) string {
	rec := transcript.Record{
		V:      transcript.SchemaVersion,
		Harp:   harp,
		Engine: "claude-code",
		Seq:    0,
		Kind:   transcript.KindEntry,
		Entry: &transcript.EntryPayload{
			Type:    "user",
			Content: "ORIGINAL CAPTURED CONTENT for " + harp + " — a vendor read must never overwrite this",
		},
	}
	data, err := json.Marshal(rec)
	if err != nil {
		// A marshal failure here is a bug in this fixture builder, not
		// something a scenario should silently limp past.
		panic(fmt.Sprintf("j001000SeedCanonicalTranscript: marshal record: %v", err))
	}
	line := string(data) + "\n"
	rel := ".ctxloom/sessions/" + harp + "/persist/transcript.jsonl"
	if err := w.env.WriteHomeFile(rel, line); err != nil {
		panic(fmt.Sprintf("j001000SeedCanonicalTranscript: write %s: %v", rel, err))
	}
	return line
}
