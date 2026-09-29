package isolation

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// The session home every case here presents: a session's claude leaf on the
// host, exactly as stage 1 (launch.SessionHome) places it.
const hostSessionHome = "/home/u/.ctxloom/sessions/ugly-icy-squid/home/claude"

var claudeHomeVar = &engine.HomeVar{Name: "CLAUDE_CONFIG_DIR", Subdir: "claude"}

// THE FIXED ROOT, pinned by value so a refactor cannot quietly move it: every
// container hangs its relocated engine home under this well-known
// in-container path, at the leaf the engine declares. It is a property of
// the container filesystem ctxloom owns, not of $HOME.
func TestContainerRelocator_RelocatedHomeIsUnderTheFixedRoot(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	pl, _, err := c.relocator().relocate(layout{cwd: "/proj", sessionHome: hostSessionHome, homeVar: claudeHomeVar})
	require.NoError(t, err)
	assert.Equal(t, present.Root{Host: hostSessionHome, Engine: "/ctxloom/home/claude"}, pl.Paths.Paths().SessionHome)
	assert.Equal(t, "/ctxloom/home/claude", pl.Env["CLAUDE_CONFIG_DIR"], "the home var names the Engine side")
	assert.Equal(t, []engine.HomeBinding{{Var: "CLAUDE_CONFIG_DIR", Path: "/ctxloom/home/claude"}}, pl.Home)
}

// The override is a builder on the policy (like WithImage), so an image whose
// filesystem cannot host the default root pins its own.
func TestContainerRelocator_InstanceRootIsOverridableOnThePolicy(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code").WithInstanceHome("/opt/agent-home")
	pl, _, err := c.relocator().relocate(layout{cwd: "/proj", sessionHome: hostSessionHome, homeVar: claudeHomeVar})
	require.NoError(t, err)
	assert.Equal(t, "/opt/agent-home/claude", pl.Paths.Paths().SessionHome.Engine)
}

// The host presents every root in place and mounts nothing: the engine opens
// the host path itself.
func TestHostRelocator_PresentsInPlaceAndMountsNothing(t *testing.T) {
	pl, mounts, err := hostRelocator{}.relocate(layout{cwd: "/proj", sessionHome: hostSessionHome, homeVar: claudeHomeVar})
	require.NoError(t, err)
	assert.Nil(t, mounts)
	assert.Equal(t, present.Root{Host: "/proj", Engine: "/proj"}, pl.Paths.Paths().ProjectRoot)
	assert.Equal(t, present.Root{Host: hostSessionHome, Engine: hostSessionHome}, pl.Paths.Paths().SessionHome)
	assert.Equal(t, hostSessionHome, pl.Env["CLAUDE_CONFIG_DIR"])
}

// THE PATH-WITH-MOUNT INVARIANT: every root the container relocator presents
// arrives WITH a mount binding its Host side at its Engine side. A presented
// path with no mount is a path the engine is told about and cannot open.
func TestContainerRelocator_EveryPresentedRootComesWithItsMount(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	for name, l := range map[string]layout{
		"relocating engine":     {cwd: "/proj", sessionHome: hostSessionHome, homeVar: claudeHomeVar},
		"non-relocating engine": {cwd: "/proj", sessionHome: "/home/u/.ctxloom/sessions/h/home/mock"},
		"no session home":       {cwd: "/proj"},
	} {
		t.Run(name, func(t *testing.T) {
			pl, mounts, err := c.relocator().relocate(l)
			require.NoError(t, err)
			paths := pl.Paths.Paths()
			for _, root := range []present.Root{paths.ProjectRoot, paths.SessionHome} {
				if root.Host == "" {
					continue
				}
				assert.Contains(t, mounts, mount{Host: root.Host, Container: root.Engine},
					"root %s is presented at %s with no mount making it true", root.Host, root.Engine)
			}
		})
	}
}

// The SCRATCH ruling: an engine that relocates nothing still gets its
// session home in a container, presented as the container's $HOME.
func TestContainerRelocator_NonRelocatingEngineHomeIsHOME(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "mock")
	pl, _, err := c.relocator().relocate(layout{cwd: "/proj", sessionHome: "/s/home/mock"})
	require.NoError(t, err)
	assert.Equal(t, defaultContainerHome, pl.Paths.Paths().SessionHome.Engine)
	assert.Empty(t, pl.Env, "no home var to set: the engine finds its home at $HOME")
}

// unroutableMapper refuses every path under its prefix, as a DooD mapper
// refuses a path no daemon-visible mount covers.
type unroutableMapper struct{ under string }

var errNoRoute = errors.New("no daemon-visible mount covers it")

func (m unroutableMapper) toContainer(host string) (string, error) {
	if present.Under(host, m.under) {
		return "", errNoRoute
	}
	return host, nil
}

// mapperRuntime is fakeRuntime over the mapper a test gives it.
type mapperRuntime struct {
	fakeRuntime
	m pathMapper
}

func (r mapperRuntime) mapper() pathMapper { return r.m }

