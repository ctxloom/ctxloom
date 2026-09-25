package isolation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRuntime is a Runtime stub for the degrade-path tests: it reports a
// configurable availability and binary without touching a real daemon.
type fakeRuntime struct {
	name      string
	binary    string
	available bool
}

func (f fakeRuntime) Name() string             { return f.name }
func (f fakeRuntime) Binary() string           { return f.binary }
func (f fakeRuntime) Available() bool          { return f.available }
func (fakeRuntime) RunArgs(RunSpec) []string   { return nil }
func (fakeRuntime) RemoveArgs(string) []string { return nil }

// reachRoute is empty: a fake runner's env passes through un-re-minted.
func (fakeRuntime) reachRoute(context.Context) (hostRoute, error) { return hostRoute{}, nil }
func (fakeRuntime) gatewayInspectArgs() []string                  { return ociRuntime{}.gatewayInspectArgs() }

// The CLI grammar is the shared OCI default, so a call site routed through the
// seam renders the same argv against the fake as against a real runtime.
func (fakeRuntime) inspectRunningArgs(name string) []string {
	return ociRuntime{}.inspectRunningArgs(name)
}
func (fakeRuntime) imageInspectArgs(format string, images ...string) []string {
	return ociRuntime{}.imageInspectArgs(format, images...)
}
func (fakeRuntime) imageListArgs(filter string) []string { return ociRuntime{}.imageListArgs(filter) }
func (fakeRuntime) containerListAllArgs() []string       { return ociRuntime{}.containerListAllArgs() }
func (fakeRuntime) containerImageArgs(ids ...string) []string {
	return ociRuntime{}.containerImageArgs(ids...)
}
func (fakeRuntime) imageRemoveArgs(refs ...string) []string {
	return ociRuntime{}.imageRemoveArgs(refs...)
}
func (fakeRuntime) canonicalRef(ref string) string { return ociRuntime{}.canonicalRef(ref) }

// imageUniqueSizes answers through the docker grammar, so a scripted
// probeExec (image_prune_test.go) serves it like any other call.
func (f fakeRuntime) imageUniqueSizes(ctx context.Context) (map[string]int64, error) {
	return dockerUniqueSizes(ctx, f.binary)
}
func (fakeRuntime) buildArgs(image, file, contextDir string, flags buildFlags) []string {
	return ociRuntime{}.buildArgs(image, file, contextDir, flags)
}
func (fakeRuntime) daemonNameTemplate() string { return ociRuntime{}.daemonNameTemplate() }
func (fakeRuntime) removeOutcome(stdout []byte, err error) removeOutcome {
	return ociRuntime{}.removeOutcome(stdout, err)
}
func (fakeRuntime) passesPUID() bool { return ociRuntime{}.passesPUID() }

// Expose is the OCI identity bind mount, so tests that route delivery mounts
// through the runtime (sessionStateMounts, gitCommonDirMount) see the same Mount
// the literal produced.
func (fakeRuntime) Expose(host, target string, readOnly bool) Mount {
	return Mount{Host: host, Container: target, ReadOnly: readOnly}
}

// ExposeMapped mirrors ociRuntime's real behavior: it routes hostPath through
// f.mapper() rather than hardcoding Host==Container, so a call site that
// skips ExposeMapped/mapper() entirely produces output distinguishable from
// one that used it (see prefixMapper's doc, pathmapper_test.go).
func (f fakeRuntime) ExposeMapped(hostPath string, readOnly bool) Mount {
	return Mount{Host: hostPath, Container: f.mapper().toContainer(hostPath), ReadOnly: readOnly}
}

// mapper is a non-identity prefixMapper — deliberately NOT identityMapper.
// Under identity, ExposeMapped(p) == Mount{p, p} whether or not a call site
// actually threads its path through the mapper, so a deleted mapper() call
// is byte-identical to a correct one and every container test that exercises
// this fake was structurally unable to prove the mapper seam is reachable.
// prefixMapper breaks that: it is injective (distinct host paths stay
// distinct after mapping), so a test comparing against the ACTUAL mapped
// value catches a call site that silently reverts to the raw host path.
func (fakeRuntime) mapper() pathMapper { return prefixMapper{prefix: "/ctr"} }

