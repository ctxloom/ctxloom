package agent

import (
	"os"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// ManagedSurfaces are the package's surfaces beside the engine's exports:
// what the composite package carries for the writers, named here so this
// package (linked by the lean binaries) never imports the package model.
type ManagedSurfaces struct {
	Hooks      wire.HooksConfig
	MCP        map[string]wire.MCPServer
	DenyTools  []string
	Statusline bool
}

// ManagedConfigFor is the ONE projection of a decoded package's surfaces and
// the engine's exports over it onto the managed payload today's writers
// deliver: the command and skill exports as that engine decided them, the
// hooks, the servers, the deny list and the statusline. The originator (the
// plugin arm) and the runner both build the payload here, which is what
// makes a host launch and a delegated launch deliver the same set.
func ManagedConfigFor(surfaces ManagedSurfaces, exports engine.Exports) *ManagedConfig {
	hooks := surfaces.Hooks
	return &ManagedConfig{
		Commands:         CommandExportsOf(exports),
		Skills:           SkillExportsOf(exports),
		Hooks:            &hooks,
		BundleMCP:        surfaces.MCP,
		ManageStatusline: surfaces.Statusline,
		DenyTools:        surfaces.DenyTools,
	}
}

// CommandExportsOf is the engine's command exports in the writers' shape.
func CommandExportsOf(exports engine.Exports) []CommandExport {
	if len(exports.Commands) == 0 {
		return nil
	}
	out := make([]CommandExport, 0, len(exports.Commands))
	for _, c := range exports.Commands {
		out = append(out, CommandExport{
			Name: c.Name, Content: string(c.Body), Enabled: c.Enabled,
			Description: c.Description, ArgumentHint: c.ArgumentHint, AllowedTools: c.AllowedTools, Model: c.Model,
		})
	}
	return out
}

// SkillExportsOf is the engine's skill exports in the writers' shape; the
// bundle package keeps a file's mode as plain permission bits, so the
// os.FileMode conversion is here.
func SkillExportsOf(exports engine.Exports) []SkillExport {
	if len(exports.Skills) == 0 {
		return nil
	}
	out := make([]SkillExport, 0, len(exports.Skills))
	for _, s := range exports.Skills {
		files := make([]PackageFile, 0, len(s.Files))
		for _, f := range s.Files {
			files = append(files, PackageFile{RelPath: f.Path, Content: f.Bytes, Mode: os.FileMode(f.Mode)})
		}
		out = append(out, SkillExport{Name: s.Name, Description: s.Description, Enabled: s.Enabled, Files: files})
	}
	return out
}
