//go:build acceptance

// J002100: "a delegated child gets ITS OWN permissions and ITS OWN MCP servers —
// not the parent's, not a sibling's — and that grant is journaled, durable,
// and auditable" (j002100_delegation.feature).
//
// tests/acceptance drives a `ctxloom` SUBPROCESS over MCP stdio; it can never
// see an in-process Go struct (an earlier attempt at this journey correctly
// refused to plan around `fakeChatEngine`, which is private to a different
// package and a different process — see the plan's BLOCKED note). And unlike
// what an earlier draft of this file's OWN brief assumed, a plain `ctxloom
// mcp` subprocess never exposes an MCP tool literally named "roster" either:
// that tool is registered ONLY on a spawned session's runner-terminated
// socket (internal/mcp/mcp_runner.go's newRunnerMCPServer), which this
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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// j002100AgentSpec is one delegated child's fixture identity: which profile it
// resolves, which bundle backs that profile, the one MCP server that bundle
// ships (with a command carrying a plausible secret-shaped argument, so the
// "names never secrets" scenario has something real to prove was never
// journaled), and the permission mode its agent binding declares.
type j002100AgentSpec struct {
	Name       string
	Profile    string
	Bundle     string
	Server     string
	Command    string
	SecretArg  string
	Permission string
	// Escalation is the agent's raw `escalation:` YAML block, already
	// indented for the agent binding, or "" for none (the overwhelmingly
	// common case — an agent with no block gets the preset its permissions:
	// mode implies). Only the "nobody ever answers" scenario declares one,
	// because only there does the LADDER ITSELF have to be part of the
	// fixture: it needs a rung that parks and expires inside a test's
	// patience, which no preset provides (the presets' relay rungs wait 24h
	// by design — coord.defaultRelayTimeout).
	Escalation string
	// LLM is the config.yaml llm label this agent binds to, "" meaning the
	// shared "fast" mock. Only the "nobody ever answers" scenario names
	// another, and it has to: the mock backend is deliberately NOT in
	// coord.viaStartRunBackends, so a mock child rides the legacy go-plugin
	// Chat dial (operations.PreparedAgentChat.Start) whose ChatRequest never
	// sets ForwardPermissions — its engine's permission requests are decided
	// by the driver and never become plane-2 ApprovalRequests. The escalation
	// ladder is structurally unreachable behind a bare mock child.
	LLM string
}

// j002100State is J002100's fixture state: the two configured agents (mutable — the
// durability scenario edits one in place), a snapshot store for "remembered
// as" labels, and each agent's pre-edit spec (so a snapshot taken before an
// edit can still be checked against what it USED to be).
type j002100State struct {
	order      []string // agent names, declaration order
	specs      map[string]*j002100AgentSpec
	beforeEdit map[string]*j002100AgentSpec // agent name -> its spec as captured just before an edit
	snapshots  map[string]j002100RunFact    // "remembered as" label -> captured journal fact
	harps      map[string]string            // agent name -> its most recently spawned session harp
	// extraLLMConfigs is any additional `llm.configs:` YAML (already
	// indented) a scenario needed beyond the shared "fast" mock. Held on the
	// state, not passed per call, so every LATER re-render — the durability
	// scenario rewrites config.yaml mid-scenario — keeps it.
	extraLLMConfigs string
}

// j002100RunFact is runEnqueued's (coord/facts.go) payload, decoded straight off
// runs.jsonl — the exact fields Wave F1 added so a child's resolved grant is
// auditable, not just provable in-process.
type j002100RunFact struct {
	RunID      string   `json:"run_id"`
	Harp       string   `json:"harp"`
	Agent      string   `json:"agent"`
	Permission string   `json:"permission"`
	MCPServers []string `json:"mcp_servers"`
}

// j002100JournalLine is one runs.jsonl line's outer envelope (coord/facts.go's
// journal.Fact shape: {kind, at, data}); only "kind" is inspected here to
// find run.enqueued lines, "data" decodes into j002100RunFact.
type j002100JournalLine struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

func j002100Of(w *World) *j002100State {
	if w.j002100 == nil {
		w.j002100 = &j002100State{
			specs:      map[string]*j002100AgentSpec{},
			beforeEdit: map[string]*j002100AgentSpec{},
			snapshots:  map[string]j002100RunFact{},
			harps:      map[string]string{},
		}
	}
	return w.j002100
}

