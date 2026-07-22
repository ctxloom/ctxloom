//go:build acceptance

// J17: "cross-engine delegation — different engines, different context, a
// real two-way bus" (j17_cross_engine_delegation.feature). Complements J6
// (j6_delegation.feature, steps_j6_delegation.go), which proved the
// PRIVILEGE half of delegation (MCP servers, permission modes, the
// journaled audit trail) using `agent_run` alone. This file proves the two
// things J6's own comments name as out of reach from this harness: that a
// child's CONTEXT genuinely differs from a sibling's (asserted on content
// the child itself emits, not a config diff), and that `agent_send`/
// `agent_recv` carry real content in BOTH directions between coordinator and
// child — previously exercised only at the unit level
// (internal/agentcoord/coord/*_test.go).
//
// j16 was already taken (steps_j16_worktree_task_store.go, landed on this
// base — not one of the features-draft/ placeholders j12-j15 reserve), so
// this journey is numbered j17.
package acceptance

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
)

// j17AgentSpec is one delegated child's fixture identity: which profile/
// bundle backs it, and the one distinctive marker string its fragment
// carries — the payload a sibling's assembled context must never contain.
type j17AgentSpec struct {
	Name     string
	Profile  string
	Bundle   string
	Fragment string
	Guidance string // the distinct marker this agent's OWN context carries
}

// j17State is J17's fixture state: the configured agents and each spawned
// child's session harp (agent_send needs a harp, never an agent name).
type j17State struct {
	specs map[string]*j17AgentSpec
	harps map[string]string // agent name -> spawned session harp
}

func j17Of(w *World) *j17State {
	if w.j17 == nil {
		w.j17 = &j17State{
			specs: map[string]*j17AgentSpec{},
			harps: map[string]string{},
		}
	}
	return w.j17
}

// j17BundleYAML renders one agent's backing bundle: a single fragment whose
// content IS the distinguishing marker — mirrors j5_multi_engine.feature's
// live-sentinel fixture shape (steps_j5.go's j5TeamBundleYAML), one fragment
// per bundle rather than j5's multi-surface bundle since context is the only
// surface this journey needs.
func j17BundleYAML(s *j17AgentSpec) string {
	return fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  %s:\n    content: %q\n", s.Fragment, s.Guidance)
}

// j17ProfileYAML renders one agent's profile: the one bundle that backs it,
// the same ref shape j6_delegation.feature's fixture uses
// (steps_j6_delegation.go's j6ProfileYAML) — proven to resolve correctly
// there.
func j17ProfileYAML(s *j17AgentSpec) string {
	return fmt.Sprintf("bundles:\n  - ctxloom:local@bundles/%s\n", s.Bundle)
}

// j17HermeticConfigYAML renders config.yaml for the hermetic tier: one mock
// LLM config shared by both children (the hermetic tier's own documented
// scope — see the feature file's header note on what "same engine" means
// here), one agent binding per spec.
func j17HermeticConfigYAML(specs ...*j17AgentSpec) string {
	var b strings.Builder
	b.WriteString("version: 4\nllm:\n  configs:\n    fast:\n      type: mock\n  defaults:\n    primary: fast\n    fast: fast\nagents:\n")
	for _, s := range specs {
		fmt.Fprintf(&b, "  %s:\n    engine: fast\n    profiles:\n      - %s\n    permissions: bypass\n", s.Name, s.Profile)
	}
	return b.String()
}

// j17LiveConfigYAML renders config.yaml for the @live tier: TWO distinct,
// real backend types (claude-code, codex — the proven-working pair; see
// live_engine_registry.go), each pinned to the same cheap model the rest of
// the @live suite already uses, so this is genuinely two different vendor
// engines, not one engine under two labels.
func j17LiveConfigYAML(claudeSpec, codexSpec *j17AgentSpec) string {
	return fmt.Sprintf(`version: 4
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
    engine: claude
    profiles:
      - %s
    permissions: bypass
  %s:
    engine: codex
    profiles:
      - %s
    permissions: bypass
`, claudeSpec.Name, claudeSpec.Profile, codexSpec.Name, codexSpec.Profile)
}

// j17WriteAgent writes one agent's bundle + profile files.
func j17WriteAgent(w *World, s *j17AgentSpec) error {
	if err := w.env.WriteFile(".ctxloom/content/bundles/"+s.Bundle+".yaml", j17BundleYAML(s)); err != nil {
		return err
	}
	return w.env.WriteFile(".ctxloom/profiles/"+s.Profile+".yaml", j17ProfileYAML(s))
}

// j17Messages unwraps an agent_recv result's "messages" array.
func j17Messages(w *World) ([]map[string]any, error) {
	raw, ok := w.lastInner["messages"].([]any)
	if !ok {
		return nil, fmt.Errorf("j17: tool result carries no messages array; result:\n%s", w.lastTool.JSON())
	}
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		if mm, ok := m.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out, nil
}

// j17FindMessageFrom returns the received message whose "from" is harp.
func j17FindMessageFrom(w *World, harp string) (map[string]any, error) {
	msgs, err := j17Messages(w)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		if f, _ := m["from"].(string); f == harp {
			return m, nil
		}
	}
	return nil, fmt.Errorf("j17: no message from harp %q among %d received message(s); result:\n%s", harp, len(msgs), w.lastTool.JSON())
}

