//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// registerRecoverSessionSteps wires the fixture/assertion steps for the
// quit-eagle flow-level regression: recover_session, driven through the REAL
// MCP surface, against a session large enough to have triggered the
// ~381,000-char blowup the original bug report described, must come back
// bounded rather than passing an uncompressed distillation through raw. See
// tests/acceptance/features/cli/mcp_tools.feature's "recover_session bounds..."
// scenario.
//
// The mock backend's DEFAULT response (no custom CTXLOOM_MOCK_RESPONSE) is an
// echo of the prompt it was sent (internal/lm/backends/mock.go's
// buildMockResponse: "[mock] prompt=%s" with the full promptContent, verbatim)
// — i.e. it "compresses" by not compressing at all, exit 0 every time. That is
// exactly quit-eagle's shape: a pipeline that behaves (no errors) but never
// actually shrinks the content. A large-enough seeded transcript reproduces
// the original bug's scale on its own, with no need to hand-craft an
// oversized canned response.
// recoverIdentityMarker appears ONLY in the production-shape scenario's seeded
// transcript, so finding it in the recovered essence proves real content made
// the round trip rather than an empty or placeholder result.
const recoverIdentityMarker = "RECOVER-IDENTITY-ROUND-TRIP"

func registerRecoverSessionSteps(ctx *godog.ScenarioContext) {
	// A SYNTHETIC canonical transcript — no real session content — sized and
	// shaped (session/entry/complete kind mix, alternating user/assistant/
	// tool_use/tool_result entries) to resemble a genuine captured session
	// without reproducing one. Written straight to the harp's own persist dir
	// — no session-index entry. The scenario calls recover_session with
	// session_id SET TO THE HARP ITSELF: CanonicalFallbackSource.GetSession
	// tries id-as-harp FIRST, so this resolves directly and, on
	// the save side, agent.Session.ID ends up equal to the harp too — keeping
	// the essence's save path (session.ID-keyed) and this handler's later
	// read-back path (the ORIGINAL sessionID argument) the SAME string.
	// Going through a session-index-bound backend-native id instead (the
	// production shape) hits a real, separate pre-existing mismatch — the
	// canonical reverse-lookup path resolves to the session via its HARP, so
	// the loaded agent.Session.ID becomes the harp, while the caller's
	// original (UUID) sessionID is what the post-distill read-back
	// (LoadDistilledSession) still keys on — a legitimate bug, but a
	// read-path one, out of this task's scope (bounding + fail-loud only;
	// filed separately, not fixed here).
	ctx.Step(`^a captured session "([^"]*)" with a large canonical transcript$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		// 300,000 chars of entry content is comfortably past both the
		// original ~381,000-CHAR blowup's scale (once JSON/envelope overhead
		// is stripped, this is genuinely large session TEXT, not padding) and
		// memory.MaxEssenceChars, so a distiller that does not compress
		// produces an essence over the bound and must be refused.
		content := syntheticCanonicalTranscript(harp, 300_000)
		return w.env.WriteHomeFile(".ctxloom/sessions/"+harp+"/persist/transcript.jsonl", content)
	})

	// Makes the mock backend the compaction LLM (llm.defaults.primary: mock in
	// config.yaml) so distillation runs hermetically — no real credentials, no
	// network — via the same SetupMockLM() fixture the rest of the suite uses.
	// Deliberately does NOT call SetResponse: the default echo response is the
	// point (see the doc comment above).
	ctx.Step(`^the compaction LLM is a mock that never compresses$`, func(c context.Context) error {
		w := worldFrom(c)
		mock, err := w.env.SetupMockLM()
		if err != nil {
			return fmt.Errorf("setup mock LLM: %w", err)
		}
		w.mock = mock
		return nil
	})

	// The PRODUCTION identity shape, and the one the large-transcript scenario
	// above deliberately avoids: the session index binds a backend-native id
	// (seeded-<harp>, per j001200AddIndexEntry) that is NOT the harp, so a caller
	// addressing the session by that id makes recover_session resolve THROUGH
	// the harp. Every downstream key is chosen from what that resolution
	// returns, which is exactly where the identity defect lived.
	//
	// The transcript is small and real rather than synthetic-and-huge: this
	// scenario is about identity surviving the round trip, not about bounding.
	ctx.Step(`^a captured session "([^"]*)" bound to a backend-native session id$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		// Binds the VENDOR transcript, not the canonical one: mock is a real
		// engine with a real vendor reader, so the index must name the file
		// ctxloom would actually convert (see j001200VendorTranscriptPath).
		transcriptPath := j001200VendorTranscriptPath(w, harp)
		if err := j001200AddIndexEntry(w, harp, "seeded production-shape session", transcriptPath); err != nil {
			return fmt.Errorf("seed index entry for %s: %w", harp, err)
		}
		// Two turns, not zero: Compact short-circuits an empty session to a
		// placeholder dump with no LLM call, which would let this pass without
		// a real distillation ever happening.
		return j001200SeedTranscripts(w, harp, []string{
			"What broke the essence read-back? " + recoverIdentityMarker,
			"The write key and the read key disagreed. " + recoverIdentityMarker,
		})
	})

	ctx.Step(`^the tool result is under (\d+) bytes$`, func(c context.Context, max int) error {
		w := worldFrom(c)
		got := len(w.lastTool.JSON())
		if got >= max {
			return fmt.Errorf("tool result is %d bytes, want under %d; result:\n%s", got, max, w.lastTool.JSON())
		}
		return nil
	})

	// The BARE-CALL fixture (finite-parabola): a self harp that has just been
	// /cleared, set up so recover_session's NO-session_id branch has a lineage
	// to resolve against. handleRecoverSession reads its own identity from
	// CTXLOOM_SESSION_HARP (via selfIdentityFromEnv), then resolves the target
	// from that harp's lineage — the per-binding engine-transcript links under
	// its session dir (operations.HarpTranscripts) — skipping whichever id the
	// index currently binds. So the shape this must build, matching what a real
	// /clear leaves on disk, is:
	//
	//   - an index entry for the harp whose CURRENT session_id is the post-clear
	//     one (the id the resolver must SKIP), with the pre-clear session in
	//     Rotations (so the pre-clear id reverse-resolves back to this harp);
	//   - a mock vendor transcript per binding, each carrying a distinct marker;
	//   - one engine-transcript link per binding, named for its session id, so
	//     the lineage scan discovers both — the pre-clear one is the target.
	//
	// The canonical transcript is NOT hand-written: recover's live refresh
	// (RefreshVendorTranscript) rebuilds it from the rotation + live vendor
	// files, which is the production path and the one that would silently serve
	// nothing if the rebuild leg were broken. The link target's BASE NAME is
	// what HarpTranscripts reads as the session id, so each vendor file is named
	// <session-id>.jsonl and the link points straight at it.
	ctx.Step(`^a cleared session "([^"]*)" whose prior thread is in its lineage$`, func(c context.Context, harp string) error {
		return seedClearedHarpLineage(worldFrom(c), harp)
	})
}

