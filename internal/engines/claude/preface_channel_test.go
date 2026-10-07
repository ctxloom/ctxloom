package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// TestDelivery_PrefaceItemsRideTheEndpoint_EveryOtherItemIsAFile is the
// two-channel probe for claude, the engine that PROVIDES both halves of
// delivery. Over a package with an unpremised fragment, a premised
// (preface) fragment, a command, a hook and a bundle MCP server:
//
//   - the engine root's delegation, applied by delivery.Route, puts the
//     preface item on the ENDPOINT (Plan.Dynamic) and nothing else there;
//   - delivering every static kind through claude's typed approaches
//     writes a FILE SET in which the unpremised fragment, the command, the
//     hook and the bundle server each appear, the preface item's text
//     appears in NO file, and the MCP file names the session endpoint —
//     the URL and bearer the preface items are served through.
//
// The preface items' text is not this test's: it asserts the CHANNEL.
func TestDelivery_PrefaceItemsRideTheEndpoint_EveryOtherItemIsAFile(t *testing.T) {
	kind, err := Build()
	require.NoError(t, err)
	root := kind.Root()
	const (
		alwaysBody  = "ALWAYS-FRAGMENT-BODY"
		prefaceBody = "PREFACE-FRAGMENT-BODY"
		commandBody = "COMMAND-BODY"
		hookCommand = "ctxloom hook probe"
	)
	items := engine.Items{
		Fragments: []engine.FragmentItem{
			{Ref: "b#fragment/always", Name: "always", Body: []byte(alwaysBody)},
			{Ref: "b#fragment/when-go", Name: "when-go", Body: []byte(prefaceBody), Premise: "the task touches Go"},
		},
		Commands: []engine.CommandItem{{Ref: "b#command/go", Name: "go", Body: []byte(commandBody)}},
		Hooks:    []wire.Hook{{Type: "command", Command: hookCommand}},
		MCP:      []wire.MCPServer{{Command: "probe-mcp"}},
	}
	dir := t.TempDir()
	project, home := filepath.Join(dir, "project"), filepath.Join(dir, "home")
	require.NoError(t, os.MkdirAll(project, 0o755))
	require.NoError(t, os.MkdirAll(home, 0o755))
	roots := present.Paths{ProjectRoot: present.Root{Host: project, Engine: project}, SessionHome: present.Root{Host: home, Engine: home}}
	plan, err := delivery.Route(items, root, delivery.Preference{}, roots)
	require.NoError(t, err)
	require.Equal(t, []string{"b#fragment/when-go"}, plan.Dynamic, "the preface item, and only it, rides the endpoint")

	ep := sessions.Endpoint{URL: "http://127.0.0.1:43111/mcp", Credential: "bearer-probe"}
	fsRoot := safefs.New()
	fs := fsRoot.Fs
	start := present.New(present.OnHost(roots))
	// A claim is what a file will hold once the static writer lands it, so
	// each counts toward the file it names.
	claimed := map[string]string{}
	for _, item := range plan.Static {
		var (
			d   present.Delivered
			err error
		)
		switch item.Kind {
		case present.Context:
			// The static context is every UNPREMISED fragment; the preface
			// item was withheld by the delegation.
			d, err = root.Context.DeliverContext(start, item.Root, engine.ContextInputs{Text: []byte(alwaysBody)}, fs)
		case present.MCP:
			// The endpoint entry rides under the dynamic approach's own name: the
			// writer rebuilds an entry named for ctxloom's OWN stdio server from
			// its own definition (agent.ResolveManagedMCPServers), which is the
			// control-plane guard, not this channel.
			servers := map[string]wire.MCPServer{root.Dynamic.Name(): root.Dynamic.Endpoint(ep), "probe": items.MCP[0]}
			d, err = root.MCP.DeliverMCP(start, item.Root, engine.MCPInputs{Servers: servers}, fs)
		case present.Hooks:
			d, err = root.Hooks.DeliverHooks(start, item.Root, engine.HooksInputs{Hooks: wire.UnifiedHooks{SessionStart: items.Hooks}}, fs)
		case present.Commands:
			d, err = root.Commands.DeliverCommands(start, item.Root, engine.CommandsInputs{Commands: []engine.CommandExport{{Name: "go", Body: []byte(commandBody), Enabled: true}}}, fsRoot)
		case present.Settings:
			d, err = root.Settings.DeliverSettings(start, item.Root, engine.SettingsInputs{}, fs)
		case present.Skills:
			d, err = root.Skills.DeliverSkills(start, item.Root, engine.SkillsInputs{}, fsRoot)
		}
		require.NoError(t, err, "deliver %v", item.Kind)
		addClaimed(claimed, d)
	}

	// The file set: every regular file under both roots, with its bytes.
	files := map[string]string{}
	for _, r := range []string{project, home} {
		require.NoError(t, filepath.WalkDir(r, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files[strings.TrimPrefix(p, dir)] = string(b)
			return nil
		}))
	}
	for p, body := range claimed {
		files[strings.TrimPrefix(p, dir)] += body
	}
	contains := func(needle string) []string {
		var hits []string
		for p, body := range files {
			if strings.Contains(body, needle) {
				hits = append(hits, p)
			}
		}
		return hits
	}
	require.NotEmpty(t, contains(alwaysBody), "the unpremised fragment reaches claude through a file; files: %v", keys(files))
	require.Empty(t, contains(prefaceBody), "the preface item's text must reach claude through the endpoint, never a file")
	require.NotEmpty(t, contains(commandBody), "the command reaches claude through a file")
	require.NotEmpty(t, contains(hookCommand), "the hook reaches claude through a file")
	require.NotEmpty(t, contains("probe-mcp"), "the bundle server reaches claude through the MCP file")
	byURL, byBearer := contains(ep.URL), contains(ep.Credential)
	require.NotEmpty(t, byURL, "the MCP file names the session endpoint the preface items are served on")
	require.Equal(t, byURL, byBearer, "the endpoint's bearer rides beside its URL, in the same file")
}

// addClaimed adds each value d claims to the text of the file it names.
func addClaimed(into map[string]string, d present.Delivered) {
	for path, claims := range d.Claims {
		for _, c := range claims {
			if b, ok := c.Value.([]byte); ok {
				into[path] += string(b)
				continue
			}
			into[path] += fmt.Sprint(c.Value)
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
