//go:build acceptance

// J6: "a delegated child gets ITS OWN permissions and ITS OWN MCP servers —
// not the parent's, not a sibling's — and that grant is journaled, durable,
// and auditable" (j6_delegation.feature).
//
// tests/acceptance drives a `ctxloom` SUBPROCESS over MCP stdio; it can never
// see an in-process Go struct (an earlier attempt at this journey correctly
// refused to plan around `fakeChatEngine`, which is private to a different
// package and a different process — see the plan's BLOCKED note). And unlike
// what an earlier draft of this file's OWN brief assumed, a plain `ctxloom
// mcp` subprocess never exposes an MCP tool literally named "roster" either:
// that tool is registered ONLY on a spawned session's runner-terminated
// socket (internal/cli/mcp_runner.go's newRunnerMCPServer), which this
// harness's `ctxloom mcp` process — launched directly, with
// CTXLOOM_MCP_SOCKET scrubbed by testsupport.EnvKeys — never becomes.
//
// So every assertion here reads the coordinator's own durable run-registry
// journal, runs.jsonl, straight off disk (internal/agentcoord/coord/statedir.go's
// documented layout, ~/.ctxloom/coord/<project-key>/runs.jsonl under this
// scenario's isolated HOME). This is not a weaker observable than "roster" —
// it is the SAME data: consumer.go's listRunsSnapshot (roster's real backing
// projection) is folded from these exact run.enqueued facts. Reading the
// journal directly is a genuine, external, disk-durable observable, never a
// captured Go struct.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cucumber/godog"
)

// j6AgentSpec is one delegated child's fixture identity: which profile it
// resolves, which bundle backs that profile, the one MCP server that bundle
// ships (with a command carrying a plausible secret-shaped argument, so the
// "names never secrets" scenario has something real to prove was never
// journaled), and the permission mode its agent binding declares.
type j6AgentSpec struct {
	Name       string
	Profile    string
	Bundle     string
	Server     string
	Command    string
	SecretArg  string
	Permission string
}

// j6State is J6's fixture state: the two configured agents (mutable — the
// durability scenario edits one in place), a snapshot store for "remembered
// as" labels, and each agent's pre-edit spec (so a snapshot taken before an
// edit can still be checked against what it USED to be).
type j6State struct {
	order      []string // agent names, declaration order
	specs      map[string]*j6AgentSpec
	beforeEdit map[string]*j6AgentSpec // agent name -> its spec as captured just before an edit
	snapshots  map[string]j6RunFact    // "remembered as" label -> captured journal fact
	harps      map[string]string       // agent name -> its most recently spawned session harp
}

// j6RunFact is runEnqueued's (coord/facts.go) payload, decoded straight off
// runs.jsonl — the exact fields Wave F1 added so a child's resolved grant is
// auditable, not just provable in-process.
type j6RunFact struct {
	RunID      string   `json:"run_id"`
	Harp       string   `json:"harp"`
	Agent      string   `json:"agent"`
	Permission string   `json:"permission"`
	MCPServers []string `json:"mcp_servers"`
}

// j6JournalLine is one runs.jsonl line's outer envelope (coord/facts.go's
// journal.Fact shape: {kind, at, data}); only "kind" is inspected here to
// find run.enqueued lines, "data" decodes into j6RunFact.
type j6JournalLine struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

func j6Of(w *World) *j6State {
	if w.j6 == nil {
		w.j6 = &j6State{
			specs:      map[string]*j6AgentSpec{},
			beforeEdit: map[string]*j6AgentSpec{},
			snapshots:  map[string]j6RunFact{},
			harps:      map[string]string{},
		}
	}
	return w.j6
}

