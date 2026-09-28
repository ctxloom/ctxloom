package isolation

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestContainer_PrepareDegrades: an unavailable runtime OR a missing image makes
// PrepareWorkspace return an error so the caller falls back to None — never blocks.
func TestContainer_PrepareDegrades(t *testing.T) {
	ctx := context.Background()

	// Runtime cannot launch → error mentioning the runtime.
	_, err := NewContainerFor(fakeRuntime{name: "docker", available: false}, "mock").WithImage("img").
		prepareWorkspace(ctx, "/proj", "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot launch")

	// Runtime available but image absent (binary "" → imagePresent false) → error.
	_, err = NewContainerFor(fakeRuntime{name: "docker", binary: "", available: true}, "mock").WithImage("img").
		prepareWorkspace(ctx, "/proj", "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not present")
}

// TestContainerWorkspace_DirAndCleanup: Dir() is the identical-path project dir;
// Cleanup removes the host socket scratch and is idempotent.
func TestContainerWorkspace_DirAndCleanup(t *testing.T) {
	scratch, err := os.MkdirTemp("", "ctxloom-iso-test-")
	require.NoError(t, err)
	// Safety net: this test's whole point is exercising ws.Cleanup() itself, so
	// a mutant that breaks its removal logic (or an earlier failure/panic)
	// must not leak this fixture dir under the OS temp dir.
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	ws := &containerWorkspace{dir: "/proj", scratchRoot: scratch, agentID: "m"}

	assert.Equal(t, "/proj", ws.Dir(), "workspace dir is the identical-path project directory")
	require.NoError(t, ws.Cleanup())
	_, statErr := os.Stat(scratch)
	assert.True(t, os.IsNotExist(statErr), "cleanup removes the scratch tree")
	assert.NoError(t, ws.Cleanup(), "cleanup is idempotent")
}

// TestContainer_GitdirMirrorMount is the unit test for the case where the LIVE
// project is itself a linked worktree (or submodule) whose .git is a POINTER
// FILE whose common
// dir lives OUTSIDE the identical-path project mount, so the plain container must
// mirror that common dir (same fix the worktree base uses) — but a normal .git
// DIRECTORY (main-repo checkout) is already covered by the project mount and needs
// no extra mount, and a non-repo project needs none either.
func TestContainer_GitdirMirrorMount(t *testing.T) {
	ctx := context.Background()
	common := filepath.Join(t.TempDir(), "repo", ".git")

	rt := fakeRuntime{name: "docker", available: true}
	g := &git.Fake{CommonDirValue: common}

	// .git is a POINTER FILE → mirror the common dir identical-path.
	fileProj := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fileProj, ".git"),
		[]byte("gitdir: "+filepath.Join(common, "worktrees", "x")+"\n"), 0o644))
	m, ok, err := gitdirMirrorMount(ctx, rt, g, fileProj)
	require.NoError(t, err)
	require.True(t, ok, "a .git POINTER FILE (linked worktree/submodule) needs the common-dir mirror")
	assert.Equal(t, mount{Host: common, Container: mapped(t, rt, common)}, m,
		"the common dir is mirrored through the runtime's mapper so gitdir resolves in-container")

	// .git is a DIRECTORY → already inside the identical-path project mount.
	dirProj := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dirProj, ".git"), 0o755))
	_, ok, err = gitdirMirrorMount(ctx, rt, g, dirProj)
	require.NoError(t, err)
	assert.False(t, ok, "a normal .git directory is covered by the project mount; no mirror")

	// No repo at all → nothing to mirror.
	bareProj := t.TempDir()
	_, ok, err = gitdirMirrorMount(ctx, rt, g, bareProj)
	require.NoError(t, err)
	assert.False(t, ok, "a non-repo project needs no gitdir mirror")
}

