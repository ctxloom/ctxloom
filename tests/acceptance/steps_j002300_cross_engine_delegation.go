//go:build acceptance

// J002300: "cross-engine delegation — different engines, different context, a
// real two-way bus" (j002300_cross_engine_delegation.feature). Complements J002100
// (j002100_delegation.feature, steps_j002100_delegation.go), which proved the
// PRIVILEGE half of delegation (MCP servers, permission modes, the
// journaled audit trail) using `agent_run` alone. This file proves the two
// things J002100's own comments name as out of reach from this harness: that a
// child's CONTEXT genuinely differs from a sibling's (asserted on content
// the child itself emits, not a config diff), and that `agent_send`/
// `agent_recv` carry real content between coordinator and child —
// previously exercised only at the unit level
// (internal/agentcoord/coord/*_test.go).
//
// j002600 was already taken (steps_j002600_worktree_task_store.go, landed on this
// base — not one of the features-draft/ placeholders j001000-j002400 reserve), so
// this journey is numbered j002300.
//
// HARNESS/PRODUCT FINDING (reported, not routed around — see the feature
// file's own header for the full account): the automatic "oneshot turn ->
// parent mailbox" bridge (coord/children.go's onTurnBoundary ->
// queueMail(...)) fires ONLY when the spawned backend does NOT implement
// agent.StructuredChat (operations/delegate.go's PrepareAgentChat: `if _,
// ok := backends.Get(rs.Backend).(agent.StructuredChat); !ok { p.oneshot =
// true }`). Every currently registered backend — mock included
// (internal/lm/backends/mock_chat.go) — implements StructuredChat, so that
// branch's own doc comment already says it plainly: "today, no production
// backend; only test doubles". In production, a delegated child's ONLY way
// to report to its coordinator is to itself decide, inside its own
// reasoning loop, to call `agent_send(to: "parent", ...)` through its
// FORWARDER MCP server — nothing bridges a chat child's output
// automatically. A scripted, non-reasoning backend (mock's Chat(), which
// only emits ChatEvents — it is not an MCP client and has no path to invoke
// the coordinator's own agent_send tool) structurally CANNOT do that. So the
// child->coordinator direction of the bus is provable only against a REAL
// reasoning engine — the @live scenario below — never hermetically. The
// hermetic scenarios instead read the child's OWN canonical transcript
// (internal/transcript/record.go's transcript.jsonl — a first-party ctxloom
// artifact, not a scrape, and the SAME class of durable, external,
// disk-backed observable j002100_delegation.feature already established for
// runs.jsonl) to prove requirement 3 (distinct context) and the
// coordinator->child half of requirement 4 (a real agent_send call, content
// verified in the child's own recorded next turn).
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
)

// j002300AgentSpec is one delegated child's fixture identity: which profile/
// bundle backs it, and the one distinctive marker string its fragment
// carries — the payload a sibling's assembled context must never contain.
type j002300AgentSpec struct {
	Name     string
	Profile  string
	Bundle   string
	Fragment string
	Guidance string // the distinct marker this agent's OWN context carries
}

// j002300State is J002300's fixture state: the configured agents and each spawned
// child's session harp (agent_send/transcript reads need a harp, never an
// agent name).
type j002300State struct {
	specs map[string]*j002300AgentSpec
	harps map[string]string // agent name -> spawned session harp
}

func j002300Of(w *World) *j002300State {
	if w.j002300 == nil {
		w.j002300 = &j002300State{
			specs: map[string]*j002300AgentSpec{},
			harps: map[string]string{},
		}
	}
	return w.j002300
}

// j002300BundleYAML renders one agent's backing bundle: a single fragment whose
// content IS the distinguishing marker — mirrors j000400_multi_engine.feature's
// live-sentinel fixture shape (steps_j000400.go's j000400TeamBundleYAML), one fragment
// per bundle rather than j000400's multi-surface bundle since context is the only
// surface this journey needs.
func j002300BundleYAML(s *j002300AgentSpec) string {
	return fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  %s:\n    content: %q\n", s.Fragment, s.Guidance)
}

