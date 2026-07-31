package config

import (
	"sort"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// agentDirLoader builds the directory-source loader for the project's
// .ctxloom/agents/*.yaml definitions, threading the injected filesystem so
// reads honor c.fs (matching GetProfileLoader). It never returns nil: with no
// app paths GetAgentDirs yields an empty slice and the loader's List() returns
// (nil, nil), which is the correct empty case one level down.
func (c *Config) agentDirLoader() *agents.Loader {
	dirs := agents.GetAgentDirs(c.fs, c.appPaths)
	return agents.NewLoader(dirs, c.fs)
}

// LoadAgents returns every locally-defined agent, merged from the two
// LOCAL sources — the `agents:` config key (c.agents) and the
// .ctxloom/agents/*.yaml directory — sorted by name. There is, deliberately,
// no third source: agents are never shipped in bundles or remotes.
//
// On a name collision the config-key entry wins (it is the "closer", explicitly
// version-controlled-with-config form) and a warning names the shadowed file —
// per fault tolerance the merge never errors. Each returned Agent carries its
// Name and Source.
//
// Every returned Agent is cloned, so it obeys accessors.go's copy-on-read
// policy like every other value handed out of a Config: Agent.Profiles decides
// which context a delegated child is given and Agent.Escalation is the
// permission ladder it runs under, so a caller filtering either in place must
// not be rewriting it for every other holder of the shared instance.
func (c *Config) LoadAgents() []agents.Agent {
	merged := make(map[string]agents.Agent, len(c.agents))

	// Config-key entries first — they are authoritative on collision.
	for name, sub := range c.agents {
		sub = cloneAgent(sub)
		sub.Name = name
		sub.Source = agents.SourceConfig
		merged[name] = sub
	}

	// Directory entries fill in the rest; a name already claimed by the config
	// key is shadowed (warn, keep config).
	list, err := c.agentDirLoader().List()
	if err != nil {
		// A failed scan is NOT an empty one. The merge continues on whatever
		// the config key supplied — fault tolerance — but the result is now a
		// fatal-class finding rather than a stderr line, because "no such
		// agent" for an agent that is defined and merely unreadable is
		// undiagnosable from the outside. The startup choke owners abort on
		// it; management commands surface it and proceed.
		strictness.Fail(strictness.ClassConfig,
			"make .ctxloom/agents readable, or remove it if it is not yours",
			"failed to scan local agents: %v — every agent defined on disk is missing from this run", err)
	}
	for _, sub := range list {
		if _, clash := merged[sub.Name]; clash {
			// WarnOnce, not Warn: this states a fact about the user's config,
			// not an event. Agent(name) re-runs this whole merge per lookup and
			// one command reaches it several times (ResolveAgent,
			// DefaultAgentProfiles, `agent show`), so a per-call Warn printed
			// the same unactionable line once per lookup.
			clidiag.WarnOnce("ctxloom",
				"agent %q is defined in both config.yaml and %s; using the config.yaml definition",
				sub.Name, sub.Source)
			continue
		}
		merged[sub.Name] = *sub
	}

	out := make([]agents.Agent, 0, len(merged))
	for _, sub := range merged {
		out = append(out, sub)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Agent returns the merged agent definition for name, applying the same
// config-key-wins-over-directory precedence as LoadAgents.
func (c *Config) Agent(name string) (agents.Agent, bool) {
	for _, sub := range c.LoadAgents() {
		if sub.Name == name {
			return sub, true
		}
	}
	return agents.Agent{}, false
}