// Enumerate is a no-op default for the many tests that never exercise the
// container-reap sweep; container_reap_test.go defines its own fake that
// embeds fakeRuntime and overrides this.
func (fakeRuntime) Enumerate(context.Context, string) ([]ContainerInfo, error) { return nil, nil }

// TestContainer_PrepareDegrades: an unavailable runtime OR a missing image makes
// PrepareWorkspace return an error so the caller falls back to None — never blocks.
func TestContainer_PrepareDegrades(t *testing.T) {
	ctx := context.Background()

	// Runtime cannot launch → error mentioning the runtime.
	_, err := NewContainerFor(fakeRuntime{name: "docker", available: false}, "mock").WithImage("img").
		PrepareWorkspace(ctx, "/proj", "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot launch")

	// Runtime available but image absent (binary "" → imagePresent false) → error.
	_, err = NewContainerFor(fakeRuntime{name: "docker", binary: "", available: true}, "mock").WithImage("img").
		PrepareWorkspace(ctx, "/proj", "m")
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

// brokenScratch builds a scratch tree RemoveAll cannot fully remove (a file
// pinned inside a write-protected subdir) — the hermetic stand-in for the
// root-owned residue a wrong-identity container leaves behind. Perms are
// restored on cleanup so t.TempDir's own removal succeeds.
func brokenScratch(t *testing.T) string {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("root ignores directory write protection; cannot simulate immovable residue")
	}
	root := t.TempDir()
	sub := filepath.Join(root, "cfg0")
	require.NoError(t, os.Mkdir(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "stuck"), []byte("x"), 0o644))
	require.NoError(t, os.Chmod(sub, 0o555))
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	return root
}

// TestContainerWorkspace_CleanupSurfacesResidue: a scratch tree the launching
// user cannot remove is the CONSEQUENCE DETECTOR for every identity hole (a
// wrong-identity container root-owned it) — the failure must stream loudly,
// naming the residue path, the likely cause, and a manual fix, never be
// silently swallowed (the callers discard Cleanup's error by contract).
func TestContainerWorkspace_CleanupSurfacesResidue(t *testing.T) {
	root := brokenScratch(t)
	ws := &containerWorkspace{dir: "/proj", scratchRoot: root, agentID: "m"}

	done := captureStderr(t)
	err := ws.Cleanup()
	stderr := done()

	require.Error(t, err, "the error still returns for callers that check")
	assert.Contains(t, err.Error(), "remove container scratch")
	assert.Contains(t, stderr, root, "the warning names the residue path")
	assert.Contains(t, stderr, "wrong-identity", "…and the likely cause")
	assert.Contains(t, stderr, "sudo rm", "…and the manual fix")
}

// TestContainerWorkspace_WorktreeBaseCleanupSurfacesResidue: the worktree-base
// workspace (baseCleanup = the worktree teardown) surfaces the same scratch
// residue the host base does — post-collapse both bases share one containerWorkspace
// whose Cleanup always warns AND returns the scratch error (SD3), the base teardown
// (WIP-safe) contributing no error of its own.
func TestContainerWorkspace_WorktreeBaseCleanupSurfacesResidue(t *testing.T) {
	root := brokenScratch(t)
	ws := &containerWorkspace{scratchRoot: root, agentID: "m", baseCleanup: (&worktreeWorkspace{}).Cleanup}

	done := captureStderr(t)
	err := ws.Cleanup()
	stderr := done()

	require.Error(t, err, "the scratch-removal error returns for callers that check")
	assert.Contains(t, err.Error(), "remove container scratch")
	assert.Contains(t, stderr, root, "the warning names the residue path")
	assert.Contains(t, stderr, "sudo rm", "…and the manual fix")
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
	const common = "/repo/.git"

	rt := fakeRuntime{name: "docker", available: true}
	g := &git.Fake{CommonDirValue: common}

	// .git is a POINTER FILE → mirror the common dir identical-path.
	fileProj := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fileProj, ".git"),
		[]byte("gitdir: /repo/.git/worktrees/x\n"), 0o644))
	m, ok, err := gitdirMirrorMount(ctx, rt, g, fileProj)
	require.NoError(t, err)
	require.True(t, ok, "a .git POINTER FILE (linked worktree/submodule) needs the common-dir mirror")
	assert.Equal(t, Mount{Host: common, Container: "/ctr" + common}, m,
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
		assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
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
//     returns on the nil runtime, and even past that the zero spec's nil
//     resolveAuth would fire before the base is touched. Name()'s guard exists
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
		_, err := Container{}.PrepareWorkspace(context.Background(), t.TempDir(), "m")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot launch",
			"a bare Container{} stops at the runtime gate, long before the base")
	})
}