// j002100RenderConfig renders config.yaml for the current spec state: one "fast"
// mock LLM label (the same built-in gRPC mock plugin every other journey's
// primary engine already uses — real production code, not a test-only
// fixture), plus the extra labels any spec named, and one agent binding per
// declared name, in declaration order (map iteration order is not stable;
// j002100.order pins it).
// j002100JoinEntries renders assistant entries for a failure message, so a
// scenario that finds no verdict shows what the child DID say instead of
// leaving the reader to go dig the transcript out by hand.
func j002100JoinEntries(entries []j002300TranscriptEntry) string {
	var b strings.Builder
	for i, e := range entries {
		fmt.Fprintf(&b, "  [%d] %s\n", i, e.Content)
	}
	return b.String()
}

func j002100RenderConfig(j002100 *j002100State) string {
	var b strings.Builder
	b.WriteString("version: 4\nllm:\n  configs:\n    fast:\n      type: mock\n")
	b.WriteString(j002100.extraLLMConfigs)
	b.WriteString("  defaults:\n    primary: fast\n    fast: fast\nagents:\n")
	for _, name := range j002100.order {
		s := j002100.specs[name]
		label := s.LLM
		if label == "" {
			label = "fast"
		}
		fmt.Fprintf(&b, "  %s:\n    llm: %s\n    profiles:\n      - %s\n    permissions: %s\n", name, label, s.Profile, s.Permission)
		b.WriteString(s.Escalation)
	}
	return b.String()
}

// j002100BundleYAML renders one agent's backing bundle: a single MCP server whose
// command carries one plausible secret-shaped argument.
func j002100BundleYAML(s *j002100AgentSpec) string {
	return fmt.Sprintf("version: \"1.0.0\"\nmcp:\n  %s:\n    command: %s\n    args: [%q]\n", s.Server, s.Command, s.SecretArg)
}

// j002100ProfileYAML renders one agent's profile: the one bundle that backs it.
func j002100ProfileYAML(s *j002100AgentSpec) string {
	return fmt.Sprintf("bundles:\n  - ctxloom:local@bundles/%s\n", s.Bundle)
}

