package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// LoadAgents returns every locally-defined agent — the `agents:` config key
// (c.agents), sorted by name. That key is the ONE source: agents are never
// shipped in bundles or remotes, and there is no per-agent file directory
// either, so every binding is schema-validated with the rest of config.yaml
// and refused at the write edge (operations.validateAgentAxes) rather than
// degraded at launch.
//
// A .ctxloom/agents directory holding definitions is not a source and is not
// quietly skipped either — see retiredAgentsDirSignpost.
//
// Every returned Agent is cloned, so it obeys accessors.go's copy-on-read
// policy like every other value handed out of a Config: Agent.Profiles decides
// which context a delegated child is given, so a caller filtering it in place
// must not be rewriting it for every other holder of the shared instance.
func (c *Config) LoadAgents() []agents.Agent {
	c.retiredAgentsDirSignpost()
	c.retiredEscalationSignpost()

	out := make([]agents.Agent, 0, len(c.agents))
	for name, sub := range c.agents {
		sub = cloneAgent(sub)
		sub.Name = name
		out = append(out, sub)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Agent returns the declared agent definition for name.
func (c *Config) Agent(name string) (agents.Agent, bool) {
	for _, sub := range c.LoadAgents() {
		if sub.Name == name {
			return sub, true
		}
	}
	return agents.Agent{}, false
}

// retiredAgentsDirSignpost records a fatal ClassMigration finding when
// .ctxloom/agents holds agent definitions. Nothing reads that directory — the
// `agents:` config key is the only source — so every file under it is inert.
// Ignoring them silently would make a user's own bindings vanish from
// `agent list`, `run --agent` and `default_agent` with no explanation, so the
// move is demanded, not performed: ctxloom does not rewrite content it did not
// author in this run.
//
// FailOnce, because Agent(name) re-runs LoadAgents on every lookup and one
// command reaches it several times (ResolveAgent, DefaultAgentProfiles,
// `agent show`), so the finding must not stack up inside one startup window.
func (c *Config) retiredAgentsDirSignpost() {
	fs := c.getFS()
	for _, appPath := range c.appPaths {
		dir := paths.AgentsPath(appPath)
		stranded := strandedAgentFiles(fs, dir)
		if len(stranded) == 0 {
			continue
		}
		strictness.FailOnce(strictness.ClassMigration,
			fmt.Sprintf("move each binding under the `agents:` key of %s, then delete %s",
				paths.ConfigPath(appPath), dir),
			"%s holds %d agent definition(s) (%s) but agents now live only under the `agents:` key of config.yaml — that directory is no longer read, so these bindings are invisible to `agent list`, `run --agent` and `default_agent`",
			dir, len(stranded), strings.Join(stranded, ", "))
	}
}

// retiredEscalationSignpost records a finding for every agent binding that
// still declares an `escalation:` ladder.
//
// The orchestrator-routed approval ladder was removed: a human answers the
// ENGINE'S OWN permission prompt in its tmux window instead of ctxloom
// brokering a second approval UI. The key survives on agents.Agent — it still
// parses, still normalises, and `agent show` still prints "escalation: N
// rung(s)" — but NOTHING consumes it any more.
//
// Left unsaid that is worse than a silent no-op: the CLI actively CONFIRMS a
// setting that has no effect, which is this project's characteristic defect
// wearing a success message. Same shape and same reason as
// retiredAgentsDirSignpost above, FailOnce included: Agent(name) re-runs
// LoadAgents on every lookup, so one command reaches this several times and the
// finding must not stack up inside a single startup window.
func (c *Config) retiredEscalationSignpost() {
	// Sorted so a config with several offenders reports them in a stable
	// order, matching LoadAgents' own name ordering.
	names := make([]string, 0, len(c.agents))
	for name := range c.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if len(c.agents[name].Escalation) == 0 {
			continue
		}
		strictness.FailOnce(strictness.ClassMigration,
			fmt.Sprintf("delete the `escalation:` key from agent %q", name),
			"agent %q declares an `escalation:` ladder (%d rung(s)), but the orchestrator-routed approval ladder was removed — the key is still parsed and still shown by `agent show`, yet nothing reads it, so the rungs have no effect on what that agent may do",
			name, len(c.agents[name].Escalation))
	}
}

// dropEnginelessAgents refuses every `agents:` entry in ONE decoded config
// layer that declares neither an llm nor profiles, removing it from values in
// place so it never reaches the layer merge, and returns one finding per
// refusal naming the key (agents.<name>) and the file it came from.
//
// It runs per LAYER, deliberately: checked on the MERGED map instead, the
// finding could only name the project's path for a declaration that lives in
// home, and the shell would already be in the view Manager.Update saves back
// into the project file — which is how a home-only `help: {}` came to be
// re-serialised into a committed config. The layer-scope check cannot catch
// it: every per-agent FIELD is ScopeShared, so a home agent declaring any
// field is dropped there (koanf prunes the emptied parent too), but a
// verbatim `{}` has no field for that check to see.
//
// An llm alone is a complete binding (the context is the project default's)
// and profiles alone are too (they carry the llm, falling back to the project
// default backend — see agents.Agent), so only the pair-absent case is
// refused. Whether the llm label or a profile actually exists is resolved
// later, by operations.ResolveAgent, exactly as before.
func dropEnginelessAgents(configPath string, values map[string]any) []Warning {
	agentsMap, ok := values["agents"].(map[string]any)
	if !ok {
		return nil
	}
	// Sorted so a layer with several offenders reports them in a stable order.
	names := make([]string, 0, len(agentsMap))
	for name := range agentsMap {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []Warning
	for _, name := range names {
		body, _ := agentsMap[name].(map[string]any)
		if bindsEngine(body) {
			continue
		}
		delete(agentsMap, name)
		out = append(out, Warning{Kind: WarnKindEnginelessAgent, Text: fmt.Sprintf(
			"agents.%s in %s declares neither an llm nor profiles, so nothing could resolve an engine for it — an agent bound to nothing is not an agent; the entry is ignored",
			name, configPath)})
	}
	if len(agentsMap) == 0 {
		delete(values, "agents")
	}
	return out
}

// bindsEngine reports whether a raw agent body names an llm or at least one
// profile. A non-string llm or non-list profiles is a schema violation
// reported separately; here it counts as absent, not as a binding.
func bindsEngine(body map[string]any) bool {
	if llm, ok := body["llm"].(string); ok && llm != "" {
		return true
	}
	if profiles, ok := body["profiles"].([]any); ok && len(profiles) > 0 {
		return true
	}
	return false
}

// strandedAgentFiles returns the base names of the YAML files under dir — the
// definitions a user wrote where nothing reads them. A dir that is absent or
// unreadable yields nothing: the signpost only ever fires on something it can
// actually see and name.
func strandedAgentFiles(fs afero.Fs, dir string) []string {
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if name := e.Name(); strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
