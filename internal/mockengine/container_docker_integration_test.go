//go:build docker_integration

// Docker-gated proof that the mock engine, running AS the claude binary INSIDE
// a container over a workspace ctxloom itself materialized, reports discovering
// exactly the surfaces that were delivered (present:true, matching hashes) and
// correctly reports the ones that were NOT (present:false).
//
// ============================ SCOPE BOUNDARY ============================
// This test proves CONTEXT DELIVERY — that ctxloom put the right bytes at the
// paths the claude CLI probes, reachable from inside the container, delivered
// through the argv/stdin contract L1 declares. It proves NOTHING about whether
// the REAL claude engine can RUN in any image: the mock is a Go binary and runs
// fine regardless of the image's Node version, npm graph, or auth — the exact
// blind spot task fiery-pasta records. The 2026-07-24 container-delegation
// incident (an image whose Node was too old to load claude-code-acp) would have
// sailed through this test green. Image runtime health is a SEPARATE,
// non-skippable check: adapterRunGate / TestACPAdapterRuns_* in
// internal/lm/isolation, which validates the real engine by EXECUTION. Do not
// cite a green run here as evidence that the image's real engine works.
// =======================================================================
//
//	just test-docker-integration
//	GOWORK=off just test-pkg ./internal/mockengine/... -tags docker_integration -run MockEngineContainer
package mockengine_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/mockengine"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

const mockEngineImage = "ctxloom-mockengine-itest:latest"

// hashHex is the sha256-lowercase-hex the runtime uses, for host-side expected
// values.
func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// dockergateRequire routes container-runtime reachability through the single
// gate (a bare t.Skip is banned in docker-gated files by _check-docker-skip-gate).
func dockergateRequire(t *testing.T, what string) {
	t.Helper()
	dockergate.RequireRuntime(t, isolation.Docker{}.Available(), what)
}

// buildMockEngineImage builds the static linux mockengine into a minimal alpine
// image (host arch, so `FROM alpine` resolves compatibly), mirroring the bus
// integration image build. WORKDIR /work is the bind-mount target the run uses
// as the engine's cwd — ScopeCwd probes resolve against it.
func buildMockEngineImage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "mockengine")
	build := exec.Command("go", "build", "-o", bin, "github.com/ctxloom/ctxloom/cmd/mockengine")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build static mockengine: %v\n%s", err, out)
	}
	dockerfile := "FROM alpine:latest\nWORKDIR /work\nCOPY mockengine /usr/local/bin/mockengine\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "build", "-t", mockEngineImage, dir).CombinedOutput(); err != nil {
		t.Fatalf("docker build mock engine image: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", mockEngineImage).Run() })
	return mockEngineImage
}

