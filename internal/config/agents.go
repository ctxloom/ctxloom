package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/paths"
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
// Same shape and same reason as legacyCacheBundlesSignpost, the other "this
// location is no longer read" gate — including FailOnce, because Agent(name)
// re-runs LoadAgents on every lookup and one command reaches it several times
// (ResolveAgent, DefaultAgentProfiles, `agent show`), so the finding must not
// stack up inside one startup window.
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