// j002300ProfileYAML renders one agent's profile: the one bundle that backs it,
// the same ref shape j002100_delegation.feature's fixture uses
// (steps_j002100_delegation.go's j002100ProfileYAML) — proven to resolve correctly
// there.
func j002300ProfileYAML(s *j002300AgentSpec) string {
	return fmt.Sprintf("bundles:\n  - ctxloom:local@bundles/%s\n", s.Bundle)
}

// j002300HermeticConfigYAML renders config.yaml for the hermetic tier: one mock
// LLM config shared by both children (the hermetic tier's own documented
// scope — see the feature file's header note on what "same engine" means
// here), one agent binding per spec. `workspace: none` runs each child
// against the live checkout instead of a fresh worktree — this journey
// proves CONTEXT isolation and the message bus, not workspace isolation
// (j002200_isolation.feature's own job), and the fixture's own bundle/profile
// files are freshly written, uncommitted state that a worktree spawn would
// otherwise refuse to carry over without an explicit dirty_tree_handler
// (operations.handleDirtyParentTree) — irrelevant noise for what this
// journey asserts, and confirmed empirically: without it, agent_run's async
// launch fails outright ("refusing to auto-commit for delegated agent").
func j002300HermeticConfigYAML(specs ...*j002300AgentSpec) string {
	var b strings.Builder
	b.WriteString("version: 4\nworkspace: none\nllm:\n  configs:\n    fast:\n      type: mock\n  defaults:\n    primary: fast\n    fast: fast\nagents:\n")
	for _, s := range specs {
		fmt.Fprintf(&b, "  %s:\n    llm: fast\n    profiles:\n      - %s\n    permissions: bypass\n", s.Name, s.Profile)
	}
	return b.String()
}

// j002300LiveConfigYAML renders config.yaml for the @live tier: TWO distinct,
// real backend types (claude-code, codex — the proven-working pair; see
// live_engine_registry.go), each pinned to the same cheap model the rest of
// the @live suite already uses, so this is genuinely two different vendor
// engines, not one engine under two labels.
func j002300LiveConfigYAML(claudeSpec, codexSpec *j002300AgentSpec) string {
	return fmt.Sprintf(`version: 4
workspace: none
llm:
  configs:
    claude:
      type: claude-code
      model: claude-haiku-4-5-20251001
    codex:
      type: codex
      model: gpt-5.4-mini
  defaults:
    primary: claude
    fast: claude
agents:
  %s:
    llm: claude
    profiles:
      - %s
    permissions: bypass
  %s:
    llm: codex
    profiles:
      - %s
    permissions: bypass
`, claudeSpec.Name, claudeSpec.Profile, codexSpec.Name, codexSpec.Profile)
}

// j002300WriteAgent writes one agent's bundle + profile files.
func j002300WriteAgent(w *World, s *j002300AgentSpec) error {
	if err := w.env.WriteFile(".ctxloom/content/bundles/"+s.Bundle+".yaml", j002300BundleYAML(s)); err != nil {
		return err
	}
	return w.env.WriteFile(".ctxloom/profiles/"+s.Profile+".yaml", j002300ProfileYAML(s))
}

// --- Canonical transcript reading (hermetic observable) ---------------------
//
// ~/.ctxloom/sessions/<harp>/persist/transcript.jsonl is ctxloom's OWN
// captured conversation record (internal/transcript/record.go's documented
// schema) — a first-party, durable, disk-backed artifact every structured
// engine (mock included) writes through, not a private format being
// scraped. Decoded locally here (mirroring steps_j002100_delegation.go's
// j002100RunFact/j002100JournalLine — a minimal local shadow of the on-disk shape,
// not an import of the internal/transcript package) rather than trusting an
// in-process struct.

// j002300TranscriptEntry is one KindEntry line's payload
// (transcript.EntryPayload, field-subset).
type j002300TranscriptEntry struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// j002300TranscriptLine is one transcript.jsonl line's envelope
// (transcript.Record, field-subset): only "kind"/"entry" are needed here.
type j002300TranscriptLine struct {
	Kind  string                  `json:"kind"`
	Entry *j002300TranscriptEntry `json:"entry,omitempty"`
}

