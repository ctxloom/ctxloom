//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/cli"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// excludedLeaves are public-surface entry points deliberately not exercised by a
// scenario, each with the reason. Printed on every run so the exclusion is never
// silent (see plan §7.5 / "no silent caps").
var excludedLeaves = map[string]string{
	// Machine callbacks (ctxloom hook inject-context|hud|session-bind|stamp-plan)
	// are all hidden, so leafCommands never surfaces them — no exclusion needed.
	"ctxloom manage config edit": "opens $EDITOR on config.yaml; TTY-only, no hermetic fixture",
	"ctxloom mcp serve":          "the MCP server itself; exercised by every @mcp scenario",
	"ctxloom llm serve":          "internal gRPC plugin server",
	"ctxloom remote discover":    "network discovery search; no deterministic fixture (excluded)",
	"ctxloom bundle mcp edit":    "edits an MCP server embedded in a bundle; requires a bundle-embedded MCP fixture (niche)",
	// Remote ops needing richer state than a single-commit fixture provides. The
	// core clone/fetch/install/sync path is covered hermetically by the @remote
	// content scenarios (browse/install/sync/lock against a seeded file:// repo).
	"ctxloom remote update":         "checks installed bundles for a newer SHA; needs a second remote commit. Core fetch covered by @remote scenarios",
	"ctxloom remote upgrade":        "upgrades installed bundles to latest; needs an update cycle. Core fetch covered by @remote scenarios",
	"ctxloom map":                   "fans a task out across parallel LLM sessions; @live-only, no hermetic fixture",
	"ctxloom weave":                 "map + an LLM synthesis pass over the results; @live-only, no hermetic fixture",
	"ctxloom session distill":       "@live + requires a real backend session transcript; the fragment/skill/bundle distill paths cover the distiller",
	"ctxloom completion zsh":        "shell variant; the completion path is covered via bash",
	"ctxloom completion fish":       "shell variant; the completion path is covered via bash",
	"ctxloom completion powershell": "shell variant; the completion path is covered via bash",
	"ctxloom help":                  "cobra-generated help command",
	"ctxloom memory list":           "reads external backend session history; not reproducible hermetically",
	"ctxloom memory show":           "reads external backend session history; not reproducible hermetically",
	"ctxloom memory compact":        "@live + external backend session history",
	"ctxloom bundle push":           "publishes to a remote forge (push/PR); requires a writable remote, out of hermetic scope",
	"ctxloom fragment push":         "publishes to a remote forge (push/PR); requires a writable remote, out of hermetic scope",
	"ctxloom skill push":            "publishes to a remote forge (push/PR); requires a writable remote, out of hermetic scope",
	"ctxloom profile push":          "publishes to a remote forge (push/PR); requires a writable remote, out of hermetic scope",
	"ctxloom container build":       "builds the agent container image; requires a container runtime + network pulls, out of hermetic scope",
	"ctxloom plan watch":            "long-lived file watcher (runs until interrupted); no hermetic exit",
}

// excludedTools / excludedResources are registered surface that no hermetic
// scenario can reach; @live scenarios cover the session tools.
var excludedTools = map[string]string{
	"compact_session":      "compacts a live backend session transcript; exercised at the operations layer in tests/integration",
	"recover_session":      "resolves the most-recent backend session; requires a real backend transcript",
	"get_previous_session": "resolves the previous backend session; requires a real backend transcript",
}

// excludedTemplates are resource templates with no hermetic scenario. Currently
// none — every templated resource is exercised, including remotes/{name}/contents
// against a seeded file:// remote.
var excludedTemplates = map[string]string{}

// TestCompleteness enforces that every public CLI leaf, MCP tool, and MCP
// resource is exercised by some scenario or step, or is explicitly excluded.
func TestCompleteness(t *testing.T) {
	corpus := loadCorpus(t)

	t.Run("cli leaves", func(t *testing.T) {
		for _, path := range leafCommands(cli.GetRootCmd()) {
			if reason, ok := excludedLeaves[path]; ok {
				t.Logf("excluded: %s — %s", path, reason)
				continue
			}
			if !strings.Contains(corpus, path) {
				t.Errorf("uncovered CLI command: %q (no scenario runs it)", path)
			}
		}
	})

	tools, resources, templates := liveSurface(t)

	t.Run("mcp tools", func(t *testing.T) {
		for _, name := range tools {
			if reason, ok := excludedTools[name]; ok {
				t.Logf("excluded tool: %s — %s", name, reason)
				continue
			}
			if !strings.Contains(corpus, name) {
				t.Errorf("uncovered MCP tool: %q", name)
			}
		}
	})

	t.Run("mcp resources", func(t *testing.T) {
		for _, uri := range resources {
			if !strings.Contains(corpus, uri) {
				t.Errorf("uncovered MCP resource: %q", uri)
			}
		}
		for _, tmpl := range templates {
			if reason, ok := excludedTemplates[tmpl]; ok {
				t.Logf("excluded template: %s — %s", tmpl, reason)
				continue
			}
			prefix := tmpl
			if i := strings.IndexByte(tmpl, '{'); i >= 0 {
				prefix = tmpl[:i]
			}
			if !strings.Contains(corpus, prefix) {
				t.Errorf("uncovered MCP resource template: %q (no URI under %q)", tmpl, prefix)
			}
		}
	})
}

// leafCommands returns the "ctxloom ..." command path of every runnable,
// non-hidden leaf in the tree.
func leafCommands(root *cobra.Command) []string {
	var out []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		children := c.Commands()
		hasVisibleChild := false
		for _, ch := range children {
			if !ch.Hidden && ch.Name() != "help" {
				hasVisibleChild = true
			}
			walk(ch)
		}
		if !hasVisibleChild && c.Runnable() && !c.Hidden && c != root {
			out = append(out, c.CommandPath())
		}
	}
	walk(root)
	sort.Strings(out)
	return out
}

// liveSurface starts an MCP server against a minimal project and returns the
// registered tools, resources, and resource templates.
func liveSurface(t *testing.T) (tools, resources, templates []string) {
	t.Helper()
	env, err := testenv.NewTestEnvironment()
	if err != nil {
		t.Fatalf("test env: %v", err)
	}
	t.Cleanup(func() { _ = env.Cleanup() })
	if err := env.Setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := env.InitGitRepo(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := env.WriteFile(".ctxloom/config.yaml", minimalConfig); err != nil {
		t.Fatalf("write config: %v", err)
	}
	client, err := env.StartMCP()
	if err != nil {
		t.Fatalf("start mcp: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if tools, err = client.ListTools(); err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if resources, err = client.ListResources(); err != nil {
		t.Fatalf("list resources: %v", err)
	}
	if templates, err = client.ListResourceTemplates(); err != nil {
		t.Fatalf("list resource templates: %v", err)
	}
	return tools, resources, templates
}

// loadCorpus concatenates every feature file and step-definition source so a
// command/tool/resource exercised through a friendly step wrapper still counts
// as covered.
func loadCorpus(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	add := func(glob string) {
		matches, _ := filepath.Glob(glob)
		for _, m := range matches {
			data, err := os.ReadFile(m)
			if err != nil {
				t.Fatalf("read %s: %v", m, err)
			}
			b.Write(data)
			b.WriteByte('\n')
		}
	}
	add("features/*.feature")
	add("steps_*.go")
	return b.String()
}
