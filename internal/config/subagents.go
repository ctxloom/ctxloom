package config

import (
	"sort"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/subagents"
)

// subagentDirLoader builds the directory-source loader for the project's
// .ctxloom/subagents/*.yaml definitions, threading the injected filesystem so
// reads honor c.fs (matching GetProfileLoader). Returns nil only when there is
// no app path to anchor a directory.
func (c *Config) subagentDirLoader() *subagents.Loader {
	dirs := subagents.GetSubagentDirs(c.AppPaths)
	var opts []subagents.LoaderOption
	if c.fs != nil {
		opts = append(opts, subagents.WithFS(c.fs))
	}
	return subagents.NewLoader(dirs, opts...)
}

// LoadSubagents returns every locally-defined subagent, merged from the two
// LOCAL sources — the `subagents:` config key (c.Subagents) and the
// .ctxloom/subagents/*.yaml directory — sorted by name. There is, deliberately,
// no third source: subagents are never shipped in bundles or remotes.
//
// On a name collision the config-key entry wins (it is the "closer", explicitly
// version-controlled-with-config form) and a warning names the shadowed file —
// per fault tolerance the merge never errors. Each returned Subagent carries its
// Name and Source.
func (c *Config) LoadSubagents() []subagents.Subagent {
	merged := make(map[string]subagents.Subagent, len(c.Subagents))

	// Config-key entries first — they are authoritative on collision.
	for name, sub := range c.Subagents {
		sub.Name = name
		sub.Source = subagents.SourceConfig
		merged[name] = sub
	}

	// Directory entries fill in the rest; a name already claimed by the config
	// key is shadowed (warn, keep config).
	if dir := c.subagentDirLoader(); dir != nil {
		list, err := dir.List()
		if err != nil {
			clidiag.Warn("ctxloom", "failed to scan local subagents: %v", err)
		}
		for _, sub := range list {
			if _, clash := merged[sub.Name]; clash {
				clidiag.Warn("ctxloom",
					"subagent %q is defined in both config.yaml and %s; using the config.yaml definition",
					sub.Name, sub.Source)
				continue
			}
			merged[sub.Name] = *sub
		}
	}

	out := make([]subagents.Subagent, 0, len(merged))
	for _, sub := range merged {
		out = append(out, sub)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Subagent returns the merged subagent definition for name, applying the same
// config-key-wins-over-directory precedence as LoadSubagents.
func (c *Config) Subagent(name string) (subagents.Subagent, bool) {
	for _, sub := range c.LoadSubagents() {
		if sub.Name == name {
			return sub, true
		}
	}
	return subagents.Subagent{}, false
}