// TestContainerName_SanitizesAndScopes: the name is a valid, unique,
// teardown-targetable container name derived from the agent id.
func TestContainerName_SanitizesAndScopes(t *testing.T) {
	n := containerName("code review/aspect:sec")
	assert.True(t, strings.HasPrefix(n, "ctxloom-iso-"), "scoped name prefix")
	assert.NotContains(t, n, "/", "path separators stripped")
	assert.NotContains(t, n, ":", "colons stripped")
	assert.NotEqual(t, containerName("m"), containerName("m"), "names are unique per spawn")

	// An empty/garbage agent id still yields a valid name.
	assert.True(t, strings.HasPrefix(containerName("///"), "ctxloom-iso-agent-"))
}

// TestResolveContainer_DegradesWithoutRuntime documents the two-place degrade,
// driven HERMETICALLY through the selectRuntimeProbe seam (never a real
// docker/podman daemon): with a launchable runtime Resolve returns the container
// policy; with no runtime it degrades to None AND records the fatal
// ClassIsolation finding an explicitly-requested-but-unsatisfiable container
// raises.
func TestResolveContainer_DegradesWithoutRuntime(t *testing.T) {
	t.Run("a launchable runtime resolves to the container policy", func(t *testing.T) {
		resetStrictness(t)
		stubRuntimeProbe(t, fakeRuntime{name: "docker", available: true})

		p := chainFor(Axes{Runtime: RuntimeContainerRootless}, "claude-code", ImageConfig{})[0]
		assert.Equal(t, "container", p.Name(), "a launchable runtime resolves to the container policy")
		assert.Empty(t, strictness.All(), "a satisfied container request records no finding")
	})

	t.Run("no runtime degrades to none and records one fatal isolation finding", func(t *testing.T) {
		resetStrictness(t)
		stubRuntimeProbe(t, Host{})

		p := chainFor(Axes{Runtime: RuntimeContainerRootless}, "claude-code", ImageConfig{})[0]
		assert.Equal(t, "none", p.Name(), "no runtime degrades to none")

		findings := strictness.All()
		require.Len(t, findings, 1, "a requested container with no reachable runtime is one fatal finding")
		assert.Equal(t, report.KindIsolation, findings[0].Kind)
	})
}

// TestContainerName_AgreesWithSanitizeAgentID pins the SHARED sanitization
// behaviour the container-name builder and the path/email segment builder must
// keep in common: both render an agent id through containerNameSafe, trim the
// separator characters, and fall back to "agent" when nothing survives. The two
// bodies were byte-identical, so no parity test could ever be red against them —
// this instead fixes the behaviour ACROSS the seam, so collapsing one onto the
// other is provably behaviour-preserving and a later divergence fails here.
func TestContainerName_AgreesWithSanitizeAgentID(t *testing.T) {
	for _, id := range []string{
		"m",
		"code review/aspect:sec",
		"///",
		"",
		"-._weird-._",
		"UPPER_lower.9",
		"a b\tc\nd",
	} {
		assert.True(t,
			strings.HasPrefix(containerName(id), "ctxloom-iso-"+sanitizeAgentID(id)+"-"),
			"containerName(%q) must embed exactly sanitizeAgentID(%q)=%q", id, id, sanitizeAgentID(id))
	}
}