// j002100JournalRaw reads the coordinator's run-registry journal straight off
// disk: ~/.ctxloom/coord/<project-key>/runs.jsonl under this scenario's
// isolated HOME (coord/statedir.go's documented layout). The project key is
// not recomputed here — a fresh isolated HOME holds exactly one coordinator's
// state, so a glob finds it without needing to reproduce
// taskops.ResolveProjectIdentity's key derivation.
func j002100JournalRaw(w *World) (string, error) {
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

// j002100AllFactsForAgent parses every run.enqueued fact for one agent name, in
// journal (append) order.
// j002100AllFactsForAgent parses run.enqueued facts for one agent. skipped and
// firstSkipErr report any journal line that failed to unmarshal, or whose
// run.enqueued payload (Data) failed to decode (these used to be
// silently `continue`d past uncounted, so a schema-drifted journal read as
// "no journaled run.enqueued fact found for agent X" -- indistinguishable
// from a child that genuinely never spawned).
func j002100AllFactsForAgent(w *World, agent string) (out []j002100RunFact, skipped int, firstSkipErr error, err error) {
	raw, err := j002100JournalRaw(w)
	if err != nil {
		return nil, 0, nil, err
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var jl j002100JournalLine
		if uerr := json.Unmarshal([]byte(line), &jl); uerr != nil {
			skipped++
			if firstSkipErr == nil {
				firstSkipErr = fmt.Errorf("journal line %q: %w", line, uerr)
			}
			continue
		}
		if jl.Kind != "run.enqueued" {
			continue
		}
		var f j002100RunFact
		if uerr := json.Unmarshal(jl.Data, &f); uerr != nil {
			skipped++
			if firstSkipErr == nil {
				firstSkipErr = fmt.Errorf("run.enqueued data %q: %w", string(jl.Data), uerr)
			}
			continue
		}
		if f.Agent != agent {
			continue
		}
		out = append(out, f)
	}
	return out, skipped, firstSkipErr, nil
}

// j002100LatestFactForAgent returns the MOST RECENT run.enqueued fact for agent —
// the only one, in every scenario except the durability scenario, which
// spawns "fixer" twice and relies on "remembered as" snapshots (below) to
// keep the first one reachable once a second exists.
func j002100LatestFactForAgent(w *World, agent string) (*j002100RunFact, error) {
	facts, skipped, skipErr, err := j002100AllFactsForAgent(w, agent)
	if err != nil {
		return nil, err
	}
	if len(facts) == 0 {
		if skipped > 0 {
			return nil, fmt.Errorf("no journaled run.enqueued fact found for agent %q -- and %d journal line(s) failed to parse (first: %v), so this may be a schema-drifted journal, not a child that never spawned", agent, skipped, skipErr)
		}
		return nil, fmt.Errorf("no journaled run.enqueued fact found for agent %q", agent)
	}
	return &facts[len(facts)-1], nil
}

func registerJ002100Steps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Alice's coordinator can delegate to two agents, "([^"]*)" and "([^"]*)", each with its own profile, its own MCP server, and its own permission mode$`,
		func(c context.Context, nameA, nameB string) error {
			w := worldFrom(c)
			j002100 := j002100Of(w)
			j002100.order = []string{nameA, nameB}
			// Two headless-safe permission modes (agent.PermissionMode.SafeHeadless:
			// bypass|plan) so the "recorded, not just implied" scenario has two
			// genuinely different values to tell apart, not one value asserted twice.
			j002100.specs[nameA] = &j002100AgentSpec{
				Name: nameA, Profile: "review-profile", Bundle: "bundle-review",
				Server: "docs-lookup", Command: "docs-server", SecretArg: "DOCS-SECRET-7e1d44",
				Permission: "plan",
			}
			j002100.specs[nameB] = &j002100AgentSpec{
				Name: nameB, Profile: "fix-profile", Bundle: "bundle-fix",
				Server: "deploy-tool", Command: "deploy-cli", SecretArg: "DEPLOY-SECRET-9f2c81",
				Permission: "bypass",
			}
			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/content/bundles/bundle-review.yaml", j002100BundleYAML(j002100.specs[nameA])); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/content/bundles/bundle-fix.yaml", j002100BundleYAML(j002100.specs[nameB])); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/profiles/review-profile.yaml", j002100ProfileYAML(j002100.specs[nameA])); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/profiles/fix-profile.yaml", j002100ProfileYAML(j002100.specs[nameB])); err != nil {
				return err
			}
			return w.env.WriteFile(".ctxloom/config.yaml", j002100RenderConfig(j002100))
		})

	// --- The "nobody ever decided" fixture (wiry-judge) ---------------------
	//
	// A THIRD agent, added on top of the Background's two, whose escalation
	// ladder is ONE relay_to_role rung with a short timeout and nothing
	// beneath it. The coordinator in this harness never answers the relayed
	// approval_request (no step drains it), so the rung expires, the ladder
	// runs out, and the request reaches the bottom with NO rung and no human
	// having resolved it — the exact "ctxloom refused, not the operator"
	// shape. It reuses the Background's review-profile rather than minting
	// another bundle: this scenario asserts the APPROVAL ANSWER, and the
	// child's composed context is irrelevant to it.
	//
	// WHY THE ENGINE IS `type: acp` DRIVING CTXLOOM'S OWN `acp serve`, and
	// not the plain mock every other scenario here uses. Only a backend in
	// coord.viaStartRunBackends rides the StartRun path, and only that path
	// builds its ChatRequest from coord.harnessSpec — the one place
	// ForwardPermissions is set. `mock` is deliberately excluded from that
	// allowlist, so a bare mock child's permission requests never become
	// plane-2 ApprovalRequests and the escalation ladder is structurally
	// unreachable behind one. `acp` IS in the allowlist, and ctxloom's own
	// `acp serve` speaks ACP over stdio with the project's configured engine
	// — the mock — behind it (tests/integration/acp_agent_test.go drives the
	// same pair). So the full production chain runs here: mock raises a
	// permission -> `acp serve` forwards it as session/request_permission ->
	// the child runner's ACP driver forwards it to the coordinator -> the
	// ladder decides -> the answer travels all the way back and the mock
	// reports the verdict it received. Real subprocesses, real wire, no
	// test-only backend anywhere in it.
	ctx.Step(`^Alice's coordinator can also delegate to "([^"]*)", whose escalation ladder relays to a parent that never answers$`,
		func(c context.Context, name string) error {
			w := worldFrom(c)
			j002100 := j002100Of(w)
			j002100.order = append(j002100.order, name)
			j002100.extraLLMConfigs = fmt.Sprintf("    forwarding:\n      type: acp\n      command: %q\n", w.env.AppBinary+" acp serve")
			j002100.specs[name] = &j002100AgentSpec{
				Name: name, Profile: "review-profile", Bundle: "bundle-review",
				Server: "docs-lookup", Command: "docs-server", SecretArg: "DOCS-SECRET-7e1d44",
				LLM: "forwarding",
				// plan, not bypass: bypass auto-accepts every kind at the
				// first rung and no ladder is ever walked. The explicit
				// escalation block REPLACES the plan preset entirely
				// (coord.buildLadder), so this agent's whole ladder is the
				// one rung below.
				Permission: "plan",
				Escalation: "    escalation:\n      - action: relay_to_role\n        role: parent\n        timeout: 2s\n",
			}
			return w.env.WriteFile(".ctxloom/config.yaml", j002100RenderConfig(j002100))
		})

	// The child's own canonical transcript is the observable, for the reason
	// this file's header gives for runs.jsonl: a durable, external, on-disk
	// artifact this harness can honestly read, never an in-process struct.
	// The reader is j002300's (steps_j002300_cross_engine_delegation.go) —
	// same package, same file format, already hardened against a corrupted or
	// slow-to-appear transcript; duplicating it here would be two copies of
	// one parser drifting apart.
	//
	// The VERDICT STRING is what makes this a payload assertion rather than
	// an exit-code one. The mock engine's permission turn (internal/lm/
	// backends/mock_chat.go's chatPermissionTurn) reports the answer it
	// actually received: "granted" for an allow option, "denied" for a reject
	// option, "dismissed" for the empty OptionID that IS the ACP
	// {outcome:"cancelled"} reply. So the assertion reads the engine's own
	// account of what ctxloom told it — "denied" here would be the defect
	// wiry-judge fixed, in the engine's own words.
	ctx.Step(`^"([^"]*)"'s reported turn records the permission verdict "([^"]*)"$`,
		func(c context.Context, name, verdict string) error {
			w := worldFrom(c)
			j002100 := j002100Of(w)
			harp, ok := j002100.harps[name]
			if !ok || harp == "" {
				return fmt.Errorf("j002100: no session harp remembered for %q — spawn it first", name)
			}
			// TWO assistant entries, not one: every ACP session opens with
			// ctxloom's always-on isolation-posture announcement, which is
			// assistant-shaped and indistinguishable from content at this
			// seam (tests/integration/acp_agent_test.go documents the same
			// offset). The verdict rides the entry AFTER it, so the search
			// below scans them all for the mock's verdict line rather than
			// counting positions.
			assistants, err := j002300TranscriptAssistantCount(w, harp, 2)
			if err != nil {
				return err
			}
			const verdictPrefix = "mock chat: permission "
			var reported string
			for _, e := range assistants {
				if i := strings.Index(e.Content, verdictPrefix); i >= 0 {
					reported = strings.TrimSpace(e.Content[i:])
					break
				}
			}
			w.docStepMaterialized = fmt.Sprintf("%s — %s's reported permission verdict:\n  %s", j002300TranscriptPath(w, harp), name, reported)
			if reported == "" {
				return fmt.Errorf("%s never reported a permission verdict at all (no %q entry) — the engine's permission request may never have been raised or never answered; entries:\n%s",
					name, verdictPrefix, j002100JoinEntries(assistants))
			}
			if want := verdictPrefix + verdict; reported != want {
				return fmt.Errorf("%s reported %q, want %q — the engine was told a DIFFERENT thing about the permission it asked for", name, reported, want)
			}
			return nil
		})

	ctx.Step(`^"([^"]*)"'s journaled grant carries its own MCP server and not "([^"]*)"'s$`,
		func(c context.Context, self, other string) error {
			w := worldFrom(c)
			j002100 := j002100Of(w)
			selfSpec, ok := j002100.specs[self]
			if !ok {
				return fmt.Errorf("j002100: unknown agent %q", self)
			}
			otherSpec, ok := j002100.specs[other]
			if !ok {
				return fmt.Errorf("j002100: unknown agent %q", other)
			}
			fact, err := j002100LatestFactForAgent(w, self)
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
			j002100 := j002100Of(w)
			spec, ok := j002100.specs[name]
			if !ok {
				return fmt.Errorf("j002100: unknown agent %q", name)
			}
			fact, err := j002100LatestFactForAgent(w, name)
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
			factSelf, err := j002100LatestFactForAgent(w, name)
			if err != nil {
				return err
			}
			factOther, err := j002100LatestFactForAgent(w, other)
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
			j002100 := j002100Of(w)
			fact, err := j002100LatestFactForAgent(w, name)
			if err != nil {
				return err
			}
			j002100.snapshots[label] = *fact
			// Real evidence: the actual journaled fact this step just snapshot
			// off runs.jsonl, not a restatement of "it was remembered".
			w.docStepMaterialized = fmt.Sprintf("runs.jsonl — %q snapshot of %s: run_id %s, permission %q",
				label, name, fact.RunID, fact.Permission)
			return nil
		})

	ctx.Step(`^Alice edits "([^"]*)" to use "([^"]*)"'s profile and permission mode instead$`,
		func(c context.Context, target, donor string) error {
			w := worldFrom(c)
			j002100 := j002100Of(w)
			donorSpec, ok := j002100.specs[donor]
			if !ok {
				return fmt.Errorf("j002100: unknown agent %q", donor)
			}
			targetSpec, ok := j002100.specs[target]
			if !ok {
				return fmt.Errorf("j002100: unknown agent %q", target)
			}
			before := *targetSpec
			j002100.beforeEdit[target] = &before
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
			return w.env.WriteFile(".ctxloom/config.yaml", j002100RenderConfig(j002100))
		})

	ctx.Step(`^the grant remembered as "([^"]*)" still shows the original MCP server and permission mode$`,
		func(c context.Context, label string) error {
			w := worldFrom(c)
			j002100 := j002100Of(w)
			fact, ok := j002100.snapshots[label]
			if !ok {
				return fmt.Errorf("j002100: no grant was ever remembered as %q", label)
			}
			orig, ok := j002100.beforeEdit[fact.Agent]
			if !ok {
				return fmt.Errorf("j002100: no pre-edit spec recorded for agent %q", fact.Agent)
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
			j002100 := j002100Of(w)
			fact, ok := j002100.snapshots[label]
			if !ok {
				return fmt.Errorf("j002100: no grant was ever remembered as %q", label)
			}
			cur, ok := j002100.specs[fact.Agent]
			if !ok {
				return fmt.Errorf("j002100: unknown agent %q", fact.Agent)
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
			j002100 := j002100Of(w)
			raw, err := j002100JournalRaw(w)
			if err != nil {
				return err
			}
			var lines []string
			var missing []string
			for _, name := range j002100.order {
				s := j002100.specs[name]
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
			j002100 := j002100Of(w)
			raw, err := j002100JournalRaw(w)
			if err != nil {
				return err
			}
			var lines []string
			var leaked []string
			for _, name := range j002100.order {
				s := j002100.specs[name]
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
	// appeared). This fixed a real bug on exactly this
	// path: a stop landing on an already-ended run must still cancel any
	// armed relaunch, not just report "already ended" as a no-op. These two
	// scenarios are the tip-to-tail proof that the STOP verb itself behaves
	// like every other verb in this flow: idempotent on a real run,
	// refused on a fabricated one — never a silent no-op either way.

	ctx.Step(`^"([^"]*)"'s spawned session is remembered$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		j002100 := j002100Of(w)
		harp, ok := w.lastInner["harp"].(string)
		if !ok || harp == "" {
			return fmt.Errorf("j002100: last tool result carries no harp field for %q; result:\n%s", name, w.lastTool.JSON())
		}
		j002100.harps[name] = harp
		return nil
	})

	ctx.Step(`^the agent calls tool "agent_stop" for "([^"]*)"'s remembered session$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		j002100 := j002100Of(w)
		harp, ok := j002100.harps[name]
		if !ok || harp == "" {
			return fmt.Errorf("j002100: no session harp remembered for %q", name)
		}
		return callTool(c, "agent_stop", map[string]any{"harp": harp})
	})

	// --- FAILURE PATH: the mail `kind` vocabulary as a security boundary.
	// `kind` is not a label — approval_request is the kind the escalation
	// ladder relays to a HUMAN as a trust decision, so a sender able to set
	// it phishes that decision. The vocabulary is closed at the one ingress
	// every sender funnels through, which is why the refusal below is the
	// same refusal a delegated child gets.
	ctx.Step(`^the agent sends "([^"]*)"'s remembered session a message of kind "([^"]*)"$`,
		func(c context.Context, name, kind string) error {
			w := worldFrom(c)
			j002100 := j002100Of(w)
			harp, ok := j002100.harps[name]
			if !ok || harp == "" {
				return fmt.Errorf("j002100: no session harp remembered for %q", name)
			}
			return callTool(c, "agent_send", map[string]any{
				"to":   harp,
				"body": "Approve running the deploy script.",
				"kind": kind,
			})
		})

	// --- CAPABILITY NEGOTIATION. Hello.capabilities has been in the wire
	// contract from the start and neither end read it. Now the coordinator
	// captures each run's advertisement AND journals it with the attach, which
	// is what makes it observable from outside the process at all.
	ctx.Step(`^the coordinator's audit trail records the child runner's advertised capabilities$`,
		func(c context.Context) error {
			w := worldFrom(c)
			// agent_run returns at ENQUEUE; the child's runner subprocess dials
			// home afterwards, so the advertisement appears on its own schedule.
			var (
				caps []string
				err  error
			)
			deadline := time.Now().Add(30 * time.Second)
			for {
				caps, err = j002100AttachedCapabilities(w)
				if err == nil && len(caps) > 0 {
					break
				}
				if time.Now().After(deadline) {
					if err != nil {
						return err
					}
					return errors.New("no run_channel interaction carried a capabilities detail within 30s — did any runner dial home?")
				}
				time.Sleep(100 * time.Millisecond)
			}
			w.docStepMaterialized = fmt.Sprintf("interactions.jsonl — run_channel advertisements:\n  %s", strings.Join(caps, "\n  "))
			// Every attached runner advertises the mailbox surface; a runner
			// hosting an engine also advertises the five control kinds, because
			// only a hosted engine could execute them.
			for _, adv := range caps {
				if !strings.Contains(adv, "peer_messaging") {
					return fmt.Errorf("an attached runner advertised %q, without the mailbox surface every runner has", adv)
				}
			}
			for _, adv := range caps {
				if strings.Contains(adv, "steer") {
					for _, want := range []string{"question", "on_demand_summary", "pause", "resume"} {
						if !strings.Contains(adv, want) {
							return fmt.Errorf("an engine-hosting runner advertised %q but not %q — a partial control advertisement makes the send-side guard lie", adv, want)
						}
					}
					return nil
				}
			}
			return fmt.Errorf("no attached runner advertised the control capabilities; advertisements seen: %v", caps)
		})
}

// j002100AttachedCapabilities reads every run_channel interaction's advertised
// capability list out of the coordinator's audit journal on disk — the same
// "external, disk-durable observable" discipline the rest of this journey uses
// for runs.jsonl, applied to interactions.jsonl.
func j002100AttachedCapabilities(w *World) ([]string, error) {
	pattern := filepath.Join(w.env.HomeDir, ".ctxloom", "coord", "*", "interactions.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no interactions.jsonl found under %q", pattern)
	}
	var out []string
	for _, m := range matches {
		data, rerr := os.ReadFile(m)
		if rerr != nil {
			return nil, fmt.Errorf("read %s: %w", m, rerr)
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var jl j002100JournalLine
			if json.Unmarshal([]byte(line), &jl) != nil || jl.Kind != "interaction" {
				continue
			}
			var in struct {
				Kind   string            `json:"kind"`
				Detail map[string]string `json:"detail"`
			}
			if json.Unmarshal(jl.Data, &in) != nil || in.Kind != "run_channel" {
				continue
			}
			if adv := in.Detail["capabilities"]; adv != "" {
				out = append(out, adv)
			}
		}
	}
	return out, nil
}