// j002300TranscriptPath returns harp's canonical transcript path under this
// scenario's isolated HOME (w.env.HomeDir) — built directly rather than via
// internal/paths' resolver, which would read the OUTER test process's own
// ambient HOME, not the isolated one a spawned `ctxloom mcp` subprocess
// actually wrote under (the identical reasoning steps_j002100_delegation.go's
// j002100JournalRaw already documents for runs.jsonl).
func j002300TranscriptPath(w *World, harp string) string {
	return filepath.Join(w.env.HomeDir, ".ctxloom", "sessions", harp, "persist", "transcript.jsonl")
}

// j002300ReadTranscriptEntries parses every KindEntry line's payload from harp's
// transcript, in file (append) order.
// j002300ReadTranscriptEntries parses every `kind:"entry"` line of harp's
// transcript. skipped and firstSkipErr report any line that failed to
// unmarshal (this used to `continue` past those silently, with no
// counter or diagnostic, so a corrupted or schema-drifted transcript read as
// merely "fewer entries" and surfaced as a 10-second timeout in
// j002300TranscriptAssistantCount instead of naming the real parse failure).
func j002300ReadTranscriptEntries(w *World, harp string) (out []j002300TranscriptEntry, skipped int, firstSkipErr error, err error) {
	path := j002300TranscriptPath(w, harp)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("j002300: read transcript %s: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var l j002300TranscriptLine
		if uerr := json.Unmarshal([]byte(line), &l); uerr != nil {
			skipped++
			if firstSkipErr == nil {
				firstSkipErr = fmt.Errorf("line %q: %w", line, uerr)
			}
			continue
		}
		if l.Kind == "entry" && l.Entry != nil {
			out = append(out, *l.Entry)
		}
	}
	return out, skipped, firstSkipErr, nil
}