// TestContainer_NilBaseIsUnreachable pins a claim. A review row claimed Name()
// nil-guards a base that PrepareWorkspace "would panic on" — the guard in the
// harmless method, absent from the dangerous one. MEASURED here, both halves of
// that are wrong:
//
//  1. every production construction path sets a non-nil base, so nothing can
//     reach PrepareWorkspace with one missing;
//  2. the only value that HAS a nil base — a bare test-built Container{} — never
//     reaches c.base.prepareBase at all. prepareContainerScratch runs first and
//     returns on the nil runtime, and even past that the zero spec's
//     undeclared container story refuses before the base is touched. Name()'s guard exists
//     because Name() IS called on such bare values; PrepareWorkspace is not.
//
// Adding a nil-base guard to PrepareWorkspace would be dead defensive code. This
// pins the property that makes it dead, so it fails if a constructor ever stops
// setting a base or the gate order changes to reach the base first.
func TestContainer_NilBaseIsUnreachable(t *testing.T) {
	rt := fakeRuntime{name: "docker", available: true}
	for name, c := range map[string]Container{
		"NewContainerFor":               NewContainerFor(rt, "claude-code"),
		"NewContainerFor/WithImage":     NewContainerFor(rt, "mock").WithImage("img"),
		"containerFor":                  containerFor(rt, "claude-code", ImageConfig{}),
		"NewContainerWorktreeFor":       NewContainerWorktreeFor(rt, "claude-code", ImageConfig{}, nil),
		"NewContainerWorktreeFor/image": NewContainerWorktreeFor(rt, "mock", ImageConfig{Image: "img"}, nil),
		"WithSessionState":              NewContainerFor(rt, "").WithSessionState(SessionState{Harp: "h"}),
		"WithImage":                     NewContainerFor(rt, "").WithImage("other"),
		"WithSessionState/worktree":     NewContainerWorktreeFor(rt, "", ImageConfig{}, nil).WithSessionState(SessionState{Harp: "h"}),
	} {
		assert.NotNil(t, c.base, "%s must yield a container with a workspace base", name)
	}

	// The one nil-base value there is never reaches the base: the gate returns
	// first, and no panic escapes.
	require.NotPanics(t, func() {
		_, err := Container{}.prepareWorkspace(context.Background(), t.TempDir(), "m")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot launch",
			"a bare Container{} stops at the runtime gate, long before the base")
	})
}

// TestContainer_RunnerSpecRendersTheRelocatedRoots: the runner spec's WorkDir
// and root mounts are the container relocator's outcome, not a second
// derivation from the host path. Under fakeRuntime's non-identity
// prefixMapper a spec that re-derived the project mount from cw.dir (or
// skipped the mapper) would carry the raw host path, which this catches.
func TestContainer_RunnerSpecRendersTheRelocatedRoots(t *testing.T) {
	rt := fakeRuntime{name: "docker", available: true}
	c := NewContainerFor(rt, "")
	live := filepath.Join(t.TempDir(), "proj", "live")
	sessionHome := filepath.Join(t.TempDir(), "sessions", "h", "home", "mock")
	cw := &containerWorkspace{dir: live, agentID: "m"}
	pl, roots, err := c.relocator().relocate(layout{cwd: cw.dir, sessionHome: sessionHome})
	require.NoError(t, err)
	_, err = c.environment(cw, pl, roots)
	require.NoError(t, err)

	spec := c.buildRunnerSpec("mock", "name", cw, nil)

	assert.Equal(t, mapped(t, rt, live), spec.WorkDir,
		"WorkDir must be the MAPPED container path, not the raw host dir")
	assert.Equal(t, pl.Paths.Paths().ProjectRoot.Engine, spec.WorkDir, "WorkDir is the placement's project root, from the same producer")
	assert.NotEqual(t, live, spec.WorkDir, "guard on the guard: the mapper under test is non-identity")
	assert.Contains(t, spec.Mounts, mount{Host: live, Container: mapped(t, rt, live)},
		"the project mount's Container side must be the MAPPED path, not the raw host dir")
	assert.Contains(t, spec.Mounts, mount{Host: sessionHome, Container: defaultContainerHome},
		"a non-relocating engine's session home is mounted as the container's $HOME")
	assert.Equal(t, defaultContainerHome, spec.Home)
}

// TestContainer_WithImageRunsAsIs pins a regression. A caller-supplied
// image is USER-OWNED: nothing ctxloom authored — the identity-remap entrypoint
// included — is guaranteed to be in it, so it must be run AS-IS (never locally
// rebuilt) and it must face checkRunAsIsIdentity's pre-start contract check, the
// one signal there is before a wrong-identity container root-owns every file it
// writes into the mounted project.
//
// containerFor already did that for an isolation_images override by clearing the
// spec's build recipe. WithImage — the override used for a per-agent
// container_image — swapped the image and left the recipe in place, so
// runAsIs() stayed false, the identity check never ran, and ensureImage would
// try to BUILD the user's tag locally when absent. The two override paths
// must agree.
func TestContainer_WithImageRunsAsIs(t *testing.T) {
	rt := fakeRuntime{name: "docker", available: true}

	assert.True(t, NewContainerFor(rt, "claude-code").WithImage("user/agent:1").runAsIs(),
		"a caller-supplied image is user-owned: run as-is, so the identity contract is actually checked")
	assert.True(t, containerFor(rt, "claude-code", ImageConfig{Image: "user/agent:1"}).runAsIs(),
		"the isolation_images override path already agreed")
	assert.False(t, NewContainerFor(rt, "claude-code").runAsIs(),
		"without an override the spec's own recipe still builds the agent image")
}