// TestContainer_ExecSpecRefusesEmptyCommand pins a regression. ExecSpec
// used to accept a nil/empty command and hand back a perfectly valid-looking
// RunSpec whose Command was nil — renderRunSpec then emits nothing after the
// image, so the container silently runs the IMAGE's default entrypoint instead
// of what the caller asked for. That is this project's signature failure: a
// success return with zero payload delivered, and the caller would go on to
// speak its protocol at whatever the image's entrypoint happens to be. The
// refusal must assert on the PAYLOAD (no spec, an error naming the empty
// command), never on an exit code.
func TestContainer_ExecSpecRefusesEmptyCommand(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "")
	ws := &containerWorkspace{dir: t.TempDir(), agentID: "m"}

	for name, command := range map[string][]string{
		"nil":   nil,
		"empty": {},
	} {
		spec, err := c.ExecSpec(ws, command, nil, nil)
		require.Error(t, err, "%s command must be refused, never silently run the image entrypoint", name)
		assert.Contains(t, err.Error(), "empty command")
		assert.Nil(t, spec.Command, "no spec is handed back on refusal")
		assert.Empty(t, spec.Image, "no spec is handed back on refusal")
	}

	// A real command still renders unchanged.
	spec, err := c.ExecSpec(ws, []string{"claude-code-acp"}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-code-acp"}, spec.Command)
}

// TestContainer_ExecSpec_RoutesProjectMountAndWorkDirThroughMapper is the
// CONTROL nothing else in this package provided: ExecSpec builds its project
// mount via ExposeMapped and its WorkDir via mapper().toContainer directly
// (container.go), and neither was ever asserted against a MAPPED value — only
// TestContainer_ExecSpecRefusesEmptyCommand touches ExecSpec, and it checks
// spec.Command only. Under identityMapper this gap is invisible (Host ==
// Container either way); under fakeRuntime's non-identity prefixMapper it is
// not — a call site that quietly reverted to the raw host path (skipping
// ExposeMapped/mapper() entirely) would leave spec.WorkDir == cw.dir and the
// project mount's Container == cw.dir, which this test would catch.
func TestContainer_ExecSpec_RoutesProjectMountAndWorkDirThroughMapper(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "")
	ws := &containerWorkspace{dir: "/proj/live", agentID: "m"}

	spec, err := c.ExecSpec(ws, []string{"true"}, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, "/ctr/proj/live", spec.WorkDir,
		"WorkDir must be the MAPPED container path, not the raw host dir")
	assert.Contains(t, spec.Mounts, Mount{Host: "/proj/live", Container: "/ctr/proj/live"},
		"the project mount's Container side must be the MAPPED path, not the raw host dir")
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

// TestContainer_GitdirMirrorMountUnreadableGit pins a regression. The
// guard was `if err != nil || info.IsDir()` — one branch for two opposite facts.
// "no .git" and "a .git directory" genuinely need no mirror, but an UNREADABLE
// .git means we could not tell which case we are in, and answering "no mirror
// needed" hands the container a checkout whose git cannot resolve the repo. The
// container axis's whole degrade contract is fatal-unless-degraded on a lost
// boundary, so this must error out of PrepareWorkspace, never resolve silently.
func TestContainer_GitdirMirrorMountUnreadableGit(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions; cannot make .git unstattable")
	}
	proj := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(proj, ".git"),
		[]byte("gitdir: /repo/.git/worktrees/x\n"), 0o644))
	require.NoError(t, os.Chmod(proj, 0o000))
	t.Cleanup(func() { _ = os.Chmod(proj, 0o755) })

	_, ok, err := gitdirMirrorMount(context.Background(),
		fakeRuntime{name: "docker", available: true}, &git.Fake{CommonDirValue: "/repo/.git"}, proj)
	require.Error(t, err, "an unreadable .git must fail the workspace, not silently yield no mirror")
	assert.False(t, ok)
	assert.Contains(t, err.Error(), ".git")
}

