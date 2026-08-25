package isolation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/git"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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

func (f fakeRuntime) Name() string                                     { return f.name }
func (f fakeRuntime) Binary() string                                   { return f.binary }
func (f fakeRuntime) Available() bool                                  { return f.available }
func (fakeRuntime) RunArgs(RunSpec) []string                           { return nil }
func (fakeRuntime) RemoveArgs(string) []string                         { return nil }
func (fakeRuntime) ExecArgs(string, bool, []string, []string) []string { return nil }

// Spawn is never reached by the Prepare-only degrade tests (they stop at the gate
// or the mount wiring); it errors loudly if one ever does call it.
func (fakeRuntime) Spawn(LaunchSpec) (pb.Client, error) {
	return nil, fmt.Errorf("fakeRuntime: Spawn not expected in these tests")
}

// Expose is the OCI identity bind mount, so tests that route delivery mounts
// through the runtime (sessionStateMounts, gitCommonDirMount) see the same Mount
// the literal produced.
func (fakeRuntime) Expose(host, target string, readOnly bool) Mount {
	return Mount{Host: host, Container: target, ReadOnly: readOnly}
}

// ExposeIdentical mirrors ociRuntime's default (identityMapper): fakeRuntime
// carries no pathMapper, so this is Expose(hostPath, hostPath, readOnly).
func (fakeRuntime) ExposeIdentical(hostPath string, readOnly bool) Mount {
	return Mount{Host: hostPath, Container: hostPath, ReadOnly: readOnly}
}

// mapper is always identity — fakeRuntime never carries a non-identity mapper;
// tests that need to exercise a non-identity mapper inject one directly at
// buildRunSpec (see runner_test.go), not through this fake.
func (fakeRuntime) mapper() pathMapper { return identityMapper{} }

// TestContainer_MCPCommandOverride pins a fix at its source: a
// container policy (either base tier — hostBase and worktreeBase share the
// same binaryPath field) reports the in-container ctxloom binary
// (defaultContainerBinary) as its MCP command override, the single source of
// truth threaded from NewContainerFor. This is the value
// operations.MCPCommandOverrideForPolicy relays onto the run env
// (agent.MCPCommandOverrideEnv) so the MCP-surface writer stamps a `command`
// the container can actually exec instead of the host self-exec path.
func TestContainer_MCPCommandOverride(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "")
	assert.Equal(t, defaultContainerBinary, c.MCPCommandOverride())
	assert.Equal(t, "/usr/local/bin/ctxloom", c.MCPCommandOverride(), "the documented in-container path — a change here is a wire-contract change")

	wt := NewContainerWorktreeFor(fakeRuntime{name: "docker", available: true}, "", ImageConfig{}, nil)
	assert.Equal(t, defaultContainerBinary, wt.MCPCommandOverride(), "the worktree-in-container base shares the same binaryPath field")
}

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
	assert.Equal(t, Mount{Host: common, Container: common}, m,
		"the common dir is mirrored identical-path so gitdir resolves in-container")

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
// success return with zero payload delivered, and the caller (internal/acp's
// container transport) would go on to speak JSON-RPC at whatever the image's
// entrypoint happens to be. The refusal must assert on the PAYLOAD (no spec, an
// error naming the empty command), never on an exit code.
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

// TestContainer_WithImageRunsAsIs pins a regression. A caller-supplied
// image is USER-OWNED: nothing ctxloom authored — the identity-remap entrypoint
// included — is guaranteed to be in it, so it must be run AS-IS (never locally
// rebuilt) and it must face checkRunAsIsIdentity's pre-start contract check, the
// one signal there is before a wrong-identity container root-owns every file it
// writes into the mounted project.
//
// containerFor already did that for an isolation_images override by clearing the
// spec's build recipe. WithImage — the override internal/acp's container
// transport uses for a per-agent container_image — swapped the image and left
// the recipe in place, so runAsIs() stayed false, the identity check never ran,
// and ensureImage would try to BUILD the user's tag locally when absent. The two
// override paths must agree.
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

