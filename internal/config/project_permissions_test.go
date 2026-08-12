package config

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config/layerscope"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The project-default permission posture (`permissions:` at the top level of
// config.yaml) is a PER-PROJECT CONSENT: "in THIS directory, this is the
// posture an agent launches at by default". Its whole value comes from being
// scoped to one project dir — a home-wide permissive default already exists as
// the claude-code host stopgap, and a second one reachable from
// ~/.ctxloom/config.yaml would silently re-grant every project on the machine
// the posture the human granted one of them. These tests are the layer pin
// that keeps that from happening.

// TestProjectPermissions_HonoredFromProjectLayer is the positive half: a
// project config that declares the key is read back through the accessor the
// resolution chain consults.
func TestProjectPermissions_HonoredFromProjectLayer(t *testing.T) {
	cfg := writeLayers(t, "", "version: 6\npermissions: bypass\n")

	assert.Equal(t, "bypass", cfg.GetPermissions(),
		"a project config's declared permission posture must be honored")
}

// TestProjectPermissions_HomeLayerIsIgnored is the pin the feature exists for.
// The HOME config declares the permissive posture; the project declares
// nothing. Layering's normal gap-filling rule (TestLoad_ProjectInheritsHomeKeys)
// would hand the project home's value — which is exactly the escalation
// layerscope closes: this key is ScopeShared, so home may not carry it at all
// and the value is DROPPED before the merge, leaving the project at its
// undeclared default.
//
// MUTATION TARGET (m1): flipping the `permissions` rule in
// layerscope.DefaultPolicy to ScopePreference (or ScopeMachine, or deleting
// the rule entirely) makes home's grant stick and turns this red.
func TestProjectPermissions_HomeLayerIsIgnored(t *testing.T) {
	cfg := writeLayers(t,
		"version: 6\npermissions: bypass\n",
		"version: 6\n",
	)

	assert.Equal(t, "", cfg.GetPermissions(),
		"a HOME config must never grant a project's permission posture: per-project consent is the whole point, and a home-wide permissive default is a second host stopgap")

	// Dropped LOUDLY, never silently: the human who wrote it in the wrong file
	// must be told which file it belongs in.
	var sawWarning bool
	for _, w := range cfg.GetWarnings() {
		if strings.Contains(w.Text, "permissions") && strings.Contains(w.Text, "home config") {
			sawWarning = true
		}
	}
	assert.True(t, sawWarning,
		"dropping a home-layer `permissions` must warn (a setting that looks applied and is not is the worse outcome)")
}

// TestProjectPermissions_EnvCannotGrantIt closes the second reach: the
// environment is inherited by every child process ctxloom spawns, so an agent
// that can run `bash` could otherwise grant itself the posture. ScopeShared
// refuses env for exactly that reason.
func TestProjectPermissions_EnvCannotGrantIt(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte("version: 6\n"), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir),
		WithOverrides(confload.Overrides{Env: map[string]any{"PERMISSIONS": "bypass"}}))
	require.NoError(t, err)

	assert.Equal(t, "", cfg.GetPermissions(),
		"an environment variable must never grant the project permission posture — env is the one channel every spawned child inherits")
}

// TestProjectPermissions_ScopeIsShared states the policy assignment directly,
// so a future edit to DefaultPolicy that changes this key's scope fails here
// with the reason attached rather than only through the behavioural tests
// above.
func TestProjectPermissions_ScopeIsShared(t *testing.T) {
	rule, ok := layerscope.DefaultPolicy().Lookup([]string{"permissions"})
	if !assert.True(t, ok, "the project permission default must have a layerscope rule") {
		return
	}
	assert.Equal(t, layerscope.ScopeShared, rule.Scope,
		"the project permission default is a privilege grant scoped to one project dir: project file (or an explicit one-invocation --config-set), never home, never env")
	assert.True(t, rule.Scope.Allows(layerscope.LayerProject))
	assert.False(t, rule.Scope.Allows(layerscope.LayerHome))
	assert.False(t, rule.Scope.Allows(layerscope.LayerEnv))
}