func registerJ17Steps(ctx *godog.ScenarioContext) {
	// --- Hermetic fixture -------------------------------------------------

	ctx.Step(`^Alice's coordinator can delegate to two agents, "([^"]*)" and "([^"]*)", each carrying its own distinct guidance in its own profile$`,
		func(c context.Context, nameA, nameB string) error {
			w := worldFrom(c)
			j17 := j17Of(w)
			specA := &j17AgentSpec{
				Name: nameA, Profile: nameA + "-profile", Bundle: "bundle-" + nameA, Fragment: "guidance",
				Guidance: "J17-GUIDANCE-" + strings.ToUpper(nameA) + "-3f8a91: cite primary sources only, never secondary summaries.",
			}
			specB := &j17AgentSpec{
				Name: nameB, Profile: nameB + "-profile", Bundle: "bundle-" + nameB, Fragment: "guidance",
				Guidance: "J17-GUIDANCE-" + strings.ToUpper(nameB) + "-7c2d64: express every distance in metric units only.",
			}
			j17.specs[nameA] = specA
			j17.specs[nameB] = specB
			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			if err := j17WriteAgent(w, specA); err != nil {
				return err
			}
			if err := j17WriteAgent(w, specB); err != nil {
				return err
			}
			return w.env.WriteFile(".ctxloom/config.yaml", j17HermeticConfigYAML(specA, specB))
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
			j17 := j17Of(w)
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
				fmt.Printf("SKIP j17 cross-engine @live: %s\n", strings.Join(missing, "; "))
				return godog.ErrSkip
			}

			claudeSpec := &j17AgentSpec{
				Name: "claude-child", Profile: "claude-child-profile", Bundle: "bundle-claude-child", Fragment: "marker",
				Guidance: "J17-LIVE-MARKER-CLAUDE-9e1b52",
			}
			codexSpec := &j17AgentSpec{
				Name: "codex-child", Profile: "codex-child-profile", Bundle: "bundle-codex-child", Fragment: "marker",
				Guidance: "J17-LIVE-MARKER-CODEX-4a6f18",
			}
			j17.specs[claudeSpec.Name] = claudeSpec
			j17.specs[codexSpec.Name] = codexSpec

			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			if err := j17WriteAgent(w, claudeSpec); err != nil {
				return err
			}
			if err := j17WriteAgent(w, codexSpec); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/config.yaml", j17LiveConfigYAML(claudeSpec, codexSpec)); err != nil {
				return err
			}

			// Subscription path: copy just the credential files into the
			// isolated HOME for whichever engine isn't using the API-key
			// path (exactly steps_live.go's own per-engine logic, applied
			// twice since this scenario runs both at once).
			claude := liveAgents["claude"]
			if !envSet(claude.apiKeyEnvs) && realHomeDir != "" {
				claude.copyCreds(realHomeDir, w.env.HomeDir)
			}
			codex := liveAgents["codex"]
			if !envSet(codex.apiKeyEnvs) && realHomeDir != "" {
				codex.copyCreds(realHomeDir, w.env.HomeDir)
			}
			return nil
		})

	// --- Shared assertions (hermetic + live) --------------------------------

	ctx.Step(`^"([^"]*)"'s session harp is remembered$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		j17 := j17Of(w)
		harp, ok := w.lastInner["harp"].(string)
		if !ok || harp == "" {
			return fmt.Errorf("j17: last tool result carries no harp field for %q; result:\n%s", name, w.lastTool.JSON())
		}
		j17.harps[name] = harp
		return nil
	})

	// The agent_send call itself: dynamic (the recipient is a runtime-minted
	// harp, never known ahead of time), so it cannot ride the generic
	// "... with:" table step j6 uses for agent_run's static args. Still a
	// GENUINE tool invocation through the same callTool the generic step
	// uses (steps_mcp.go) — literally contains `calls tool "agent_send"` so
	// the MCP-tool completeness gate (completeness_test.go's ranAsTool)
	// credits it as real coverage, not vacuous prose.
	ctx.Step(`^the agent calls tool "agent_send" addressed to "([^"]*)"'s session with body "([^"]*)"$`,
		func(c context.Context, name, body string) error {
			w := worldFrom(c)
			j17 := j17Of(w)
			harp, ok := j17.harps[name]
			if !ok {
				return fmt.Errorf("j17: no session harp remembered for %q — spawn it first", name)
			}
			return callTool(c, "agent_send", map[string]any{"to": harp, "body": body})
		})

	ctx.Step(`^the received message is from "([^"]*)" and its body carries its own guidance, not "([^"]*)"'s$`,
		func(c context.Context, self, other string) error {
			w := worldFrom(c)
			j17 := j17Of(w)
			selfSpec, ok := j17.specs[self]
			if !ok {
				return fmt.Errorf("j17: unknown agent %q", self)
			}
			otherSpec, ok := j17.specs[other]
			if !ok {
				return fmt.Errorf("j17: unknown agent %q", other)
			}
			harp, ok := j17.harps[self]
			if !ok {
				return fmt.Errorf("j17: no session harp remembered for %q", self)
			}
			msg, err := j17FindMessageFrom(w, harp)
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
			j17 := j17Of(w)
			harp, ok := j17.harps[self]
			if !ok {
				return fmt.Errorf("j17: no session harp remembered for %q", self)
			}
			msg, err := j17FindMessageFrom(w, harp)
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
