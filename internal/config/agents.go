package config

import (
	"sort"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// agentDirLoader builds the directory-source loader for the project's
// .ctxloom/agents/*.yaml definitions, threading the injected filesystem so
// reads honor c.fs (matching GetProfileLoader). Returns nil only when there is
// no app path to anchor a directory.
func (c *Config) agentDirLoader() *agents.Loader {
	dirs := agents.GetAgentDirs(c.fs, c.AppPaths)
	var opts []agents.LoaderOption
	if c.fs != nil {
		opts = append(opts, agents.WithFS(c.fs))
	}
	return agents.NewLoader(dirs, opts...)
}

// LoadAgents returns every locally-defined agent, merged from the two
// LOCAL sources — the `agents:` config key (c.Agents) and the
// .ctxloom/agents/*.yaml directory — sorted by name. There is, deliberately,
// no third source: agents are never shipped in bundles or remotes.
//
// On a name collision the config-key entry wins (it is the "closer", explicitly
// version-controlled-with-config form) and a warning names the shadowed file —
// per fault tolerance the merge never errors. Each returned Agent carries its
// Name and Source.
func (c *Config) LoadAgents() []agents.Agent {
	merged := make(map[string]agents.Agent, len(c.Agents))

	// Config-key entries first — they are authoritative on collision.
	for name, sub := range c.Agents {
		sub.Name = name
		sub.Source = agents.SourceConfig
		merged[name] = sub
	}

	// Directory entries fill in the rest; a name already claimed by the config
	// key is shadowed (warn, keep config).
	if dir := c.agentDirLoader(); dir != nil {
		list, err := dir.List()
		if err != nil {
			clidiag.Warn("ctxloom", "failed to scan local agents: %v", err)
		}
		for _, sub := range list {
			if _, clash := merged[sub.Name]; clash {
				clidiag.Warn("ctxloom",
					"agent %q is defined in both config.yaml and %s; using the config.yaml definition",
					sub.Name, sub.Source)
				continue
			}
			merged[sub.Name] = *sub
		}
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