func (r mapperRuntime) exposeMapped(hostPath string, readOnly bool) (mount, error) {
	return exposeThrough(r.m, hostPath, readOnly)
}

// A root the runtime cannot route is refused by name, as
// present.ErrUnreachableRoot — never presented at a guessed path.
func TestContainerRelocator_UnroutableRootIsErrUnreachableRoot(t *testing.T) {
	for name, tc := range map[string]struct {
		under string
		names string
	}{
		"project root": {under: "/proj", names: "project root"},
		"session home": {under: "/home/u", names: "session home"},
	} {
		t.Run(name, func(t *testing.T) {
			rt := mapperRuntime{fakeRuntime: fakeRuntime{name: "docker", available: true}, m: unroutableMapper{under: tc.under}}
			c := NewContainerFor(rt, "claude-code")
			_, mounts, err := c.relocator().relocate(layout{cwd: "/proj", sessionHome: hostSessionHome, homeVar: claudeHomeVar})
			require.ErrorIs(t, err, present.ErrUnreachableRoot)
			require.ErrorIs(t, err, errNoRoute, "the mapper's own reason rides along")
			assert.Contains(t, err.Error(), tc.names)
			assert.Nil(t, mounts, "no partial mount set escapes a refused relocation")
		})
	}
}

// The host relocator cannot produce ErrUnreachableRoot — not by a branch, but
// because it never maps anything.
func TestHostRelocator_NeverUnreachable(t *testing.T) {
	_, _, err := hostRelocator{}.relocate(layout{cwd: "/anywhere/at/all", sessionHome: "/any/home"})
	require.NoError(t, err)
}

// A relocated claude container gets its session home mounted and no
// credential file: it authenticates from its env.
func TestContainerEnvironment_MountsTheHomeAndNoCredential(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	cw := &containerWorkspace{dir: "/proj"}
	pl, roots, err := c.relocator().relocate(layout{cwd: cw.dir, sessionHome: hostSessionHome, homeVar: claudeHomeVar})
	require.NoError(t, err)
	_, err = c.environment(cw, pl, roots)
	require.NoError(t, err)

	spec := c.buildRunnerSpec("claude-code", "name", cw, nil)
	assert.Contains(t, spec.Mounts, mount{Host: hostSessionHome, Container: "/ctxloom/home/claude", ReadOnly: false},
		"the engine writes its session state into its home: read-write")
	for _, m := range spec.Mounts {
		assert.NotContains(t, m.Container, ".credentials.json", "no credential file is mounted into a container")
	}
}

// The shared credential stores reach the daemon: a login store renders as a
// read-write bind at $HOME/.claude and a cloud provider store as a READ-ONLY
// bind at its place under $HOME, in the very `docker run` argv the runner
// starts — beside the session home, never replacing it.
func TestContainerEnvironment_RendersTheSharedStores(t *testing.T) {
	c := NewContainerFor(Docker{rootless: true}, "claude-code")
	cw := &containerWorkspace{dir: t.TempDir()}
	home := t.TempDir()
	login, provider := filepath.Join(home, ".claude"), filepath.Join(home, ".aws")
	ssoCache := filepath.Join(provider, "sso", "cache")
	stores := []sharedStore{
		{SharedStore: engine.SharedStore{Var: "STORE_VAR", HomeRel: ".claude"}, hostDir: login},
		{SharedStore: engine.SharedStore{HomeRel: ".aws", ReadOnly: true}, hostDir: provider},
		{SharedStore: engine.SharedStore{HomeRel: ".aws/sso/cache"}, hostDir: ssoCache},
	}
	sessionHome := filepath.Join(t.TempDir(), "home", "claude")
	pl, roots, err := c.relocator().relocate(layout{cwd: cw.dir, sessionHome: sessionHome, homeVar: claudeHomeVar, stores: stores})
	require.NoError(t, err)
	_, err = c.environment(cw, pl, roots)
	require.NoError(t, err)

	argv := strings.Join(c.runtime.RunArgs(c.buildRunnerSpec("claude-code", "name", cw, nil)), " ")
	assert.Contains(t, argv, "type=bind,source="+login+",target="+defaultContainerHome+"/.claude ", "the login store, read-write")
	assert.Contains(t, argv, "type=bind,source="+provider+",target="+defaultContainerHome+"/.aws,readonly", "the provider store, read-only")
	nested := "type=bind,source=" + ssoCache + ",target=" + defaultContainerHome + "/.aws/sso/cache "
	assert.Contains(t, argv, nested, "a store nested in a read-only one is still read-write")
	assert.Less(t, strings.Index(argv, "target="+defaultContainerHome+"/.aws,readonly"), strings.Index(argv, nested),
		"the nested store is mounted after its parent, which would otherwise shadow it")
	assert.Contains(t, argv, "target=/ctxloom/home/claude", "the session home keeps its own mount")
	assert.Equal(t, "", pl.Env["STORE_VAR"], "the var points the engine at $HOME")
}