// TestGitCommonDirMount_WholeCommonDirReadWrite pins the ACCEPTED posture a
// review row re-opened. The row's facts are correct: the entire git common dir is
// bind-mounted READ-WRITE, mapped through the runtime's pathMapper, so a
// low-trust container-worktree member can reach the main checkout's
// refs/objects/index and every other worktree's admin dir. That exposure is
// real and was adjudicated in the tree before this wave (see
// gitCommonDirMount's own DECISION block): the per-worktree admin dir a
// linked checkout needs is a SUBDIRECTORY of the common dir, git needs write
// access to refs/logs and the packed-refs/objects layout, and a surgical
// partial mount is fragile in ways that are easy to get subtly wrong.
// Narrowing it is a per-agent-git-isolation design decision, not a sweep's
// call — escalated, not changed here.
//
// What this pins is the posture itself, in both directions: read-only would
// break every linked-worktree container run, and an UNMAPPED path (skipping
// the runtime's pathMapper rather than routing through it) would break the
// `gitdir:` pointer that made the mount necessary — the mount and the
// project's own WorkDir must always agree on the SAME translation, identity
// or not. A change to either must be deliberate.
func TestGitCommonDirMount_WholeCommonDirReadWrite(t *testing.T) {
	repo := t.TempDir()
	common := filepath.Join(repo, ".git")
	rt := fakeRuntime{name: "docker", available: true}
	m, err := gitCommonDirMount(context.Background(), rt,
		&git.Fake{CommonDirValue: common}, filepath.Join(repo, "wt"))
	require.NoError(t, err)

	assert.Equal(t, common, m.Host, "the WHOLE common dir is the mount source (accepted blast radius)")
	assert.Equal(t, mapped(t, rt, common), m.Container, "mapped through the runtime's pathMapper, so a `gitdir:` pointer file resolves in-container")
	assert.False(t, m.ReadOnly,
		"read-write by design: a linked checkout writes its own admin files under <common>/worktrees/<name>")
}

// TestContainerFor_PropagatesEveryImageConfigField pins the field-by-field
// copy containerFor performs, and — via the reflective guard at the end —
// makes that copy IMPOSSIBLE to under-do silently: a field added to
// ImageConfig fails this test until it is both propagated and asserted here.
//
// Without that guard the omission is invisible in the worst way this project
// knows: the user sets a config key, the run succeeds, and the setting was
// never carried into the policy that was supposed to honour it.
//
// Image takes a different route from the other five and is checked separately:
// an override runs AS-IS (the user owns that image), so it also clears the
// local-build recipe rather than being layered onto a base.
func TestContainerFor_PropagatesEveryImageConfigField(t *testing.T) {
	rt := fakeRuntime{name: "docker", available: true}

	img := ImageConfig{
		BaseContainerfile:   "/base/Containerfile",
		AppRoot:             "/some/project",
		NoDevcontainerBase:  true,
		DevcontainerService: "devservice",
		Engines:             []string{"claude-code", "mock"},
	}
	c := containerFor(rt, "claude-code", img)
	assert.Equal(t, img.BaseContainerfile, c.baseContainerfile)
	assert.Equal(t, img.AppRoot, c.appRoot)
	assert.Equal(t, img.NoDevcontainerBase, c.noDevcontainerBase)
	assert.Equal(t, img.DevcontainerService, c.devcontainerService)
	assert.Equal(t, "claude-code", c.engine, "the container carries ITS engine — one image per engine, so there is no set to select")

	over := containerFor(rt, "claude-code", ImageConfig{Image: "user/agent:1"})
	assert.Equal(t, "user/agent:1", over.image)
	assert.Nil(t, over.engineSpec.engineInstall,
		"an image the user owns is run as-is, never layered onto by a local build")

	assert.Equal(t,
		[]string{"Image", "BaseContainerfile", "AppRoot", "NoDevcontainerBase", "DevcontainerService", "Engines"},
		imageConfigFieldNames(),
		"ImageConfig grew or lost a field: propagate it in containerFor and assert it above")
}

