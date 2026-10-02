//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// TestDeliveryApproach_ClaudeProjectMCPNeverHoldsTheBearerAcrossSessions:
// claude's unsafe-file MCP form, delivered by the ONE static writer over the
// production record into a project whose .mcp.json the user already wrote.
// Session A delivers, is swept the way a departed session is
// (Static.Reverse), and session B delivers into the same file. At every step
// the file holds the relay bearer by reference only, and the user's server
// survives; B's delivery is not refused by anything A's left behind (claude
// keeps its own confpatch record of the same file beside the ownership
// record).
func TestDeliveryApproach_ClaudeProjectMCPNeverHoldsTheBearerAcrossSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	project := t.TempDir()
	mcpPath := filepath.Join(project, claude.MCPFileName)
	theirs := `{"mcpServers": {"theirs": {"command": "their-server"}}}` + "\n"
	require.NoError(t, os.WriteFile(mcpPath, []byte(theirs), 0o644))
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)

	kind, err := claude.Build()
	require.NoError(t, err)
	root := kind.Root()
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"),
		compositetest.WithMCP("ctxloom", wire.MCPServer{ServedBy: wire.ServedBySessionEndpoint}))
	items := pkg.EngineItems(root.Name)
	exports, err := kind.Exports(items)
	require.NoError(t, err)
	static := fsstatic.New(fs)

	deliver := func(harp, bearer string) delivery.Writer {
		t.Helper()
		home := t.TempDir()
		cell := present.Paths{ProjectRoot: present.Root{Host: project, Engine: project}, SessionHome: present.Root{Host: home, Engine: home}}
		plan, err := delivery.Route(items, root, delivery.Preference{Root: map[present.Kind]present.RootKind{present.MCP: present.RootProjectRoot}}, cell)
		require.NoError(t, err)
		w := delivery.SessionWriter(harp)
		lo := delivery.Loadout{Plan: plan, Package: pkg, Exports: exports, MCP: sessions.Endpoint{URL: "http://127.0.0.1:1/mcp", Credential: bearer}}
		_, err = static.Deliver(context.Background(), lo, root, delivery.Target{Root: present.New(present.OnHost(cell)), Ownership: rec, Writer: w})
		require.NoError(t, err, "session %s's delivery", harp)
		body, err := os.ReadFile(mcpPath)
		require.NoError(t, err)
		require.NotContains(t, string(body), bearer, "the project file holds session %s's bearer", harp)
		require.Contains(t, string(body), "${"+claude.EnvRelayBearer+"}")
		require.Contains(t, string(body), "their-server", "the user's server survives session %s's delivery", harp)
		return w
	}

	a := deliver("session-a", "bearer-of-session-a")
	require.NoError(t, static.Reverse(context.Background(), rec, a))
	body, err := os.ReadFile(mcpPath)
	require.NoError(t, err)
	require.JSONEq(t, theirs, string(body), "the sweep leaves the user's file as they wrote it")

	b := deliver("session-b", "bearer-of-session-b")
	require.NoError(t, static.Reverse(context.Background(), rec, b))
	body, err = os.ReadFile(mcpPath)
	require.NoError(t, err)
	require.JSONEq(t, theirs, string(body))
}

// sharedMCPFile is one project whose .mcp.json the user wrote, delivered into
// by BOTH of claude's writers of that file: the at-rest hooks install
// (operations.DeliverProject, the project writer — what applyHooksToBackend
// runs) and a run's unsafe-file delivery (a session writer), over the one
// production record store. Both reach the file through claude's own
// confpatch writer too, so each step is where one writer's reversal could
// take out the other's entry.
type sharedMCPFile struct {
	t       *testing.T
	fs      afero.Fs
	project string
	mcpPath string
	rec     delivery.Ownership
	kind    engine.Engine
	pkg     composite.Package
}