// materializeClaudeContext runs ctxloom's REAL claude surface materialization
// for the context surface into workspace, and returns the sha256 of the bytes
// ctxloom actually wrote (the managed-marker merge wraps the input, so the mock
// must match the FILE, not the raw input). Only the context surface is
// delivered, so mcp/settings/commands/skills/agents stay absent — their
// present:false rows are the point.
func materializeClaudeContext(t *testing.T, workspace, context string) string {
	t.Helper()
	set := backends.BuildSurfaces("claude-code", agent.SurfaceInputs{Context: context}, nil)
	var delivered bool
	for _, d := range set.Deliveries() {
		kd, ok := d.(agent.KindedDelivery)
		if !ok || kd.Kind() != agent.SurfaceContext {
			continue
		}
		if _, err := d.Deliver(workspace); err != nil {
			t.Fatalf("materialize context: %v", err)
		}
		delivered = true
	}
	if !delivered {
		t.Fatal("claude declared no context delivery — cannot set up the test")
	}
	b, err := os.ReadFile(filepath.Join(workspace, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("ctxloom's context delivery wrote no CLAUDE.md at the well-known path: %v", err)
	}
	sum := hashHex(b)
	t.Logf("ctxloom wrote CLAUDE.md (%d bytes, sha256 %s)", len(b), sum)
	return sum
}

// TestMockEngineContainer_DiscoversDeliveredSurfaces is the deliverable.
func TestMockEngineContainer_DiscoversDeliveredSurfaces(t *testing.T) {
	dockergateRequire(t, "the mock-engine container context-delivery test")

	workspace := t.TempDir()
	wantContextHash := materializeClaudeContext(t, workspace, "# Project rules\nAlways run the tests.\nDeliver evidence, not existence.\n")

	img := buildMockEngineImage(t)

	// Run the mock AS claude oneshot, inside the container, over the
	// materialized workspace. The argv mirrors what claude's buildArgs emits
	// under SkipSetup (--print --output-format json --model). The prompt goes on
	// stdin, exactly as L1 declares for claude oneshot. The report is written to
	// a file in the mounted workspace so we read it back host-side.
	prompt := "summarize the project rules"
	cmd := exec.Command("docker", "run", "--rm", "-i",
		"-v", workspace+":/work", "-w", "/work",
		"-e", "CTXLOOM_MOCK_REPORT_FILE=/work/report.json",
		img, "/usr/local/bin/mockengine",
		"--claude", "--print", "--output-format", "json", "--model", "mock-model",
	)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("mock engine container run failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	// The oneshot JSON envelope on stdout — proof the mock ran AS the engine and
	// the driver's SkipSetup decode would have succeeded.
	var env struct {
		Result     string                    `json:"result"`
		ModelUsage map[string]map[string]int `json:"modelUsage"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout was not the JSON envelope the driver parses: %v\nstdout: %s", err, stdout.String())
	}
	if env.Result == "" {
		t.Fatal("envelope result empty")
	}

	// Read the discovery report the mock wrote into the mounted workspace.
	rb, err := os.ReadFile(filepath.Join(workspace, "report.json"))
	if err != nil {
		t.Fatalf("no report.json in the workspace; stderr:\n%s", stderr.String())
	}
	var rep mockengine.Report
	if err := json.Unmarshal(rb, &rep); err != nil {
		t.Fatalf("report did not parse: %v\n%s", err, rb)
	}

	if rep.Engine != "claude-code" || rep.Surface != "oneshot" {
		t.Fatalf("report identity = %s/%s, want claude-code/oneshot", rep.Engine, rep.Surface)
	}

	// DELIVERED: the context file the mock discovered inside the container must
	// match the bytes ctxloom wrote on the host — same file, seen across the
	// container boundary.
	ctx := recordFor(t, rep, "context", "cwd")
	if !ctx.Present {
		t.Fatalf("CLAUDE.md was materialized but the mock reported it absent inside the container: %+v", ctx)
	}
	if ctx.SHA256 != wantContextHash {
		t.Fatalf("context hash mismatch across the container boundary: got %s want %s", ctx.SHA256, wantContextHash)
	}

	// ABSENT: surfaces that were NEVER delivered must report present:false — the
	// silent-no-op signal. .claude/agents/ has no delivery surface at all;
	// .mcp.json was not delivered in this run.
	if agents := recordFor(t, rep, "agents", "cwd"); agents.Present {
		t.Fatalf("agents surface reported present but was never delivered: %+v", agents)
	}
	if mcp := recordFor(t, rep, "mcp", "cwd"); mcp.Present {
		t.Fatalf("mcp surface reported present but was never delivered: %+v", mcp)
	}
	if rep.DiscoveryDigest == "" {
		t.Fatal("no discovery digest")
	}

	t.Logf("discovery digest: %s", rep.DiscoveryDigest)
	for _, r := range rep.Records {
		t.Logf("  probe order=%d kind=%s scope=%s present=%t size=%d sha256=%s rel=%s",
			r.Order, r.Kind, r.Scope, r.Present, r.Size, r.SHA256, r.Rel)
	}
}

// recordFor returns the (kind, scope) record or fails.
func recordFor(t *testing.T, rep mockengine.Report, kind, scope string) mockengine.ProbeRecord {
	t.Helper()
	for _, r := range rep.Records {
		if r.Kind == kind && r.Scope == scope {
			return r
		}
	}
	t.Fatalf("no probe record for kind=%s scope=%s", kind, scope)
	return mockengine.ProbeRecord{}
}