func imageConfigFieldNames() []string {
	tp := reflect.TypeOf(ImageConfig{})
	names := make([]string, 0, tp.NumField())
	for i := range tp.NumField() {
		names = append(names, tp.Field(i).Name)
	}
	return names
}

// TestRunAsIsIdentityProblem pins the per-runtime identity contract for
// user-owned run-as-is images. PUID-passing modes (rootful docker, podman
// both modes) need the ctxloom entrypoint started as root — nothing else
// makes the PUID env change who the engine runs as. Rootless docker passes
// no PUID and container-root is the ONE uid that maps to the launching user,
// so there the image must simply run as root.
func TestRunAsIsIdentityProblem(t *testing.T) {
	governed := []string{"/usr/local/bin/ctxloom-entrypoint"}
	tests := []struct {
		name   string
		rt     Runtime
		id     imageIdentity
		wantOK bool
	}{
		{"rootful docker + governed", Docker{}, imageIdentity{Entrypoint: governed}, true},
		{"rootful docker + foreign entrypoint", Docker{}, imageIdentity{Entrypoint: []string{"/docker-entrypoint.sh"}}, false},
		{"rootful docker + no entrypoint", Docker{}, imageIdentity{}, false},
		{"rootful docker + governed but USER blocks the remap", Docker{}, imageIdentity{Entrypoint: governed, User: "node"}, false},
		{"rootless docker + default root", Docker{rootless: true}, imageIdentity{}, true},
		{"rootless docker + explicit root", Docker{rootless: true}, imageIdentity{User: "root"}, true},
		{"rootless docker + USER maps to a subuid", Docker{rootless: true}, imageIdentity{User: "1000:1000"}, false},
		{"rootful podman + governed", Podman{}, imageIdentity{Entrypoint: governed}, true},
		{"rootless podman + ungoverned", Podman{rootless: true}, imageIdentity{}, false},
		{"unknown runtime held to the PUID contract", fakeRuntime{}, imageIdentity{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problem := runAsIsIdentityProblem(tt.rt, tt.id)
			if tt.wantOK {
				assert.Empty(t, problem)
			} else {
				assert.NotEmpty(t, problem)
			}
		})
	}
}

// TestCheckRunAsIsIdentity_LocallyBuiltSkips: a backend with a local build
// recipe (no image override) bakes the entrypoint itself — the contract holds
// by construction and no inspect runs (the fake binary here would fail one).
func TestCheckRunAsIsIdentity_LocallyBuiltSkips(t *testing.T) {
	resetStrictness(t)
	c := NewContainerFor(fakeRuntime{name: "docker", binary: "false", available: true}, "claude-code")
	c.checkRunAsIsIdentity(context.Background())
	assert.Empty(t, strictness.All(), "locally-built images are governed by construction")
}

// TestIdentityFor_ComposableCarriesSlot: the on-the-fly build (ensureImage)
// gets the same slot labels as the explicit one — identityFor hands
// buildFromSource composedIdentity's slot and companion key, under the
// container's own tag.
func TestIdentityFor_ComposableCarriesSlot(t *testing.T) {
	c := containerFor(fakeRuntime{name: "docker", binary: "true", available: true}, "claude-code", ImageConfig{})
	id := c.identityFor(nil)
	assert.Equal(t, c.image, id.ref)
	assert.NotEmpty(t, id.slot)
	assert.True(t, strings.HasSuffix(c.image, id.slot), "the slot is the content key the tag ends in: %s vs %s", c.image, id.slot)
	assert.Equal(t, hostImageKeys().companions, id.companions)
}
