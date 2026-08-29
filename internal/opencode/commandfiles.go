//go:build parked_engines

package opencode

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// opencodeCommandDir is opencode's project-scoped custom-command directory,
// relative to the workspace root. opencode discovers a slash command from every
// `.opencode/command/<name>.md` (subdirectories yield namespaced names like
// `group/cmd`): the file's YAML front-matter supplies the description and its
// body is the command template. VERIFIED against opencode 1.18.1 — a file
// written here resolves into `opencode debug config`'s `command` map, keyed by
// its path minus the `.md` suffix. opencode also accepts the plural
// `.opencode/commands/`; the singular form is the one opencode's own config key
// (`command`) and docs list first, so ctxloom writes there.
const opencodeCommandDir = ".opencode/command"

// WriteCommandFiles writes the enabled command exports as opencode custom-command
// files under <workDir>/.opencode/command/<name>.md and records them in the
// manifest. Previously manifest-listed files are removed first, so the written
// set always mirrors the current exports. Passing no enabled exports reverts
// exactly the managed set (the live chat path's cleanup relies on this).
func WriteCommandFiles(workDir string, cmds []agent.CommandExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	dir := filepath.Join(workDir, filepath.FromSlash(opencodeCommandDir))
	return agent.WriteManagedCommandFiles(fs, dir, cmds, renderCommandFile)
}

// renderCommandFile renders one command export as an opencode custom command:
// `<name>.md` (path separators in the name become nested directories, which
// opencode reads as a namespaced command name) with YAML front-matter carrying
// the description and, when set, the model override; the body is the command
// template verbatim. opencode's command front-matter accepts only a fixed set of
// keys (description/agent/model/subtask), so the argument-hint and allowed-tools
// export fields — which opencode has no command-level slot for — are dropped.
func renderCommandFile(c agent.CommandExport) (string, []byte, error) {
	// Same defect class as agent.RenderCommandAsSkillFile: empty or
	// whitespace-only Content must not render a valid-looking frontmatter-only
	// file. WriteManagedPackageFiles warns and skips this one item on error.
	if strings.TrimSpace(c.Content) == "" {
		return "", nil, fmt.Errorf("command %q has no content to render", c.Name)
	}

	desc := c.Description
	if desc == "" {
		desc = c.Name
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("description: " + agent.EscapeYAMLString(desc) + "\n")
	if c.Model != "" {
		b.WriteString("model: " + agent.EscapeYAMLString(c.Model) + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(c.Content)
	if !strings.HasSuffix(c.Content, "\n") {
		b.WriteString("\n")
	}
	return c.Name + ".md", []byte(b.String()), nil
}