// bareRecoverPreclearID / bareRecoverPostclearID are the two backend-native
// session ids the cleared-harp fixture binds: the pre-clear thread the bare
// recover must return, and the post-clear session it must skip. They are
// asserted on directly in the feature (session_id equals "sess-preclear"), so
// they are the contract with that scenario and live here beside the fixture
// that writes them.
const (
	bareRecoverPreclearID  = "sess-preclear"
	bareRecoverPostclearID = "sess-postclear"
	// bareRecoverPreclearMarker rides ONLY in the pre-clear vendor transcript,
	// so finding it in the recovered essence proves the pre-clear thread's real
	// bytes made the round trip rather than an empty or placeholder result.
	bareRecoverPreclearMarker = "PRECLEAR-THREAD-MARKER"
)

// seedClearedHarpLineage writes the on-disk state a /cleared session leaves
// behind for harp: a two-binding index entry (post-clear current, pre-clear
// rotated), a mock vendor transcript per binding, and one engine-transcript
// link per binding so operations.HarpTranscripts discovers the lineage. It
// also points CTXLOOM_SESSION_HARP at harp so the MCP server adopts it as its
// own identity — the same door through the ambient-session scrub the session
// hooks use (see steps_session_hooks.go).
func seedClearedHarpLineage(w *World, harp string) error {
	// SetChildEnv, not SetEnv: CTXLOOM_SESSION_HARP is on the ambient-session
	// scrub list, so a plain SetEnv would be stripped before the MCP server
	// child ever saw it (see steps_session_hooks.go). This is the deliberate
	// door through the scrub — the value is one the scenario chose.
	w.env.SetChildEnv("CTXLOOM_SESSION_HARP", harp)

	harpDir := ".ctxloom/sessions/" + harp
	preclearVendorRel := harpDir + "/vendor/" + bareRecoverPreclearID + ".jsonl"
	postclearVendorRel := harpDir + "/vendor/" + bareRecoverPostclearID + ".jsonl"

	// The pre-clear thread carries the marker the scenario asserts on; the
	// post-clear (current) session carries its own, so a resolver that failed
	// to skip it would surface the wrong marker AND the wrong session_id.
	if err := writeMockVendorTranscript(w, preclearVendorRel, []string{
		"Trace the flaky capture-integrity test to its root. " + bareRecoverPreclearMarker,
		"The tee raced the pty close; the fix is to drain before close. " + bareRecoverPreclearMarker,
	}); err != nil {
		return err
	}
	if err := writeMockVendorTranscript(w, postclearVendorRel, []string{
		"POSTCLEAR-CURRENT-SESSION starting fresh after the clear.",
		"POSTCLEAR-CURRENT-SESSION nothing concluded yet.",
	}); err != nil {
		return err
	}

	preclearVendorAbs := filepath.Join(w.env.HomeDir, preclearVendorRel)
	postclearVendorAbs := filepath.Join(w.env.HomeDir, postclearVendorRel)

	// The index entry: current binding is the POST-clear id (the exclusion),
	// the PRE-clear id is a rotation (so it reverse-resolves to this harp).
	rotatedAt := time.Date(2026, 3, 14, 1, 0, 0, 0, time.UTC).Format(time.RFC3339)
	index := fmt.Sprintf("sessions:\n"+
		"  - harp_name: %s\n"+
		"    session_id: %s\n"+
		"    backend: %s\n"+
		"    engine_version: %s\n"+
		"    project_dir: %s\n"+
		"    started_at: 2026-03-14T00:00:00Z\n"+
		"    transcript_path: %q\n"+
		"    rotations:\n"+
		"      - session_id: %s\n"+
		"        transcript_path: %q\n"+
		"        rotated_at: %s\n",
		harp, bareRecoverPostclearID, config.BackendMock,
		j001000SeededEngineVersion(config.BackendMock), w.env.ProjectDir,
		postclearVendorAbs, bareRecoverPreclearID, preclearVendorAbs, rotatedAt)
	if err := w.env.WriteHomeFile(".ctxloom/sessions/index.yaml", index); err != nil {
		return err
	}

	// One engine-transcript link per binding, at the harp dir's root, each
	// pointing at its vendor file. HarpTranscripts reads the link TARGET's base
	// name as the session id, so the target must be named <session-id>.jsonl —
	// which is exactly how these vendor files are named above. The prefix is
	// taken from the production constant so a rename of the scheme reaches this
	// fixture too.
	if err := linkEngineTranscript(w, harp, bareRecoverPreclearID, preclearVendorAbs); err != nil {
		return err
	}
	if err := linkEngineTranscript(w, harp, bareRecoverPostclearID, postclearVendorAbs); err != nil {
		return err
	}

	// The post-clear session must sort NEWEST in the lineage so that a resolver
	// which stopped skipping the current binding would return IT (reddening the
	// session_id assertion) rather than the pre-clear one by accident of mtime.
	// HarpTranscripts stats the link TARGET, so it is the vendor files' mtimes
	// that order the lineage.
	preTime := time.Date(2026, 3, 14, 0, 30, 0, 0, time.UTC)
	postTime := time.Date(2026, 3, 14, 1, 30, 0, 0, time.UTC)
	if err := os.Chtimes(preclearVendorAbs, preTime, preTime); err != nil {
		return fmt.Errorf("set pre-clear vendor mtime: %w", err)
	}
	if err := os.Chtimes(postclearVendorAbs, postTime, postTime); err != nil {
		return fmt.Errorf("set post-clear vendor mtime: %w", err)
	}
	return nil
}