func newSharedMCPFile(t *testing.T) *sharedMCPFile {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	project := t.TempDir()
	mcpPath := filepath.Join(project, claude.MCPFileName)
	require.NoError(t, os.WriteFile(mcpPath, []byte(`{"mcpServers": {"theirs": {"command": "their-server"}}}`+"\n"), 0o644))
	rec, err := operations.OwnershipRecordsOn(fs)
	require.NoError(t, err)
	kind, err := claude.Build()
	require.NoError(t, err)
	// The companion's session-endpoint declaration (dropped at rest, the
	// relay entry in a run) beside a bundle server both writers deliver.
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"),
		compositetest.WithMCP(wire.LayerServerName, wire.MCPServer{ServedBy: wire.ServedBySessionEndpoint}),
		compositetest.WithMCP("tasks", wire.MCPServer{Command: "taskloom", Args: []string{"mcp"}}))
	return &sharedMCPFile{t: t, fs: fs, project: project, mcpPath: mcpPath, rec: rec, kind: kind, pkg: pkg}
}

// atRest is the hooks install's delivery into the project.
func (s *sharedMCPFile) atRest() {
	s.t.Helper()
	_, _, err := operations.DeliverProject(context.Background(), s.fs, s.kind, s.pkg, s.project)
	require.NoError(s.t, err, "the at-rest delivery")
}

// run is a session's unsafe-file delivery; it returns the run's teardown.
func (s *sharedMCPFile) run(harp, bearer string) func() {
	s.t.Helper()
	root := s.kind.Root()
	items := s.pkg.EngineItems(root.Name)
	exports, err := s.kind.Exports(items)
	require.NoError(s.t, err)
	home := s.t.TempDir()
	cell := present.Paths{ProjectRoot: present.Root{Host: s.project, Engine: s.project}, SessionHome: present.Root{Host: home, Engine: home}}
	plan, err := delivery.Route(items, root, delivery.Preference{Root: map[present.Kind]present.RootKind{present.MCP: present.RootProjectRoot}}, cell)
	require.NoError(s.t, err)
	lo := delivery.Loadout{Plan: plan, Package: s.pkg, Exports: exports, MCP: sessions.Endpoint{URL: "http://127.0.0.1:1/mcp", Credential: bearer}}
	d, err := fsstatic.New(s.fs).Deliver(context.Background(), lo, root, delivery.Target{Root: present.New(present.OnHost(cell)), Ownership: s.rec, Writer: delivery.SessionWriter(harp)})
	require.NoError(s.t, err, "session %s's delivery", harp)
	return func() {
		s.t.Helper()
		require.NoError(s.t, d.Undo(context.Background()), "session %s's teardown", harp)
	}
}

// servers is the file's server table, by name; the bearer is never in it.
func (s *sharedMCPFile) servers(step, bearer string) map[string]any {
	s.t.Helper()
	b, err := os.ReadFile(s.mcpPath)
	require.NoError(s.t, err, step)
	require.NotContains(s.t, string(b), bearer, "%s: the file holds the bearer", step)
	var doc struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	require.NoError(s.t, json.Unmarshal(b, &doc), step)
	require.Contains(s.t, doc.MCPServers, "theirs", "%s: the user's own server was removed", step)
	return doc.MCPServers
}

// expect asserts which of ctxloom's entries the file holds after step.
func (s *sharedMCPFile) expect(step, bearer string, tasks, relay bool) {
	s.t.Helper()
	got := s.servers(step, bearer)
	_, hasTasks := got["tasks"]
	_, hasRelay := got[wire.LayerServerName]
	require.Equal(s.t, tasks, hasTasks, "%s: the bundle server both writers deliver", step)
	require.Equal(s.t, relay, hasRelay, "%s: the run's relay entry", step)
}

// TestDeliveryApproach_AtRestThenARunShareTheProjectMCPFile: the hooks
// install delivers, a run delivers over it and tears down, the install
// re-applies. Neither writer's reversal takes out the other's entry.
func TestDeliveryApproach_AtRestThenARunShareTheProjectMCPFile(t *testing.T) {
	s := newSharedMCPFile(t)
	const bearer = "bearer-of-the-run"
	s.atRest()
	s.expect("1 at rest", bearer, true, false)
	teardown := s.run("run-a", bearer)
	s.expect("2 the run delivered", bearer, true, true)
	teardown()
	s.expect("3 the run tore down", bearer, true, false)
	s.atRest()
	s.expect("4 at rest again", bearer, true, false)
	require.NoError(t, operations.RemoveProject(context.Background(), s.fs, s.kind, s.project))
	s.expect("5 uninstalled", bearer, false, false)
}