// j002300TranscriptAssistantCount waits (bounded) for harp's transcript to carry
// AT LEAST want assistant entries, returning them in order. A oneshot
// tool-subprocess turn (mock spawns a real `ctxloom llm serve mock` process
// per turn) completes in well under a second locally, but this box runs
// other agents concurrently (see this session's own shared-state-hygiene
// brief) — polling tolerates load-induced slack without a fixed sleep either
// racing or over-waiting.
func j002300TranscriptAssistantCount(w *World, harp string, want int) ([]j002300TranscriptEntry, error) {
	deadline := time.Now().Add(10 * time.Second)
	var assistants []j002300TranscriptEntry
	var lastErr error
	var lastSkipped int
	var lastSkipErr error
	for time.Now().Before(deadline) {
		entries, skipped, skipErr, err := j002300ReadTranscriptEntries(w, harp)
		if err != nil {
			lastErr = err
		} else {
			lastSkipped, lastSkipErr = skipped, skipErr
			assistants = assistants[:0]
			for _, e := range entries {
				if e.Type == "assistant" {
					assistants = append(assistants, e)
				}
			}
			if len(assistants) >= want {
				return assistants, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr != nil {
		return nil, fmt.Errorf("j002300: transcript for harp %q never appeared after 10s: %w", harp, lastErr)
	}
	if lastSkipped > 0 {
		return nil, fmt.Errorf("j002300: transcript for harp %q carries %d assistant entr(y/ies) after 10s, want >= %d -- and %d transcript line(s) failed to parse (first: %v), so this may be a corrupted/schema-drifted transcript, not a slow child",
			harp, len(assistants), want, lastSkipped, lastSkipErr)
	}
	return nil, fmt.Errorf("j002300: transcript for harp %q carries %d assistant entr(y/ies) after 10s, want >= %d", harp, len(assistants), want)
}

func registerJ002300Steps(ctx *godog.ScenarioContext) {
	// --- Hermetic fixture -------------------------------------------------

	ctx.Step(`^Alice's coordinator can delegate to two agents, "([^"]*)" and "([^"]*)", each carrying its own distinct guidance in its own profile$`,
		func(c context.Context, nameA, nameB string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			specA := &j002300AgentSpec{
				Name: nameA, Profile: nameA + "-profile", Bundle: "bundle-" + nameA, Fragment: "guidance",
				Guidance: "J002300-GUIDANCE-" + strings.ToUpper(nameA) + "-3f8a91: cite primary sources only, never secondary summaries.",
			}
			specB := &j002300AgentSpec{
				Name: nameB, Profile: nameB + "-profile", Bundle: "bundle-" + nameB, Fragment: "guidance",
				Guidance: "J002300-GUIDANCE-" + strings.ToUpper(nameB) + "-7c2d64: express every distance in metric units only.",
			}
			j002300.specs[nameA] = specA
			j002300.specs[nameB] = specB
			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			if err := j002300WriteAgent(w, specA); err != nil {
				return err
			}
			if err := j002300WriteAgent(w, specB); err != nil {
				return err
			}
			return w.env.WriteFile(".ctxloom/config.yaml", j002300HermeticConfigYAML(specA, specB))
		})

	// --- @live fixture ------------------------------------------------------
	//
	// Gate-and-skip mirrors steps_live.go's "a real X agent is available" and
	// isolation_probe.go's per-cell loudness: both engines are probed BEFORE
	// anything is written or spent, and a skip names exactly which engine and
	// why. Unlike steps_live.go (one engine per scenario), this needs TWO —
	// so it is its own step rather than a second call to the shared one
	// (which would overwrite config.yaml between calls).

	ctx.Step(`^real "claude" and "codex" engines are both available for cross-engine delegation$`,
		func(c context.Context) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			optIn := resolveOptIn()
			claudeStatus := probeEngine("claude", liveAgents["claude"], realHomeDir, optIn)
			codexStatus := probeEngine("codex", liveAgents["codex"], realHomeDir, optIn)
			// Loud regardless of outcome: named per engine, not a single bit.
			w.docStepMaterialized = formatLiveEngineReport([]engineStatus{claudeStatus, codexStatus})
			if !claudeStatus.available || !codexStatus.available {
				var missing []string
				if !claudeStatus.available {
					missing = append(missing, fmt.Sprintf("claude (%s)", claudeStatus.reason))
				}
				if !codexStatus.available {
					missing = append(missing, fmt.Sprintf("codex (%s)", codexStatus.reason))
				}
				fmt.Printf("SKIP j002300 cross-engine @live: %s\n", strings.Join(missing, "; "))
				return godog.ErrSkip
			}

			claudeSpec := &j002300AgentSpec{
				Name: "claude-child", Profile: "claude-child-profile", Bundle: "bundle-claude-child", Fragment: "marker",
				Guidance: "J002300-LIVE-MARKER-CLAUDE-9e1b52",
			}
			codexSpec := &j002300AgentSpec{
				Name: "codex-child", Profile: "codex-child-profile", Bundle: "bundle-codex-child", Fragment: "marker",
				Guidance: "J002300-LIVE-MARKER-CODEX-4a6f18",
			}
			j002300.specs[claudeSpec.Name] = claudeSpec
			j002300.specs[codexSpec.Name] = codexSpec

			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			if err := j002300WriteAgent(w, claudeSpec); err != nil {
				return err
			}
			if err := j002300WriteAgent(w, codexSpec); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/config.yaml", j002300LiveConfigYAML(claudeSpec, codexSpec)); err != nil {
				return err
			}

			// Subscription path: MAP each engine at its own real credential
			// directory (erased-collar) for whichever isn't using the
			// API-key path — exactly steps_live.go's own per-engine logic,
			// applied twice since this scenario runs both at once. This is
			// the scenario jovial-employee was measured on: the codex half
			// used to be seeded by COPY, and codex's refresh inside the
			// throwaway HOME consumed the host's refresh token server-side.
			if err := seedLiveCredentials("claude", liveAgents["claude"], realHomeDir, w.env.HomeDir, w.env.SetChildEnv); err != nil {
				return err
			}
			return seedLiveCredentials("codex", liveAgents["codex"], realHomeDir, w.env.HomeDir, w.env.SetChildEnv)
		})

	// --- Shared: capture a spawned child's harp -----------------------------

	ctx.Step(`^"([^"]*)"'s session harp is remembered$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		j002300 := j002300Of(w)
		harp, ok := w.lastInner["harp"].(string)
		if !ok || harp == "" {
			return fmt.Errorf("j002300: last tool result carries no harp field for %q; result:\n%s", name, w.lastTool.JSON())
		}
		j002300.harps[name] = harp
		return nil
	})

	// --- Hermetic assertions: read the child's OWN canonical transcript ----

	ctx.Step(`^"([^"]*)"'s reported turn carries its own guidance, not "([^"]*)"'s$`,
		func(c context.Context, self, other string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			selfSpec, ok := j002300.specs[self]
			if !ok {
				return fmt.Errorf("j002300: unknown agent %q", self)
			}
			otherSpec, ok := j002300.specs[other]
			if !ok {
				return fmt.Errorf("j002300: unknown agent %q", other)
			}
			harp, ok := j002300.harps[self]
			if !ok {
				return fmt.Errorf("j002300: no session harp remembered for %q", self)
			}
			assistants, err := j002300TranscriptAssistantCount(w, harp, 1)
			if err != nil {
				return err
			}
			body := assistants[0].Content
			w.docStepMaterialized = fmt.Sprintf("%s — %s's first reported turn:\n  %s", j002300TranscriptPath(w, harp), self, body)
			if !strings.Contains(body, selfSpec.Guidance) {
				return fmt.Errorf("%s's reported turn does not carry its OWN guidance %q; turn:\n%s", self, selfSpec.Guidance, body)
			}
			if strings.Contains(body, otherSpec.Guidance) {
				return fmt.Errorf("CONTEXT LEAK: %s's reported turn unexpectedly carries %s's guidance %q; turn:\n%s", self, other, otherSpec.Guidance, body)
			}
			return nil
		})

	// The agent_send call itself: dynamic (the recipient is a runtime-minted
	// harp, never known ahead of time), so it cannot ride the generic
	// "... with:" table step j002100 uses for agent_run's static args. Still a
	// GENUINE tool invocation through the same callTool the generic step
	// uses (steps_mcp.go) — literally contains `calls tool "agent_send"` so
	// the MCP-tool completeness gate (completeness_test.go's ranAsTool)
	// credits it as real coverage, not vacuous prose.
	ctx.Step(`^the agent calls tool "agent_send" addressed to "([^"]*)"'s session with body "([^"]*)"$`,
		func(c context.Context, name, body string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			harp, ok := j002300.harps[name]
			if !ok {
				return fmt.Errorf("j002300: no session harp remembered for %q — spawn it first", name)
			}
			return callTool(c, "agent_send", map[string]any{"to": harp, "body": body})
		})

	ctx.Step(`^"([^"]*)"'s next reported turn carries "([^"]*)"$`,
		func(c context.Context, self, want string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			harp, ok := j002300.harps[self]
			if !ok {
				return fmt.Errorf("j002300: no session harp remembered for %q", self)
			}
			// want >= 2: the SECOND assistant entry is this turn's — the
			// first is the initial "go" turn's, asserted by the guidance
			// step above.
			assistants, err := j002300TranscriptAssistantCount(w, harp, 2)
			if err != nil {
				return err
			}
			body := assistants[len(assistants)-1].Content
			w.docStepMaterialized = fmt.Sprintf("%s — %s's next reported turn:\n  %s", j002300TranscriptPath(w, harp), self, body)
			if !strings.Contains(body, want) {
				return fmt.Errorf("%s's next reported turn does not carry %q; turn:\n%s", self, want, body)
			}
			return nil
		})

	// --- @live-only: agent_recv, retried within a live-turn-sized budget ----
	//
	// testenv/mcpclient.go's mcpRecvTimeout hard-caps EVERY single JSON-RPC
	// round trip this harness makes at 15s, client-side, regardless of the
	// agent_recv tool's own `wait` argument — shared harness code this task
	// must not change. A real claude/codex turn (context load + reasoning +
	// deciding to call agent_send) routinely exceeds that. Retrying
	// agent_recv from the TEST side tolerates it without touching that file:
	// each retry is a free mailbox poll, never a second paid model call, so a
	// generous total budget costs nothing extra. Literally contains `calls
	// tool "agent_recv"` so completeness_test.go's ranAsTool still credits
	// this as real coverage.
	ctx.Step(`^the agent calls tool "agent_recv" repeatedly, waiting up to (\d+)s total, until "([^"]*)" reports$`,
		func(c context.Context, budgetSec int, name string) error {
			deadline := time.Now().Add(time.Duration(budgetSec) * time.Second)
			var lastErr error
			for {
				if err := callTool(c, "agent_recv", map[string]any{"wait": 12}); err != nil {
					return fmt.Errorf("j002300: agent_recv transport error while waiting for %q: %w", name, err)
				}
				w := worldFrom(c)
				if isErr, msg := w.lastTool.IsError(); isErr {
					lastErr = fmt.Errorf("%s", msg)
					if time.Now().After(deadline) {
						return fmt.Errorf("j002300: agent_recv never returned a message from %q within %ds: %w", name, budgetSec, lastErr)
					}
					continue
				}
				return nil // a real, non-error result — subsequent Then steps assert its content
			}
		})

	// --- @live-only assertion: the child's OWN agent_send reply, observed --
	// via agent_recv — the direction the hermetic tier cannot exercise (see
	// this file's header finding). Kept textually distinct from the
	// transcript-based steps above so a reader can never mistake one
	// observable for the other.

	ctx.Step(`^the received message is from "([^"]*)" and its body carries its own guidance, not "([^"]*)"'s$`,
		func(c context.Context, self, other string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			selfSpec, ok := j002300.specs[self]
			if !ok {
				return fmt.Errorf("j002300: unknown agent %q", self)
			}
			otherSpec, ok := j002300.specs[other]
			if !ok {
				return fmt.Errorf("j002300: unknown agent %q", other)
			}
			harp, ok := j002300.harps[self]
			if !ok {
				return fmt.Errorf("j002300: no session harp remembered for %q", self)
			}
			msg, err := j002300FindMessageFrom(w, harp)
			if err != nil {
				return err
			}
			body, _ := msg["body"].(string)
			w.docStepMaterialized = fmt.Sprintf("agent_recv — message from %s (harp %s):\n  body: %s", self, harp, body)
			if !strings.Contains(body, selfSpec.Guidance) {
				return fmt.Errorf("%s's reported body does not carry its OWN guidance %q; body:\n%s", self, selfSpec.Guidance, body)
			}
			if strings.Contains(body, otherSpec.Guidance) {
				return fmt.Errorf("CONTEXT LEAK: %s's reported body unexpectedly carries %s's guidance %q; body:\n%s", self, other, otherSpec.Guidance, body)
			}
			return nil
		})

	ctx.Step(`^the received message is from "([^"]*)" and its body contains "([^"]*)"$`,
		func(c context.Context, self, want string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			harp, ok := j002300.harps[self]
			if !ok {
				return fmt.Errorf("j002300: no session harp remembered for %q", self)
			}
			msg, err := j002300FindMessageFrom(w, harp)
			if err != nil {
				return err
			}
			body, _ := msg["body"].(string)
			w.docStepMaterialized = fmt.Sprintf("agent_recv — message from %s (harp %s):\n  body: %s", self, harp, body)
			if !strings.Contains(body, want) {
				return fmt.Errorf("%s's reported body does not contain %q; body:\n%s", self, want, body)
			}
			return nil
		})
}

// j002300Messages unwraps an agent_recv result's "messages" array (@live only —
// see the header finding for why the hermetic tier cannot reach this path).
func j002300Messages(w *World) ([]map[string]any, error) {
	raw, ok := w.lastInner["messages"].([]any)
	if !ok {
		return nil, fmt.Errorf("j002300: tool result carries no messages array; result:\n%s", w.lastTool.JSON())
	}
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		if mm, ok := m.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out, nil
}

// j002300FindMessageFrom returns the received message whose "from" is harp
// (@live only).
func j002300FindMessageFrom(w *World, harp string) (map[string]any, error) {
	msgs, err := j002300Messages(w)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		if f, _ := m["from"].(string); f == harp {
			return m, nil
		}
	}
	return nil, fmt.Errorf("j002300: no message from harp %q among %d received message(s); result:\n%s", harp, len(msgs), w.lastTool.JSON())
}
