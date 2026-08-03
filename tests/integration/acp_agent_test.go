//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestACPAgent_SelfConformance drives `ctxloom acp` (the AGENT half) with
// ctxloom's OWN ACP client driver (the half that drives kiro/claude/codex) —
// both ends of the protocol conformance-tested against each other over a real
// subprocess boundary, fully hermetic via the mock engine: outer driver →
// `ctxloom acp serve` → plugin (mock) chat → echo → session/update → outer
// driver.
// The mock's echo proves exactly what the server delivered to the engine.
func TestACPAgent_SelfConformance(t *testing.T) {
	env := setupTestEnv(t)
	_, err := env.SetupMockLM()
	require.NoError(t, err)

	drv := acp.NewChatDriver(acp.ACPConfig{Command: env.AppBinary + " acp serve"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	in := make(chan agent.ChatMessage, 2)
	in <- agent.ChatMessage{Text: "conformance ping"}
	in <- agent.ChatMessage{Text: "second turn"}
	close(in)
	out := make(chan agent.ChatEvent, 64)

	done := make(chan error, 1)
	go func() {
		done <- drv.Chat(ctx, agent.ChatRequest{WorkDir: env.ProjectDir}, in, out)
	}()

	var texts []string
	completes := 0
	for ev := range out {
		switch {
		case ev.Entry != nil && ev.Entry.Type == agent.EntryTypeAssistant:
			texts = append(texts, ev.Entry.Content)
		case ev.Complete != nil:
			completes++
		}
	}
	require.NoError(t, <-done, "the loopback ACP conversation must complete cleanly")

	// ISO3/ISO4: every session now opens with the always-on session
	// initialization summary (internal/operations/engine_session.go's
	// buildSessionInitSummary), delivered as a session/update notification
	// right after session/new —
	// the outer driver has no wire-level way to tell it apart from real
	// engine content (that's the whole point: no ACP method lets ctxloom
	// mark a message "system"), so it arrives here as just another assistant
	// text entry ahead of the two real turns. See
	// TestACPAgent_AnnouncementArrivesAtConnect_NoPromptSent below for the
	// STRONGER proof that it needs no prompt at all — this test only proves
	// it survives alongside real turns, in order.
	require.Len(t, texts, 3, "the ISO3 posture announcement, then one assistant entry per turn")
	assert.Contains(t, texts[0], "ctxloom:", "the first entry is the always-on isolation-posture announcement")
	assert.Contains(t, texts[1], "mock chat: ", "the mock engine's echo came back through both protocol halves")
	assert.Contains(t, texts[1], "conformance ping")
	assert.Equal(t, "mock chat: second turn", texts[2], "the second turn carries no context prefix")
	assert.Equal(t, 2, completes, "one completion marker per turn")
}

// TestACPAgent_AnnouncementArrivesAtConnect_NoPromptSent is the headline
// proof for moving the ISO3 posture announcement off the per-turn Events
// channel and onto a session/new-time session/update notification: it opens
// a real `ctxloom acp` subprocess, closes the input channel WITHOUT EVER
// SENDING A PROMPT, and asserts the announcement still arrives.
//
// Before this slice this was impossible to observe passing for the right
// reason: the old mechanism (announceOnFirstEvent) spliced the announcement
// onto the engine's Events channel, which acpagent's server only ever drains
// from inside runTurn — the code path session/prompt starts. With zero
// prompts sent, runTurn never runs, so the old server would have delivered
// nothing at all here and this test would time out / see an empty texts
// slice. Seeing the announcement land with session/prompt never even
// invoked is the proof that delivery now happens at connect (session/new),
// not gated behind a turn.
func TestACPAgent_AnnouncementArrivesAtConnect_NoPromptSent(t *testing.T) {
	env := setupTestEnv(t)
	_, err := env.SetupMockLM()
	require.NoError(t, err)

	drv := acp.NewChatDriver(acp.ACPConfig{Command: env.AppBinary + " acp serve"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// No message ever written to `in` — closed immediately, so
	// session/prompt is NEVER called on this connection.
	in := make(chan agent.ChatMessage)
	close(in)
	out := make(chan agent.ChatEvent, 64)

	done := make(chan error, 1)
	go func() {
		done <- drv.Chat(ctx, agent.ChatRequest{WorkDir: env.ProjectDir}, in, out)
	}()

	var texts []string
	for ev := range out {
		if ev.Entry != nil && ev.Entry.Type == agent.EntryTypeAssistant {
			texts = append(texts, ev.Entry.Content)
		}
	}
	require.NoError(t, <-done, "the conversation completes cleanly even though session/prompt was never called")

	require.Len(t, texts, 1, "the posture announcement — and ONLY the announcement — arrives, despite no prompt ever being sent")
	assert.Contains(t, texts[0], "ctxloom:", "delivered at connect (session/new), not gated behind a turn")
}

// TestACPAgent_InitSummaryRealMultiFieldOverSubprocess is the integration
// counterpart to internal/operations' unit-level buildSessionInitSummary
// proofs: it drives a REAL `ctxloom acp --agent <name>` subprocess (real
// binary, real config.yaml/profile/bundle on disk, real ACP wire protocol)
// and asserts the connect-time summary names several REAL, resolved facts at
// once — the bound agent + its engine, the configured model, the composed
// profile, the profile's own bundle fragment, and ctxloom's auto-registered
// MCP server — rather than merely checking the generic "ctxloom:" prefix
// every other test here settles for. This is the "at least one integration
// test asserts a REAL multi-field summary, not just presence" proof.
func TestACPAgent_InitSummaryRealMultiFieldOverSubprocess(t *testing.T) {
	env := setupTestEnv(t)
	_, err := env.SetupMockLM()
	require.NoError(t, err)

	writeFragment(t, env, "onboarding", []string{"onboarding"}, "Onboarding fragment content.")
	writeProfile(t, env, "reviewer-profile", `name: reviewer-profile
description: Reviewer profile
bundles:
  - local#fragments/onboarding
`)

	// SetupMockLM already wrote a working `llm.configs.mock` entry (record
	// file + response wiring) plus `llm.defaults.primary: mock` — extend that
	// SAME file (not replace it) with a real `model:` value and an agent
	// binding to the profile above, so the summary has real, non-default
	// values to report for BOTH fields at once.
	cfgPath := ".ctxloom/config.yaml"
	existing, rerr := env.ReadFile(cfgPath)
	require.NoError(t, rerr)
	existing = strings.Replace(existing, "type: mock\n", "type: mock\n      model: mock-model-v1\n", 1)
	existing += "agents:\n  reviewer:\n    engine: mock\n    profiles: [reviewer-profile]\n"
	require.NoError(t, env.WriteFile(cfgPath, existing))

	drv := acp.NewChatDriver(acp.ACPConfig{Command: env.AppBinary + " acp serve --agent reviewer"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	in := make(chan agent.ChatMessage, 1)
	in <- agent.ChatMessage{Text: "hello"}
	close(in)
	out := make(chan agent.ChatEvent, 64)

	done := make(chan error, 1)
	go func() {
		done <- drv.Chat(ctx, agent.ChatRequest{WorkDir: env.ProjectDir}, in, out)
	}()

	var texts []string
	for ev := range out {
		if ev.Entry != nil && ev.Entry.Type == agent.EntryTypeAssistant {
			texts = append(texts, ev.Entry.Content)
		}
	}
	require.NoError(t, <-done)
	require.Len(t, texts, 2, "the init summary, then the one real turn")

	summary := texts[0]
	assert.Contains(t, summary, `agent     : agent "reviewer" (engine mock)`, "the REAL bound agent + its engine")
	assert.Contains(t, summary, "model     : mock-model-v1", "the REAL configured model, not the generic default text")
	assert.Contains(t, summary, "profiles  : [reviewer-profile]", "the REAL composed profile")
	// The fragments/mcp lines are asserted by extracting the ONE real line
	// (rather than the whole summary substring) because this dev host also
	// has ltk/taskloom companions on PATH, which auto-inject their OWN
	// builtin fragments/MCP server alongside the profile's — real,
	// environment-dependent facts this test must not assume away, but the
	// profile's own contribution must still be present among them.
	assert.Contains(t, summaryFieldLine(t, summary, "fragments"), "local#fragments/onboarding",
		"the REAL fragment the profile's own bundle contributed, among whatever else this host auto-injects")
	assert.Contains(t, summaryFieldLine(t, summary, "mcp"), "ctxloom",
		"ctxloom's own auto-registered MCP server, a real resolved fact this project never disabled")
	assert.Contains(t, texts[1], "hello", "the real turn still runs after the summary")
}

// summaryFieldLine returns the single "  <label>" line from a rendered
// session init summary (buildSessionInitSummary's fixed field-per-line
// shape), failing the test if the field is missing — a focused assertion
// surface so a field's line can be checked without the whole summary text
// needing to match verbatim.
func summaryFieldLine(t *testing.T, summary, label string) string {
	t.Helper()
	for _, line := range strings.Split(summary, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), label) {
			return line
		}
	}
	t.Fatalf("summary has no %q field line:\n%s", label, summary)
	return ""
}

// TestACPAgent_PermissionPassThrough drives the WHOLE permission loop over
// real process + plugin boundaries: the mock engine raises a scripted
// permission request → `ctxloom acp` forwards it to its ACP client (the outer
// driver) as session/request_permission → the driver decides (allow under
// PermissionBypass, reject otherwise — agent.PermissionMode.AllowsWithoutPrompt,
// the generalized replacement for the old AutoApprove bool) → the decision
// rides back down and the mock echoes the verdict.
func TestACPAgent_PermissionPassThrough(t *testing.T) {
	env := setupTestEnv(t)
	_, err := env.SetupMockLM()
	require.NoError(t, err)

	cases := []struct {
		name  string
		perms agent.PermissionMode
		want  string
	}{
		{"approved", agent.PermissionBypass, "mock chat: permission granted"},
		{"rejected", agent.PermissionDefault, "mock chat: permission denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drv := acp.NewChatDriver(acp.ACPConfig{Command: env.AppBinary + " acp serve"})

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			in := make(chan agent.ChatMessage, 1)
			in <- agent.ChatMessage{Text: "PERMISSION check"}
			close(in)
			out := make(chan agent.ChatEvent, 64)

			done := make(chan error, 1)
			go func() {
				done <- drv.Chat(ctx, agent.ChatRequest{WorkDir: env.ProjectDir, Permissions: tc.perms}, in, out)
			}()

			var texts []string
			for ev := range out {
				if ev.Entry != nil && ev.Entry.Type == agent.EntryTypeAssistant {
					texts = append(texts, ev.Entry.Content)
				}
			}
			require.NoError(t, <-done)
			// ISO3: the always-on posture announcement is now the first
			// assistant-shaped entry on every session — see
			// TestACPAgent_SelfConformance's comment for why the outer driver
			// can't tell it apart from real content.
			require.Len(t, texts, 2)
			assert.Contains(t, texts[0], "ctxloom:", "the first entry is the always-on isolation-posture announcement")
			assert.Equal(t, tc.want, texts[1], "the driver's decision must round-trip to the engine")
		})
	}
}

// TestACPAgent_PerTurnCancel proves the per-turn cancel seam across the whole
// stack: the mock engine parks on a HANG turn, the outer driver's CancelTurn
// message becomes session/cancel → the agent cancels only the TURN (stopReason
// cancelled), and the SAME session then answers a further turn.
func TestACPAgent_PerTurnCancel(t *testing.T) {
	env := setupTestEnv(t)
	_, err := env.SetupMockLM()
	require.NoError(t, err)

	drv := acp.NewChatDriver(acp.ACPConfig{Command: env.AppBinary + " acp serve"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	in := make(chan agent.ChatMessage, 4)
	out := make(chan agent.ChatEvent, 64)
	done := make(chan error, 1)
	go func() {
		done <- drv.Chat(ctx, agent.ChatRequest{WorkDir: env.ProjectDir}, in, out)
	}()

	in <- agent.ChatMessage{Text: "HANG until cancelled"}
	in <- agent.ChatMessage{CancelTurn: true}
	in <- agent.ChatMessage{Text: "still alive?"}
	close(in)

	var stops, texts []string
	for ev := range out {
		switch {
		case ev.Entry != nil && ev.Entry.Type == agent.EntryTypeAssistant:
			texts = append(texts, ev.Entry.Content)
		case ev.Complete != nil:
			stops = append(stops, ev.Complete.StopReason)
		}
	}
	require.NoError(t, <-done)

	require.Len(t, stops, 2, "the cancelled turn and the follow-up both complete")
	assert.Equal(t, "cancelled", stops[0], "the hung turn resolves with the spec's required stop reason")
	assert.Equal(t, "end_turn", stops[1])
	// ISO3: the always-on posture announcement is now the first
	// assistant-shaped entry on every session — see
	// TestACPAgent_SelfConformance's comment for why the outer driver can't
	// tell it apart from real content.
	require.Len(t, texts, 2)
	assert.Contains(t, texts[0], "ctxloom:", "the first entry is the always-on isolation-posture announcement")
	assert.Contains(t, texts[1], "still alive?", "the session survives a per-turn cancel")
}
