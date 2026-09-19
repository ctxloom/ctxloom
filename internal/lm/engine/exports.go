package engine

import (
	"os"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
)

// The two export loops below are the engine-agnostic half of command and
// skill export: names, content and file bytes are plumbing every engine
// shares, and only the per-item enablement/metadata projection (pick) is the
// engine's own. Each engine's descriptor supplies its pick; the loop lives
// here because more than one engine already uses it.

// BuildCommandExports maps prompts to command exports, with pick projecting
// each prompt's per-engine export config into the engine-specific fields.
func BuildCommandExports(prompts []*bundles.LoadedContent, pick func(*bundles.LoadedContent) agent.CommandExport) []agent.CommandExport {
	names := exportNames(prompts)
	out := make([]agent.CommandExport, 0, len(prompts))
	for _, p := range prompts {
		e := pick(p)
		e.Name = names[p.Name]
		e.Content = p.Content
		out = append(out, e)
	}
	return out
}

// BuildSkillExports maps resolved skills to skill exports, with pick
// supplying the engine-specific enablement.
func BuildSkillExports(skills []*bundles.LoadedSkill, pick func(*bundles.LoadedSkill) bool) []agent.SkillExport {
	out := make([]agent.SkillExport, 0, len(skills))
	for _, s := range skills {
		files := make([]agent.PackageFile, 0, len(s.Files))
		for _, f := range s.Files {
			// bundles keeps the mode as a plain uint32 so its loader/manifest
			// layer carries no os.FileMode dependency; the conversion is here.
			files = append(files, agent.PackageFile{RelPath: f.RelPath, Content: f.Content, Mode: os.FileMode(f.Mode)})
		}
		out = append(out, agent.SkillExport{
			Name:        s.Frontmatter.Name,
			Description: s.Frontmatter.Description,
			Enabled:     pick(s),
			Files:       files,
		})
	}
	return out
}

// exportNames maps each prompt's full identity (LoadedContent.Name) to its
// export-facing command name. The short form (bundle's last path segment +
// item, see LoadedContent.ExportName) is used whenever it's unambiguous within
// the export set; when two bundles shorten to the same name, the colliders
// fall back to their full identity — sanitized for filesystem safety — so
// neither silently overwrites the other's command file.
func exportNames(prompts []*bundles.LoadedContent) map[string]string {
	counts := make(map[string]int, len(prompts))
	for _, p := range prompts {
		counts[p.ExportName()]++
	}
	names := make(map[string]string, len(prompts))
	for _, p := range prompts {
		short := p.ExportName()
		if counts[short] > 1 {
			names[p.Name] = sanitizeExportName(p.Name)
			continue
		}
		names[p.Name] = short
	}
	return names
}

// sanitizeExportName makes a collision-fallback name safe to use as a filename
// across platforms: ':' is invalid on Windows and '\' is a path separator
// there, so both collapse to '-'. The per-agent writers handle '/' themselves.
func sanitizeExportName(name string) string {
	return strings.NewReplacer(":", "-", `\`, "-").Replace(name)
}
