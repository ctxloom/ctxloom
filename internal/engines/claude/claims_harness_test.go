package claude

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport/atrest"
)

// atRest is claude's project at dir on fs, delivered at rest through the
// claims path — what `manage hooks install` and `uninstall` run.
func atRest(t *testing.T, fs afero.Fs, dir string) *atrest.Project {
	t.Helper()
	kind, err := Build()
	require.NoError(t, err)
	return atrest.New(t, fs, kind, dir)
}

// install delivers hooks and servers into p with the statusline managed, as
// `manage hooks install` does by default.
func install(t *testing.T, p *atrest.Project, hooks *wire.HooksConfig, servers map[string]wire.MCPServer) {
	t.Helper()
	require.NoError(t, p.Install(managedPackage(hooks, servers)))
}

// managedPackage is the package install delivers.
func managedPackage(hooks *wire.HooksConfig, servers map[string]wire.MCPServer) composite.Package {
	pkg := composite.Package{MCP: servers, Statusline: true}
	if hooks != nil {
		pkg.Hooks = *hooks
	}
	return pkg
}

// statusOf is claude's status read of p.
func statusOf(t *testing.T, p *atrest.Project) agent.SettingsStatus {
	t.Helper()
	st, err := NewWriter(p.Settings()).Status(p.Dir)
	require.NoError(t, err)
	return st
}

// testDeclaration is claude's named-form table off a built engine.
func testDeclaration() agent.Declaration {
	e, err := Build()
	if err != nil {
		panic(err)
	}
	return e.(Claude).Declaration()
}

// denyPackage is install's package with deny_tools: what the settings
// surface carries for a run that denies tools.
func denyPackage(hooks *wire.HooksConfig, deny []string) composite.Package {
	pkg := managedPackage(hooks, nil)
	pkg.DenyTools = deny
	return pkg
}