// TestHostBase_PrunesOverlayTargetsItCreated pins a regression. The
// overlay TARGET dirs must be pre-created as the invoking user — otherwise a
// rootful daemon creates the bind mountpoint as ROOT inside the identical-path
// project bind, EACCES-ing every later host run. But they were created inside the
// user's HOST project and never removed: Cleanup only removed the scratch tree,
// so preparing a container run left `.claude/` and `.ctxloom/cache/` behind in a
// project that never had them, as a side effect of a run that writes nothing
// there (the overlay shadows them; every write lands in scratch).
//
// Only what we created, only while still empty, and never a directory the
// project already had.
func TestHostBase_PrunesOverlayTargetsItCreated(t *testing.T) {
	ctx := context.Background()
	rt := fakeRuntime{name: "docker", available: true}
	proj := t.TempDir()
	scratch := t.TempDir()

	// A directory the project already owns, with content: untouchable.
	preexisting := filepath.Join(proj, ".kept")
	require.NoError(t, os.MkdirAll(preexisting, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(preexisting, "user.json"), []byte("{}"), 0o644))

	spec := engineContainerSpec{overlayDirs: []string{".kept", ".claude", filepath.FromSlash(".ctxloom/cache")}}
	_, _, cleanup, err := hostBase{}.prepareBase(ctx, rt, proj, "m", scratch, spec, &git.Fake{})
	require.NoError(t, err)

	for _, rel := range []string{".claude", filepath.FromSlash(".ctxloom/cache")} {
		require.DirExists(t, filepath.Join(proj, rel), "the overlay target must exist before the daemon sees the mount")
	}

	require.NoError(t, cleanup())

	assert.NoDirExists(t, filepath.Join(proj, ".claude"),
		"an overlay target this run created must not outlive it in the user's project")
	assert.NoDirExists(t, filepath.Join(proj, ".ctxloom"),
		"…including the intermediate directories created with it")
	assert.FileExists(t, filepath.Join(preexisting, "user.json"),
		"a directory the project already owned is never pruned")
}

// TestHostBase_KeepsOverlayTargetsThatGainedContent: pruning is empty-only.
// Anything that landed in a target dir belongs to the user (or to a writer the
// overlay did not shadow), so the directory stays — and its parents with it.
func TestHostBase_KeepsOverlayTargetsThatGainedContent(t *testing.T) {
	ctx := context.Background()
	proj := t.TempDir()

	spec := engineContainerSpec{overlayDirs: []string{filepath.FromSlash(".ctxloom/cache")}}
	_, _, cleanup, err := hostBase{}.prepareBase(ctx, fakeRuntime{name: "docker", available: true},
		proj, "m", t.TempDir(), spec, &git.Fake{})
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(proj, ".ctxloom", "cache", "landed"), []byte("x"), 0o644))
	require.NoError(t, cleanup())

	assert.FileExists(t, filepath.Join(proj, ".ctxloom", "cache", "landed"),
		"a target that gained content is never removed")
}

// TestGitCommonDirMount_WholeCommonDirReadWrite pins the ACCEPTED posture a
// review row re-opened. The row's facts are correct: the entire git common dir is
// bind-mounted READ-WRITE at its identical path, so a low-trust
// container-worktree member can reach the main checkout's refs/objects/index and
// every other worktree's admin dir. That exposure is real and was adjudicated in
// the tree before this wave (see gitCommonDirMount's own DECISION block): the
// per-worktree admin dir a linked checkout needs is a SUBDIRECTORY of the common
// dir, git needs write access to refs/logs and the packed-refs/objects layout,
// and a surgical partial mount is fragile in ways that are easy to get subtly
// wrong. Narrowing it is a per-agent-git-isolation design decision, not a sweep's
// call — escalated, not changed here.
//
// What this pins is the posture itself, in both directions: read-only would
// break every linked-worktree container run, and a non-identical path would
// break the `gitdir:` pointer that made the mount necessary. A change to either
// must be deliberate.
func TestGitCommonDirMount_WholeCommonDirReadWrite(t *testing.T) {
	const common = "/repo/.git"
	m, err := gitCommonDirMount(context.Background(),
		fakeRuntime{name: "docker", available: true},
		&git.Fake{CommonDirValue: common}, "/repo/wt")
	require.NoError(t, err)

	assert.Equal(t, common, m.Host, "the WHOLE common dir is the mount source (accepted blast radius)")
	assert.Equal(t, common, m.Container, "identical-path, so a `gitdir:` pointer file resolves in-container")
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
		Engines:             []string{"claude-code", "kiro"},
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
