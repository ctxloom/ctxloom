package isolation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// windowsStyleMapper is a TEST-ONLY stand-in for lanky-pod's eventual Windows
// pathMapper (container-runtime-bugs.plan.md §4/§5): it maps ANY host path to
// a single canonical POSIX in-container root, mirroring the plan's design
// ("choose a canonical in-container mount root … and translate C:\Users\foo\
// proj → e.g. /workspace"). It is deliberately simplistic (no drive-letter
// parsing, no Docker-Desktop /host_mnt SOURCE translation) — exercising it
// here proves the SEAM (buildRunSpec/ExposeMapped route every identical-
// path Mount + WorkDir through whatever mapper the runtime carries), not a
// production-ready Windows implementation, which the plan explicitly scopes
// out of this task (untested-without-hardware).
type windowsStyleMapper struct{ root string }

func (m windowsStyleMapper) toContainer(string) string { return m.root }

// prefixMapper is a TEST-ONLY non-identity pathMapper that maps hostPath to
// prefix+hostPath. Unlike windowsStyleMapper (which collapses EVERY host path
// to one canonical root, and so cannot tell two different remapped paths
// apart), prefixMapper stays INJECTIVE: distinct host paths still map to
// distinct container paths. That is what makes it useful as the default
// mapper for the package's fakeRuntime/reapRuntime test doubles — a call
// site that skips the mapper and hands back the raw host path produces
// output a prefixMapper-backed assertion can tell apart from the real
// (mapped) contract, closing the gap identityMapper's Host==Container leaves
// (a skipped mapper call is byte-identical to a used one under identity).
type prefixMapper struct{ prefix string }

func (m prefixMapper) toContainer(hostPath string) string { return m.prefix + hostPath }

// TestIdentityMapper_NoOp pins the seam's zero-behavior-change contract: the
// default mapper is a pure passthrough.
func TestIdentityMapper_NoOp(t *testing.T) {
	m := identityMapper{}
	assert.Equal(t, "/home/user/proj", m.toContainer("/home/user/proj"))
}

// TestRuntimeMapper_NilIsIdentity pins the nil-safe getter every Runtime's
// mapper() uses: an unset pathMapper (every construction path in this package
// today) is identity, never a nil-pointer hazard.
func TestRuntimeMapper_NilIsIdentity(t *testing.T) {
	got := runtimeMapper(nil)
	assert.Equal(t, "/x", got.toContainer("/x"), "nil mapper must behave as identity")
}

// TestBuildRunSpec_IdentityMapper_Unchanged pins lanky-pod's "no behavior
// change on Linux" contract at buildRunSpec specifically: a nil mapper (what
// every current caller passes — containerRunnerFunc reads it off rt.mapper(),
// identity for every Docker/Podman/Host construction path today) renders
// byte-for-byte the identical-path project mount + WorkDir buildRunSpec always
// built, matching TestBuildRunSpec_WiresAuthHandshakeAndMounts's assertions
// (provision_test.go) with an explicit nil mapper argument.
func TestBuildRunSpec_IdentityMapper_Unchanged(t *testing.T) {
	spec := buildRunSpec("img", "name", "/proj", "/root",
		[]string{"/usr/local/bin/ctxloom", "llm", "serve", "mock"},
		"/run/ctxloom/plugin", "/tmp/host-sock/plugin1",
		nil, nil, nil, nil)

	assert.Equal(t, "/proj", spec.WorkDir, "nil mapper: WorkDir is the unmapped project dir")
	assert.Contains(t, spec.Mounts, Mount{Host: "/proj", Container: "/proj"},
		"nil mapper: the project mount is identical-path")
}

// TestBuildRunSpec_WindowsStyleMapper_TranslatesWorkDirAndProjectMount is
// lanky-pod's pure, no-daemon proof: injecting a non-identity mapper changes
// the project mount's CONTAINER target and WorkDir to the mapped POSIX path,
// while the mount's HOST side (what docker/podman actually reads off disk)
// stays the real host path unchanged — exactly the seam's contract (§5 of the
// plan): "buildRunSpec builds the project mount, socket mount, gitdir mirror,
// overlays, and WorkDir THROUGH the mapper."
func TestBuildRunSpec_WindowsStyleMapper_TranslatesWorkDirAndProjectMount(t *testing.T) {
	const hostProj = `C:\Users\foo\proj`
	mapper := windowsStyleMapper{root: "/workspace"}

	spec := buildRunSpec("img", "name", hostProj, "/root",
		[]string{"/usr/local/bin/ctxloom", "llm", "serve", "mock"},
		"/run/ctxloom/plugin", "/tmp/host-sock/plugin1",
		nil, nil, nil, mapper)

	assert.Equal(t, "/workspace", spec.WorkDir,
		"WorkDir must be the mapped POSIX target, not the raw Windows host path")
	assert.Contains(t, spec.Mounts, Mount{Host: hostProj, Container: "/workspace"},
		"the project mount's SOURCE stays the real host path; only the CONTAINER target is mapped")

	// The socket-dir mount is a FIXED in-container convention, not host-
	// derived — it must NOT route through the mapper.
	assert.Contains(t, spec.Mounts, Mount{Host: "/tmp/host-sock/plugin1", Container: "/run/ctxloom/plugin"},
		"the socket-dir mount target is the fixed containerSocketDir convention, unaffected by the project-path mapper")

	// Render sanity: the WorkDir flag and the mount's CONTAINER (target) side
	// are the mapped POSIX path — no Windows-style separator in either. The
	// mount's HOST (source) side legitimately keeps the real Windows path
	// here (this test's mapper does not attempt the Docker-Desktop
	// /host_mnt SOURCE translation — a known, documented, separate concern;
	// see pathMapper's doc).
	argv := strings.Join(Docker{rootless: true}.RunArgs(spec), " ")
	assert.Contains(t, argv, "-w /workspace")
	assert.Contains(t, argv, "target=/workspace")
	assert.NotContains(t, argv, `target=C:\`, "the CONTAINER (target) side must never carry a Windows-style path")
}

// TestOciRuntime_ExposeMapped_RoutesThroughMapper pins the OTHER lanky-pod
// call site (gitCommonDirMount's mirror mount): ExposeMapped must apply
// the SAME mapper buildRunSpec uses, so a linked-worktree's common-dir mirror
// and the project mount never disagree about the in-container root.
func TestOciRuntime_ExposeMapped_RoutesThroughMapper(t *testing.T) {
	// Identity (default): unchanged behavior.
	plain := Docker{}
	assert.Equal(t, Mount{Host: "/repo/.git", Container: "/repo/.git"}, plain.ExposeMapped("/repo/.git", false),
		"a Docker runtime constructed without a mapper (every call site today) is identity")

	// Non-identity: injected via the unexported ociRuntime.pathMap field
	// (same-package test, mirroring how the plan's design injects a future
	// Windows/DooD mapper at runtime construction).
	mapped := Docker{ociRuntime: ociRuntime{pathMap: windowsStyleMapper{root: "/workspace"}}}
	assert.Equal(t, Mount{Host: `C:\Users\foo\proj\.git`, Container: "/workspace", ReadOnly: true},
		mapped.ExposeMapped(`C:\Users\foo\proj\.git`, true),
		"ExposeMapped must route the container target through the SAME mapper buildRunSpec uses")
}