// j6RenderConfig renders config.yaml for the current spec state: one "fast"
// mock LLM label (the same built-in gRPC mock plugin every other journey's
// primary engine already uses — real production code, not a test-only
// fixture) and one agent binding per declared name, in declaration order (map
// iteration order is not stable; j6.order pins it).
func j6RenderConfig(j6 *j6State) string {
	var b strings.Builder
	b.WriteString("version: 4\nllm:\n  configs:\n    fast:\n      type: mock\n  defaults:\n    primary: fast\n    fast: fast\nagents:\n")
	for _, name := range j6.order {
		s := j6.specs[name]
		fmt.Fprintf(&b, "  %s:\n    engine: fast\n    profiles:\n      - %s\n    permissions: %s\n", name, s.Profile, s.Permission)
	}
	return b.String()
}

// j6BundleYAML renders one agent's backing bundle: a single MCP server whose
// command carries one plausible secret-shaped argument.
func j6BundleYAML(s *j6AgentSpec) string {
	return fmt.Sprintf("version: \"1.0.0\"\nmcp:\n  %s:\n    command: %s\n    args: [%q]\n", s.Server, s.Command, s.SecretArg)
}

// j6ProfileYAML renders one agent's profile: the one bundle that backs it.
func j6ProfileYAML(s *j6AgentSpec) string {
	return fmt.Sprintf("bundles:\n  - ctxloom:local@bundles/%s\n", s.Bundle)
}