// TestContainerWorkspace_CleanupSurfacesBaseError pins a regression. The
// base teardown's error was discarded with `_ =` under a comment asserting it
// "never contributes an error" — true today only because worktreeWorkspace.
// Cleanup happens to return nil unconditionally (it warns instead), which is a
// property of a DIFFERENT type in a different file that nothing binds to this
// one. The moment a base teardown does report a failure it would vanish. Join
// it instead, so the guarantee is structural rather than remote.
func TestContainerWorkspace_CleanupSurfacesBaseError(t *testing.T) {
	baseErr := fmt.Errorf("worktree teardown failed")

	// Base failure alone: nothing else went wrong, and it still surfaces.
	ws := &containerWorkspace{dir: "/proj", agentID: "m", baseCleanup: func() error { return baseErr }}
	err := ws.Cleanup()
	require.Error(t, err, "a base teardown failure must not be swallowed")
	assert.ErrorIs(t, err, baseErr)

	// Both halves fail: neither hides the other.
	root := brokenScratch(t)
	both := &containerWorkspace{dir: "/proj", agentID: "m", scratchRoot: root, baseCleanup: func() error { return baseErr }}
	done := captureStderr(t)
	err = both.Cleanup()
	_ = done()
	require.Error(t, err)
	assert.ErrorIs(t, err, baseErr)
	assert.Contains(t, err.Error(), "remove container scratch")
}

// TestContainer_CleanupKeepsOverlayTargets pins the ruling that the overlay
// mountpoints a container run needs inside the LIVE project are created and
// KEPT. They must be pre-created as the invoking user (containerConfigOverlay
// says why), and removing them again at teardown is unsafe: the project is
// shared, so a second run on it mounts the SAME targets, and one run's teardown
// removing an empty target detaches the path the other run's overlay is bound
// to. The accepted cost is an empty .claude/ and .ctxloom/cache/ in a
// container-only project.
//
// Driven through the real PrepareWorkspace and Cleanup, with two workspaces on
// one project: the first creates the targets, and its Cleanup must leave them
// for the second.
func TestContainer_CleanupKeepsOverlayTargets(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()
	targets := []string{".claude", filepath.FromSlash(".ctxloom/cache")}
	c := hermeticHostContainer(t, targets)
	c.state = SessionState{Harp: "brisk-teal-otter"}

	proj := t.TempDir()
	first, err := c.PrepareWorkspace(ctx, proj, "member-first")
	require.NoError(t, err)
	for _, rel := range targets {
		require.DirExists(t, filepath.Join(proj, rel), "premise: the first run created the overlay target")
	}
	second, err := c.PrepareWorkspace(ctx, proj, "member-second")
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Cleanup() })

	require.NoError(t, first.Cleanup())

	for _, rel := range targets {
		assert.DirExists(t, filepath.Join(proj, rel),
			"the first run's teardown must not remove a target the second run's overlay is still bound to")
	}
}

// hermeticHostContainer is a host-base Container whose whole prepare gate runs
// without a daemon: a fake runtime script that reports the image present and
// provenance-current, stubbed auth, and a stubbed shared-fs probe. The caller
// stamps the session state.
func hermeticHostContainer(t *testing.T, overlayDirs []string) Container {
	t.Helper()
	fake := t.TempDir()
	script := filepath.Join(fake, "fake-docker")
	labels := fmt.Sprintf(`{"ctxloom.provenance":%q}`, HostProvenanceDigest(""))
	writeFakeRuntimeScript(t, script, filepath.Join(fake, "builds.log"), fake, labels)
	require.NoError(t, os.WriteFile(filepath.Join(fake, "ctxloom-agent-hermetic-test_latest"), nil, 0o644))

	prevFS := sharedFSCheck
	sharedFSCheck = func(context.Context, Runtime, string, []string) error { return nil }
	t.Cleanup(func() { sharedFSCheck = prevFS })

	return Container{
		runtime: fakeRuntime{name: "docker", binary: script, available: true},
		image:   "ctxloom-agent-hermetic-test:latest",
		engineSpec: engineContainerSpec{
			engineInstall: []byte("RUN echo fake-install\n"),
			resolveAuth: func() (containerAuth, bool) {
				return containerAuth{mode: authEnv, envPassthrough: []string{"X"}}, true
			},
			overlayDirs: overlayDirs,
		},
		binaryPath: defaultContainerBinary,
		home:       defaultContainerHome,
		base:       hostBase{},
	}
}