// writeMockVendorTranscript writes turns to relPath under the fake home in the
// mock vendor format ({"role","text","ts"} per line — the one
// internal/adapters/transcript/vendorreader/mock parses), alternating user/assistant so
// every line converts to an entry (the adapter's checkFloor refuses a file that
// yields none). Unlike j001200WriteMockVendorTranscript this takes an explicit
// path, because the lineage fixture needs two vendor files named for their
// session ids rather than the single fixed vendor/mock-session.jsonl.
func writeMockVendorTranscript(w *World, relPath string, turns []string) error {
	var b strings.Builder
	ts := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	for i, text := range turns {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		line, err := json.Marshal(map[string]any{
			"role": role,
			"text": text,
			"ts":   ts.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
		})
		if err != nil {
			return fmt.Errorf("render mock vendor line %d: %w", i, err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return w.env.WriteHomeFile(relPath, b.String())
}

// linkEngineTranscript creates the per-binding engine-transcript symlink the
// lineage scan reads — <harp dir>/engine-transcript-<mock>-<id>.jsonl pointing
// at targetAbs — mirroring what sessions.linkEngineTranscript writes in
// production. The leaf prefix comes from paths.EngineTranscriptLinkPrefix so
// the fixture cannot drift from the scheme HarpTranscripts filters on.
func linkEngineTranscript(w *World, harp, sessionID, targetAbs string) error {
	linkName := paths.EngineTranscriptLinkPrefix + config.BackendMock + "-" + sessionID + ".jsonl"
	linkAbs := filepath.Join(w.env.HomeDir, ".ctxloom", "sessions", harp, linkName)
	if err := os.MkdirAll(filepath.Dir(linkAbs), 0o755); err != nil {
		return fmt.Errorf("create harp dir for link: %w", err)
	}
	if err := os.Symlink(targetAbs, linkAbs); err != nil {
		return fmt.Errorf("link engine transcript %s: %w", linkName, err)
	}
	return nil
}

// syntheticCanonicalTranscript builds a valid transcript.jsonl document (one
// JSON Record per line, matching internal/adapters/transcript's real on-disk schema —
// constructed via its own exported types, not hand-written JSON strings, so
// it can never drift from what CanonicalHistory actually parses) for harp,
// repeating a user/assistant/tool_use/tool_result/complete turn until at
// least targetChars of entry content has been written. The kind mix
// (one session record; entry records dominated by tool_use/tool_result/
// assistant with fewer user turns; a complete marker per turn) mirrors the
// proportions of a genuine long-running captured session without containing
// any real content.
func syntheticCanonicalTranscript(harp string, targetChars int) string {
	var b strings.Builder
	seq := 0
	write := func(rec transcript.Record) {
		rec.V = transcript.SchemaVersion
		rec.Harp = harp
		rec.Engine = "claude-code"
		rec.Seq = seq
		rec.TS = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(seq) * time.Second)
		seq++
		data, err := json.Marshal(rec)
		if err != nil {
			panic(fmt.Sprintf("marshal synthetic transcript record: %v", err)) // fixture bug, not a scenario failure
		}
		b.Write(data)
		b.WriteByte('\n')
	}

	write(transcript.Record{
		Kind:    transcript.KindSession,
		Session: &transcript.SessionPayload{Model: "claude-opus-5", PermissionMode: "bypassPermissions"},
	})

	const userLine = "Investigate the flaky test in this package and figure out the root cause; keep going until it's fixed."
	assistantChunk := strings.Repeat("Traced through the relevant code and the failure path it takes. ", 20)
	toolInput := json.RawMessage(`{"path":"internal/example/file.go","pattern":"TODO"}`)
	toolOutput := strings.Repeat("internal/example/file.go:42: a matched line of output\n", 10)

	written := 0
	for written < targetChars {
		write(transcript.Record{Kind: transcript.KindEntry, Entry: &transcript.EntryPayload{Type: "user", Content: userLine}})
		write(transcript.Record{Kind: transcript.KindEntry, Entry: &transcript.EntryPayload{Type: "assistant", Content: assistantChunk}})
		write(transcript.Record{Kind: transcript.KindEntry, Entry: &transcript.EntryPayload{Type: "tool_use", ToolName: "grep", ToolInput: toolInput}})
		write(transcript.Record{Kind: transcript.KindEntry, Entry: &transcript.EntryPayload{Type: "tool_result", ToolName: "grep", ToolOutput: toolOutput}})
		write(transcript.Record{Kind: transcript.KindComplete, Complete: &transcript.CompletePayload{}})
		written += len(userLine) + len(assistantChunk) + len(toolInput) + len(toolOutput)
	}
	return b.String()
}
