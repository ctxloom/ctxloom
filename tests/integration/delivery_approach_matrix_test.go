//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

var matrixKinds = []agent.SurfaceKind{
	agent.SurfaceContext,
	agent.SurfaceMCP,
	agent.SurfaceSettings,
	agent.SurfaceCommands,
	agent.SurfaceSkills,
}

func matrixSentinelInputs(tag string) agent.SurfaceInputs {
	return agent.SurfaceInputs{
		Context:   "CTXSENTINEL-context-" + tag,
		Fragments: []*agent.Fragment{{Name: "frag", Content: "CTXSENTINEL-fragment-" + tag}},
		MCP: &wire.MCPConfig{Servers: map[string]wire.MCPServer{
			"CTXSENTINEL-mcp-" + tag: {Command: "CTXSENTINEL-mcpcmd-" + tag},
		}},
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "CTXSENTINEL-hook-" + tag, Type: "command"}},
		}},
		Commands: []agent.CommandExport{
			{Name: "ctxsentinelcmd", Description: "d", Content: "CTXSENTINEL-command-" + tag, Enabled: true},
		},
		Skills: []agent.SkillExport{
			{Name: "ctxsentinelskill", Description: "d", Enabled: true,
				Files: []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("CTXSENTINEL-skill-" + tag)}}},
		},
	}
}

func matrixTree(t *testing.T, fs afero.Fs, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = afero.Walk(fs, root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		b, rerr := afero.ReadFile(fs, path)
		if rerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	return out
}

// TestMatrixDump is exploratory scaffolding: it prints the derived matrix and
// the tree each declared pair writes.
func TestMatrixDump(t *testing.T) {
	names := backends.List()
	sort.Strings(names)
	for _, name := range names {
		probe := backends.BuildSurfaces(name, matrixSentinelInputs("probe"), afero.NewMemMapFs())
		any := false
		for _, k := range matrixKinds {
			if len(probe.SupportedApproaches(k)) > 0 {
				any = true
			}
		}
		if !any {
			t.Logf("BACKEND %s: no native surfaces", name)
			continue
		}
		for _, k := range matrixKinds {
			for _, a := range probe.SupportedApproaches(k) {
				fs := afero.NewMemMapFs()
				root := "/cell"
				require.NoError(t, fs.MkdirAll(root, 0o755))
				set := backends.BuildSurfaces(name, matrixSentinelInputs("probe"), fs)
				d, err := set.SurfaceFor(k, a)
				if err != nil {
					t.Logf("PAIR %s/%s/%s -> SurfaceFor error: %v", name, k, a, err)
					continue
				}
				if d == nil {
					t.Logf("PAIR %s/%s/%s -> nil delivery", name, k, a)
					continue
				}
				_, derr := d.Deliver(root)
				tree := matrixTree(t, fs, root)
				paths := make([]string, 0, len(tree))
				for p, c := range tree {
					mark := ""
					for _, kind := range []string{"context", "fragment", "mcp", "mcpcmd", "hook", "command", "skill"} {
						if strings.Contains(c, "CTXSENTINEL-"+kind+"-") {
							mark += "[" + kind + "]"
						}
					}
					paths = append(paths, p+mark)
				}
				sort.Strings(paths)
				t.Logf("PAIR %s/%s/%s err=%v files=%v", name, k, a, derr, paths)
			}
		}
	}
}
