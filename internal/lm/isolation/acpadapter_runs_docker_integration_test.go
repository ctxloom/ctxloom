//go:build docker_integration

// The 2026-07-24 container-delegation defect's regression gate.
//
// EVERY containerized claude-code agent's structured chat was dead: the agent
// image built GREEN, `claude-code-acp` was on PATH, and the adapter then
// died at startup with
//
//	SyntaxError: Unexpected token 'with'
//	  import packageJson from "../package.json" with { type: "json" };
//
// because claudeCodeInstallFragment's node prereq falls back to the base
// distro's `apt-get install nodejs`, which on Ubuntu 24.04 is Node 18.19.1 —
// older than the 18.20/20.10 that first parsed import attributes. The child
// therefore produced ZERO ChatEvents, its transcript carried only the briefing
// `user` record at seq 0, and the coordinator relaunched it forever
// (~1000 attempts over 77 minutes, all seq=0).
//
// The image-time gate could not catch it: it was `command -v claude-code-acp`,
// PATH PRESENCE ONLY — this project's signature silent no-op. These tests
// replace that with EXECUTION.
//
//	GOWORK=off just test-pkg ./internal/lm/isolation/... -tags docker_integration -run ACPAdapterRuns
package isolation

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// adapterProbeBase is the distro whose packaged nodejs is too old — the exact
// base that produced the live failure. Any base is legitimate input to these
// fragments (they run on "an ARBITRARY base", per their own doc), so the
// fragment must make its own node floor, not inherit one.
const adapterProbeBase = "ubuntu:24.04"

// buildFragmentImage builds adapterProbeBase + fragment and returns the tag.
func buildFragmentImage(t *testing.T, tag string, fragment []byte) string {
	t.Helper()
	dir := t.TempDir()
	body := "FROM " + adapterProbeBase + "\n" + string(fragment)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(body), 0o644))
	out, err := exec.Command("docker", "build", "-t", tag, dir).CombinedOutput()
	if err != nil {
		t.Fatalf("docker build %s failed:\n%s", tag, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })
	return tag
}

// assertAdapterExecutes runs the ACP adapter inside img with stdin closed. A
// healthy adapter reaches its stdio loop and exits on EOF; a broken one dies in
// the module loader. Asserting on the STDERR TEXT (not the exit code) is
// deliberate: a Node module-load failure and a clean EOF shutdown can both be
// non-zero, and it was precisely an exit-status-shaped check that missed this.
func assertAdapterExecutes(t *testing.T, img, adapter string) {
	t.Helper()
	out, _ := exec.Command("docker", "run", "--rm", "-i", "--entrypoint", "/bin/sh", img,
		"-c", "timeout 20 "+adapter+" < /dev/null 2>&1 | head -40").CombinedOutput()
	got := string(out)
	for _, bad := range []string{"SyntaxError", "Cannot find module", "ERR_MODULE_NOT_FOUND", "ERR_UNKNOWN_BUILTIN_MODULE"} {
		if strings.Contains(got, bad) {
			t.Fatalf("%s does not RUN in the image it was installed into (%q):\n%s\n"+
				"a PATH-presence gate cannot see this — the install must validate by execution", adapter, bad, got)
		}
	}
	t.Logf("%s startup output:\n%s", adapter, got)
}

// TestACPAdapterRuns_ClaudeCode is the headline gate: after
// claudeCodeInstallFragment, claude-code-acp must actually EXECUTE.
func TestACPAdapterRuns_ClaudeCode(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the claude-code-acp adapter-runs integration test")
	img := buildFragmentImage(t, "ctxloom-acpprobe-claude:latest", claudeCodeInstallFragment)
	ver, err := exec.Command("docker", "run", "--rm", "--entrypoint", "node", img, "--version").Output()
	require.NoError(t, err)
	t.Logf("node in image: %s", strings.TrimSpace(string(ver)))
	assertAdapterExecutes(t, img, "claude-code-acp")
}

// TestACPAdapterRuns_Codex is the identical gate for codex: codexInstallFragment
// carries the same best-effort node prereq and the same PATH-presence-only
// validate, so it is exposed to the identical failure.
func TestACPAdapterRuns_Codex(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the codex adapter-runs integration test")
	img := buildFragmentImage(t, "ctxloom-acpprobe-codex:latest", codexInstallFragment)
	assertAdapterExecutes(t, img, "codex-acp")
}
