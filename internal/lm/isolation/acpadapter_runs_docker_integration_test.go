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
	// The SAME vocabulary the in-image gate greps for (enginespec.go), split back
	// out — so the test and the gate cannot drift apart.
	for _, bad := range strings.Split(acpProbeFailurePatterns, "|") {
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

// ---------------------------------------------------------------------------
// The generalization (task minty-wilt): kiro and opencode.
//
// These two install from shell installers, not npm, so they were never exposed
// to the node-version defect above — but their ONLY build gate was `<client>
// --version`, which proves the client binary loads and nothing about
// `<client> acp`, the surface their structured chat actually spawns
// (internal/lm/backends' kiroACPTransport/opencodeACPTransport are
// agent.ACPNative). Same silent-no-op class, different first cause.
// ---------------------------------------------------------------------------

// buildFragmentImageErr is buildFragmentImage's non-fatal twin: it returns the
// build's combined output and error instead of failing the test, because the
// gate tests below assert that a build FAILS.
func buildFragmentImageErr(t *testing.T, tag, body string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(body), 0o644))
	out, err := exec.Command("docker", "build", "-t", tag, dir).CombinedOutput()
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })
	return string(out), err
}

// stubEngineLayer installs a shell stand-in for an engine client at
// /usr/local/bin/<client>: it is on PATH and answers `--version` happily, and
// its `acp` surface is BROKEN in the given way. This is what a half-succeeded
// installer or a pinned-old release looks like from the image's point of view —
// a green build containing a dead engine.
func stubEngineLayer(client, acpBehaviour string) string {
	return "FROM " + adapterProbeBase + "\n" +
		"RUN printf '%s\\n' '#!/bin/sh' 'if [ \"$1\" = \"--version\" ]; then echo \"" + client + " 9.9.9\"; exit 0; fi' " +
		"'" + acpBehaviour + "' > /usr/local/bin/" + client + " && chmod +x /usr/local/bin/" + client + "\n"
}

// The two REAL broken-engine shapes, measured live 2026-07-24 by running the
// host's own kiro-cli and opencode with an unrecognized subcommand:
//
//   - kiro-cli (clap) rejects it: `error: unrecognized subcommand 'acpXX'`, exit 2.
//   - opencode does NOT reject it — it adopts the token as a working directory
//     and reports `Error: Failed to change directory to <cwd>/acpXX`, EXIT 0.
//
// opencode's shape is the reason these gates match on stderr TEXT: a
// status-shaped gate sees exit 0 and calls a dead engine healthy.
const (
	kiroBrokenACP     = `echo "error: unrecognized subcommand '"'"'$1'"'"'" >&2; exit 2`
	opencodeBrokenACP = `echo "Error: Failed to change directory to /work/$1" >&2; exit 0`
)

// TestNativeACPGate_RedAgainstBrokenEngine is the red half, permanently
// encoded: for each native-ACP engine, an image whose client is PRESENT and
// answers --version but whose `acp` surface is dead
//
//	(a) BUILDS GREEN under the old `<client> --version` gate — the hole, and
//	(b) FAILS THE BUILD under nativeACPRunGate — the fix.
//
// (b) failing while (a) passes is the whole point; if (a) ever starts failing,
// the old gate was not as blind as documented and this test should be re-read.
func TestNativeACPGate_RedAgainstBrokenEngine(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the native ACP gate broken-engine test")
	for _, tc := range []struct {
		client   string
		broken   string
		wantText string
	}{
		{"kiro-cli", kiroBrokenACP, "unrecognized subcommand"},
		{"opencode", opencodeBrokenACP, "Failed to change directory"},
	} {
		t.Run(tc.client, func(t *testing.T) {
			stub := stubEngineLayer(tc.client, tc.broken)

			// (a) the OLD gate: `--version` only. A dead engine ships green.
			oldGate := stub + "RUN " + tc.client + " --version\n"
			out, err := buildFragmentImageErr(t, "ctxloom-acpprobe-oldgate-"+tc.client+":latest", oldGate)
			require.NoError(t, err, "the OLD --version-only gate is supposed to be blind to this; if it caught it, re-read the premise:\n%s", out)

			// (b) the NEW gate: run the surface structured chat actually spawns.
			newGate := stub + "RUN true \\\n" + nativeACPRunGate(tc.client, "acp")
			out, err = buildFragmentImageErr(t, "ctxloom-acpprobe-newgate-"+tc.client+":latest", newGate)
			require.Error(t, err, "a broken %s acp MUST fail the build:\n%s", tc.client, out)
			require.Contains(t, out, tc.wantText, "the build must SAY what was wrong, not just fail:\n%s", out)
			require.Contains(t, out, tc.client+" acp is installed but its ACP surface cannot start",
				"the gate must name the surface it probed:\n%s", out)
		})
	}
}

// TestNativeACPGate_PassesAHealthyEngine is the green half: the same gate over
// a client whose `acp` behaves like the real ones do (measured live: exit 0,
// no output at all on stdin EOF) must NOT fail the build. Without this, a gate
// that failed everything would look identical to a gate that works.
func TestNativeACPGate_PassesAHealthyEngine(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the native ACP gate healthy-engine test")
	stub := stubEngineLayer("kiro-cli", `cat >/dev/null; exit 0`)
	body := stub + "RUN true \\\n" + nativeACPRunGate("kiro-cli", "acp")
	out, err := buildFragmentImageErr(t, "ctxloom-acpprobe-healthy:latest", body)
	require.NoError(t, err, "a healthy acp surface must build clean:\n%s", out)
}

// TestACPAdapterRuns_Kiro builds the REAL kiroInstallFragment and proves
// `kiro-cli acp` comes up in the image it was installed into. Needs network
// (the official installer).
func TestACPAdapterRuns_Kiro(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the kiro adapter-runs integration test")
	img := buildFragmentImage(t, "ctxloom-acpprobe-kiro:latest", kiroInstallFragment)
	assertAdapterExecutes(t, img, "kiro-cli acp")
}

// TestACPAdapterRuns_Opencode is the same for opencode.
func TestACPAdapterRuns_Opencode(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the opencode adapter-runs integration test")
	img := buildFragmentImage(t, "ctxloom-acpprobe-opencode:latest", opencodeInstallFragment)
	assertAdapterExecutes(t, img, "opencode acp")
}