// j6JournalRaw reads the coordinator's run-registry journal straight off
// disk: ~/.ctxloom/coord/<project-key>/runs.jsonl under this scenario's
// isolated HOME (coord/statedir.go's documented layout). The project key is
// not recomputed here — a fresh isolated HOME holds exactly one coordinator's
// state, so a glob finds it without needing to reproduce
// taskops.ResolveProjectIdentity's key derivation.
func j6JournalRaw(w *World) (string, error) {
	pattern := filepath.Join(w.env.HomeDir, ".ctxloom", "coord", "*", "runs.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("glob %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no runs.jsonl found under %q — was a child ever spawned?", pattern)
	}
	var b strings.Builder
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", m, err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// j6AllFactsForAgent parses every run.enqueued fact for one agent name, in
// journal (append) order.
func j6AllFactsForAgent(w *World, agent string) ([]j6RunFact, error) {
	raw, err := j6JournalRaw(w)
	if err != nil {
		return nil, err
	}
	var out []j6RunFact
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var jl j6JournalLine
		if err := json.Unmarshal([]byte(line), &jl); err != nil {
			continue
		}
		if jl.Kind != "run.enqueued" {
			continue
		}
		var f j6RunFact
		if err := json.Unmarshal(jl.Data, &f); err != nil {
			continue
		}
		if f.Agent != agent {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// j6LatestFactForAgent returns the MOST RECENT run.enqueued fact for agent —
// the only one, in every scenario except the durability scenario, which
// spawns "fixer" twice and relies on "remembered as" snapshots (below) to
// keep the first one reachable once a second exists.
func j6LatestFactForAgent(w *World, agent string) (*j6RunFact, error) {
	facts, err := j6AllFactsForAgent(w, agent)
	if err != nil {
		return nil, err
	}
	if len(facts) == 0 {
		return nil, fmt.Errorf("no journaled run.enqueued fact found for agent %q", agent)
	}
	return &facts[len(facts)-1], nil
}

func registerJ6Steps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Alice's coordinator can delegate to two agents, "([^"]*)" and "([^"]*)", each with its own profile, its own MCP server, and its own permission mode$`,
		func(c context.Context, nameA, nameB string) error {
			w := worldFrom(c)
			j6 := j6Of(w)
			j6.order = []string{nameA, nameB}
			// Two headless-safe permission modes (agent.PermissionMode.SafeHeadless:
			// bypass|plan) so the "recorded, not just implied" scenario has two
			// genuinely different values to tell apart, not one value asserted twice.
			j6.specs[nameA] = &j6AgentSpec{
				Name: nameA, Profile: "review-profile", Bundle: "bundle-review",
				Server: "docs-lookup", Command: "docs-server", SecretArg: "DOCS-SECRET-7e1d44",
				Permission: "plan",
			}
			j6.specs[nameB] = &j6AgentSpec{
				Name: nameB, Profile: "fix-profile", Bundle: "bundle-fix",
				Server: "deploy-tool", Command: "deploy-cli", SecretArg: "DEPLOY-SECRET-9f2c81",
				Permission: "bypass",
			}
			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/content/bundles/bundle-review.yaml", j6BundleYAML(j6.specs[nameA])); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/content/bundles/bundle-fix.yaml", j6BundleYAML(j6.specs[nameB])); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/profiles/review-profile.yaml", j6ProfileYAML(j6.specs[nameA])); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/profiles/fix-profile.yaml", j6ProfileYAML(j6.specs[nameB])); err != nil {
				return err
			}
			return w.env.WriteFile(".ctxloom/config.yaml", j6RenderConfig(j6))
		})

	ctx.Step(`^"([^"]*)"'s journaled grant carries its own MCP server and not "([^"]*)"'s$`,
		func(c context.Context, self, other string) error {
			w := worldFrom(c)
			j6 := j6Of(w)
			selfSpec, ok := j6.specs[self]
			if !ok {
				return fmt.Errorf("j6: unknown agent %q", self)
			}
			otherSpec, ok := j6.specs[other]
			if !ok {
				return fmt.Errorf("j6: unknown agent %q", other)
			}
			fact, err := j6LatestFactForAgent(w, self)
			if err != nil {
				return err
			}
			w.docStepMaterialized = fmt.Sprintf("runs.jsonl — run_id %s (agent=%s):\n  mcp_servers: %v", fact.RunID, fact.Agent, fact.MCPServers)
			if !slices.Contains(fact.MCPServers, selfSpec.Server) {
				return fmt.Errorf("%s's journaled mcp_servers %v do not contain its own server %q", self, fact.MCPServers, selfSpec.Server)
			}
			if slices.Contains(fact.MCPServers, otherSpec.Server) {
				return fmt.Errorf("PRIVILEGE BOUNDARY VIOLATED: %s's journaled mcp_servers %v unexpectedly contain %s's server %q", self, fact.MCPServers, other, otherSpec.Server)
			}
			return nil
		})

	ctx.Step(`^"([^"]*)"'s journaled grant records its own permission mode$`,
		func(c context.Context, name string) error {
			w := worldFrom(c)
			j6 := j6Of(w)
			spec, ok := j6.specs[name]
			if !ok {
				return fmt.Errorf("j6: unknown agent %q", name)
			}
			fact, err := j6LatestFactForAgent(w, name)
			if err != nil {
				return err
			}
			w.docStepMaterialized = fmt.Sprintf("runs.jsonl — run_id %s (agent=%s):\n  permission: %q", fact.RunID, fact.Agent, fact.Permission)
			if fact.Permission == "" {
				return fmt.Errorf("%s's journaled run carries no permission mode at all", name)
			}
			if fact.Permission != spec.Permission {
				return fmt.Errorf("%s's journaled permission mode = %q, want %q", name, fact.Permission, spec.Permission)
			}
			return nil
		})

	ctx.Step(`^"([^"]*)"'s journaled grant records its own permission mode, different from "([^"]*)"'s$`,
		func(c context.Context, name, other string) error {
			w := worldFrom(c)
			factSelf, err := j6LatestFactForAgent(w, name)
			if err != nil {
				return err
			}
			factOther, err := j6LatestFactForAgent(w, other)
			if err != nil {
				return err
			}
			w.docStepMaterialized = fmt.Sprintf("runs.jsonl:\n  %s (run_id %s) → permission: %q\n  %s (run_id %s) → permission: %q",
				name, factSelf.RunID, factSelf.Permission, other, factOther.RunID, factOther.Permission)
			if factSelf.Permission == "" || factOther.Permission == "" {
				return fmt.Errorf("one of %s/%s carries no permission mode", name, other)
			}
			if factSelf.Permission == factOther.Permission {
				return fmt.Errorf("%s and %s unexpectedly share permission mode %q — the fixture must give them genuinely different modes to prove each is recorded as its OWN grant", name, other, factSelf.Permission)
			}
			return nil
		})

	ctx.Step(`^"([^"]*)"'s current journaled grant is remembered as "([^"]*)"$`,
		func(c context.Context, name, label string) error {
			w := worldFrom(c)
			j6 := j6Of(w)
			fact, err := j6LatestFactForAgent(w, name)
			if err != nil {
				return err
			}
			j6.snapshots[label] = *fact
			// Real evidence: the actual journaled fact this step just snapshot
			// off runs.jsonl, not a restatement of "it was remembered".
			w.docStepMaterialized = fmt.Sprintf("runs.jsonl — %q snapshot of %s: run_id %s, permission %q",
				label, name, fact.RunID, fact.Permission)
			return nil
		})

	ctx.Step(`^Alice edits "([^"]*)" to use "([^"]*)"'s profile and permission mode instead$`,
		func(c context.Context, target, donor string) error {
			w := worldFrom(c)
			j6 := j6Of(w)
			donorSpec, ok := j6.specs[donor]
			if !ok {
				return fmt.Errorf("j6: unknown agent %q", donor)
			}
			targetSpec, ok := j6.specs[target]
			if !ok {
				return fmt.Errorf("j6: unknown agent %q", target)
			}
			before := *targetSpec
			j6.beforeEdit[target] = &before
			// Mutate the TARGET's spec in place to mirror the donor's profile (and
			// therefore its bundle/server/command/secret) and permission — a
			// realistic edit (reassigning which profile an agent binding composes,
			// or its declared permission), not a synthetic field flip.
			targetSpec.Profile = donorSpec.Profile
			targetSpec.Bundle = donorSpec.Bundle
			targetSpec.Server = donorSpec.Server
			targetSpec.Command = donorSpec.Command
			targetSpec.SecretArg = donorSpec.SecretArg
			targetSpec.Permission = donorSpec.Permission
			return w.env.WriteFile(".ctxloom/config.yaml", j6RenderConfig(j6))
		})

	ctx.Step(`^the grant remembered as "([^"]*)" still shows the original MCP server and permission mode$`,
		func(c context.Context, label string) error {
			w := worldFrom(c)
			j6 := j6Of(w)
			fact, ok := j6.snapshots[label]
			if !ok {
				return fmt.Errorf("j6: no grant was ever remembered as %q", label)
			}
			orig, ok := j6.beforeEdit[fact.Agent]
			if !ok {
				return fmt.Errorf("j6: no pre-edit spec recorded for agent %q", fact.Agent)
			}
			w.docStepMaterialized = fmt.Sprintf("runs.jsonl — run_id %s (agent=%s), captured BEFORE the edit:\n  permission: %q\n  mcp_servers: %v", fact.RunID, fact.Agent, fact.Permission, fact.MCPServers)
			if fact.Permission != orig.Permission {
				return fmt.Errorf("the run remembered as %q now shows permission %q, but it was journaled as %q — a later config edit must never rewrite an already-enqueued run's record", label, fact.Permission, orig.Permission)
			}
			if !slices.Contains(fact.MCPServers, orig.Server) {
				return fmt.Errorf("the run remembered as %q no longer shows its original MCP server %q (has %v) — a later config edit must never rewrite an already-enqueued run's record", label, orig.Server, fact.MCPServers)
			}
			return nil
		})

	ctx.Step(`^the grant remembered as "([^"]*)" shows the newly edited MCP server and permission mode$`,
		func(c context.Context, label string) error {
			w := worldFrom(c)
			j6 := j6Of(w)
			fact, ok := j6.snapshots[label]
			if !ok {
				return fmt.Errorf("j6: no grant was ever remembered as %q", label)
			}
			cur, ok := j6.specs[fact.Agent]
			if !ok {
				return fmt.Errorf("j6: unknown agent %q", fact.Agent)
			}
			w.docStepMaterialized = fmt.Sprintf("runs.jsonl — run_id %s (agent=%s), captured AFTER the edit:\n  permission: %q\n  mcp_servers: %v", fact.RunID, fact.Agent, fact.Permission, fact.MCPServers)
			if fact.Permission != cur.Permission {
				return fmt.Errorf("the run remembered as %q shows permission %q, want the newly edited %q — the edit did not take effect on a fresh spawn", label, fact.Permission, cur.Permission)
			}
			if !slices.Contains(fact.MCPServers, cur.Server) {
				return fmt.Errorf("the run remembered as %q does not show the newly edited MCP server %q (has %v) — the edit did not take effect on a fresh spawn", label, cur.Server, fact.MCPServers)
			}
			return nil
		})

	ctx.Step(`^the journal carries both children's MCP server names$`,
		func(c context.Context) error {
			w := worldFrom(c)
			j6 := j6Of(w)
			raw, err := j6JournalRaw(w)
			if err != nil {
				return err
			}
			var lines []string
			var missing []string
			for _, name := range j6.order {
				s := j6.specs[name]
				lines = append(lines, fmt.Sprintf("  %s → %s: present=%t", name, s.Server, strings.Contains(raw, s.Server)))
				if !strings.Contains(raw, s.Server) {
					missing = append(missing, fmt.Sprintf("%s (%s)", name, s.Server))
				}
			}
			w.docStepMaterialized = "runs.jsonl server-name check:\n" + strings.Join(lines, "\n")
			if len(missing) > 0 {
				return fmt.Errorf("journal is missing the MCP server name(s) for: %v", missing)
			}
			return nil
		})

	ctx.Step(`^the journal never carries the command or arguments that launch them$`,
		func(c context.Context) error {
			w := worldFrom(c)
			j6 := j6Of(w)
			raw, err := j6JournalRaw(w)
			if err != nil {
				return err
			}
			var lines []string
			var leaked []string
			for _, name := range j6.order {
				s := j6.specs[name]
				lines = append(lines, fmt.Sprintf("  %s → command %q absent=%t, secret arg %q absent=%t",
					name, s.Command, !strings.Contains(raw, s.Command), s.SecretArg, !strings.Contains(raw, s.SecretArg)))
				if strings.Contains(raw, s.Command) {
					leaked = append(leaked, fmt.Sprintf("%s's command %q", name, s.Command))
				}
				if strings.Contains(raw, s.SecretArg) {
					leaked = append(leaked, fmt.Sprintf("%s's secret argument %q", name, s.SecretArg))
				}
			}
			w.docStepMaterialized = "runs.jsonl command/secret absence check:\n" + strings.Join(lines, "\n")
			if len(leaked) > 0 {
				return fmt.Errorf("SECURITY FINDING: the journal leaked what it must never carry: %v", leaked)
			}
			return nil
		})

	// --- FAILURE PATH: every scenario above is happy-path (spawn, succeed,
	// read the journal back). This flow's own coordinator verbs include a
	// shutdown edge — agent_stop — that had NO acceptance coverage at all
	// (completeness_test.go's own census is the only place the string
	// appeared). U024-F04 (this batch) fixed a real bug on exactly this
	// path: a stop landing on an already-ended run must still cancel any
	// armed relaunch, not just report "already ended" as a no-op. These two
	// scenarios are the tip-to-tail proof that the STOP verb itself behaves
	// like every other verb in this flow: idempotent on a real run,
	// refused on a fabricated one — never a silent no-op either way.

	ctx.Step(`^"([^"]*)"'s spawned session is remembered$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		j6 := j6Of(w)
		harp, ok := w.lastInner["harp"].(string)
		if !ok || harp == "" {
			return fmt.Errorf("j6: last tool result carries no harp field for %q; result:\n%s", name, w.lastTool.JSON())
		}
		j6.harps[name] = harp
		return nil
	})

	ctx.Step(`^the agent calls tool "agent_stop" for "([^"]*)"'s remembered session$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		j6 := j6Of(w)
		harp, ok := j6.harps[name]
		if !ok || harp == "" {
			return fmt.Errorf("j6: no session harp remembered for %q", name)
		}
		return callTool(c, "agent_stop", map[string]any{"harp": harp})
	})
}
