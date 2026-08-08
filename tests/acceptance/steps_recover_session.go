//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/transcript"
)

// registerRecoverSessionSteps wires the fixture/assertion steps for the
// quit-eagle flow-level regression: recover_session, driven through the REAL
// MCP surface, against a session large enough to have triggered the
// ~381,000-char blowup the original bug report described, must come back
// bounded rather than passing an uncompressed distillation through raw. See
// tests/acceptance/features/mcp_tools.feature's "recover_session bounds..."
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
	// tries id-as-harp FIRST (early-crane), so this resolves directly and, on
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
		// memory.MaxEssenceChars, and — at the compactor's default 8000-token
		// (32,000-char) chunk size — spans enough chunks to exercise the real
		// multi-chunk map/reduce pipeline, not a single-chunk degenerate case.
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
	// (seeded-<harp>, per j12AddIndexEntry) that is NOT the harp, so a caller
	// addressing the session by that id makes recover_session resolve THROUGH
	// the harp. Every downstream key is chosen from what that resolution
	// returns, which is exactly where the identity defect lived.
	//
	// The transcript is small and real rather than synthetic-and-huge: this
	// scenario is about identity surviving the round trip, not about bounding.
	ctx.Step(`^a captured session "([^"]*)" bound to a backend-native session id$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		transcriptPath := w.env.HomeDir + "/" + j12HarpHome(harp) + "/persist/transcript.jsonl"
		if err := j12AddIndexEntry(w, harp, "seeded production-shape session", transcriptPath); err != nil {
			return fmt.Errorf("seed index entry for %s: %w", harp, err)
		}
		// Two turns, not zero: Compact short-circuits an empty session to a
		// placeholder dump with no LLM call, which would let this pass without
		// a real distillation ever happening.
		return j12WriteCanonicalTranscript(w, harp, []string{
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
}

// syntheticCanonicalTranscript builds a valid transcript.jsonl document (one
// JSON Record per line, matching internal/transcript's real on-disk schema —
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