// TestContainer_ScratchLivesUnderTheSessionEphemeralDir pins where a container
// run's host scratch goes: under the session's ephemeral dir, never the OS temp
// dir. An owner that dies before Cleanup then leaves it inside the session
// layout, where the session's own cleanup reaches it, instead of an orphaned
// ctxloom-iso-* in the temp dir that nothing ever collects.
func TestContainer_ScratchLivesUnderTheSessionEphemeralDir(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "brisk-teal-otter"
	c := hermeticHostContainer(t, []string{".claude"})
	c.state = SessionState{Harp: harp}

	ws, err := c.PrepareWorkspace(context.Background(), t.TempDir(), "member-scratch")
	require.NoError(t, err)
	cw := ws.(*containerWorkspace)
	root := cw.scratchRoot

	eph, err := paths.HarpEphemeralDir(harp)
	require.NoError(t, err)
	assert.Equal(t, eph, filepath.Dir(root), "the scratch root is a direct child of the session's ephemeral dir")
	assert.True(t, strings.HasPrefix(filepath.Base(root), "ctxloom-iso-"), "scratch root %q keeps its name prefix", root)
	require.DirExists(t, root)

	require.NoError(t, ws.Cleanup())
	assert.NoDirExists(t, root, "Cleanup still removes the scratch root")
}

// TestContainer_HarplessRunIsRefused: a container run with no usable harp has
// nowhere in the session layout to put its scratch, and is refused rather than
// falling back to the OS temp dir. Through the degrade chain the refusal is the
// fatal ClassIsolation finding, the same way an unpreparable state dir fails.
func TestContainer_HarplessRunIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		harp string
		want error
	}{
		"no harp":     {"", errNoSessionHarp},
		"unsafe harp": {"../evil", errUnsafeSessionHarp},
	} {
		t.Run(name, func(t *testing.T) {
			testsupport.Isolate(t)
			resetStrictness(t)
			c := hermeticHostContainer(t, []string{".claude"})
			c.state = SessionState{Harp: tc.harp}
			proj := t.TempDir()

			ws, err := c.ResolveWorkspace(context.Background(), proj, "member-harpless")
			require.ErrorIs(t, err, tc.want)
			assert.Nil(t, ws)

			mark := strictness.Checkpoint()
			done := captureStderr(t)
			policy, fallback := prepareChain(context.Background(), []Policy{c, None{}}, RuntimeContainerRootless, proj, "member-harpless")
			_ = done()
			found := strictness.Since(mark)
			strictness.Close(mark)
			t.Cleanup(func() { _ = fallback.Cleanup() })

			assert.Equal(t, None{}.Name(), policy.Name(), "the chain walks past the refused container")
			// The hermetic gate records findings of its own (the fake image has
			// no engine recipe and no ctxloom entrypoint); the one under test is
			// the one naming the harp refusal.
			var refusals []strictness.Finding
			for _, f := range found {
				if strings.Contains(f.Message, tc.want.Error()) {
					refusals = append(refusals, f)
				}
			}
			require.Len(t, refusals, 1, "the refusal is a recorded finding, never a silent host run: %v", found)
			assert.Equal(t, strictness.ClassIsolation, refusals[0].Class)
			assert.True(t, refusals[0].NonDegradable, "a requested container boundary is refused in both modes")
		})
	}
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
	const common = "/repo/.git"
	m, err := gitCommonDirMount(context.Background(),
		fakeRuntime{name: "docker", available: true},
		&git.Fake{CommonDirValue: common}, "/repo/wt")
	require.NoError(t, err)

	assert.Equal(t, common, m.Host, "the WHOLE common dir is the mount source (accepted blast radius)")
	assert.Equal(t, "/ctr"+common, m.Container, "mapped through the runtime's pathMapper, so a `gitdir:` pointer file resolves in-container")
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
