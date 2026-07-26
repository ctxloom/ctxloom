//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// registerRecoverSessionSteps wires the fixture/assertion steps for the
// quit-eagle flow-level regression: recover_session, driven through the REAL
// MCP surface, against a session large enough to have triggered the
// ~381,000-char blowup the original bug report described, must come back
// bounded rather than passing the (mocked) oversized distillation through
// raw. See tests/acceptance/features/mcp_tools.feature's "recover_session
// bounds..." scenario.
func registerRecoverSessionSteps(ctx *godog.ScenarioContext) {
	// A SYNTHETIC canonical transcript — no real session content — sized and
	// shaped (session/entry/complete kind mix, alternating user/assistant/
	// tool_use/tool_result entries) to resemble a genuine captured session
	// without reproducing one. Written straight to the harp's own persist dir
	// so recover_session's canonical-first resolution (CanonicalFallbackSource,
	// harp-first) finds it directly by harp name — no session-index entry
	// needed.
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

	// The mock LLM's fixed response is deliberately over the bound: every map
	// call AND the reduce call return this same oversized text (mock output is
	// call-site-independent), so this reproduces "the pipeline ran, every LLM
	// call exited 0, but the model never actually compressed enough" —
	// the general case the compactor's final MaxEssenceChars check (not just
	// the reduce-failure fallback) exists to catch.
	ctx.Step(`^the mock LLM responds with an oversized distillation$`, func(c context.Context) error {
		w := worldFrom(c)
		mock, err := w.env.SetupMockLM()
		if err != nil {
			return fmt.Errorf("setup mock LLM: %w", err)
		}
		oversized := strings.Repeat("z", memory.MaxEssenceChars+1)
		if err := mock.SetResponse(oversized); err != nil {
			return fmt.Errorf("set mock response: %w", err)
		}
		w.mock = mock
		return nil
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
