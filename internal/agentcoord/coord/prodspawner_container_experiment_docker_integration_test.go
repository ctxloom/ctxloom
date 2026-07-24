//go:build docker_integration

// grave-able's DECISIVE EXPERIMENT (local, minimal, NOT the progress-asserting
// container test light-foe is building): does a `mock`-backend agent driven
// through the PRODUCTION spawner (prodSpawner — config-resolved agent,
// operations.PrepareAgentChat, real isolation) with runtime: container fail the
// same way the 2026-07-24 claude-code container children did (transcript = 100%
// `user` entries, seq never leaving 0)?
//
// Unlike container_direct_docker_integration_test.go, whose directBusSpawner
// hand-rolls StartEngine, this drives Options{Cfg: ...} so the coordinator
// builds newProdSpawner itself — the production route.
//
//	GOWORK=off just test-pkg ./internal/agentcoord/coord/... -tags docker_integration -run ProdSpawnerContainerExperiment
package coord

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// experimentBaseImage is a REAL ctxloom-built agent image already on this host
// (it carries the ctxloom-entrypoint the run-as-is identity contract needs);
// the experiment image layers THIS tree's ctxloom binary over it so the
// in-container runner is the code under test.
const experimentBaseImage = "ctxloom-agent:0cdb268e1b0f"

const experimentImage = "ctxloom-experiment-grave-able:latest"

func buildExperimentImage(t *testing.T) string {
	t.Helper()
	if err := exec.Command("docker", "image", "inspect", experimentBaseImage).Run(); err != nil {
		t.Skipf("base agent image %s absent; skipping", experimentBaseImage)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "ctxloom")
	build := exec.Command("go", "build", "-o", bin, "github.com/ctxloom/ctxloom/cmd/ctxloom")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build static ctxloom: %v\n%s", err, out)
	}
	dockerfile := "FROM " + experimentBaseImage + "\nCOPY --chmod=0755 ctxloom /usr/local/bin/ctxloom\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644))
	img := exec.Command("docker", "build", "-t", experimentImage, dir)
	if out, err := img.CombinedOutput(); err != nil {
		t.Fatalf("docker build experiment image: %v\n%s", err, out)
	}
	return experimentImage
}

// transcriptCensus is the measurement the incident report used: how many
// records, how many DISTINCT seq values, and the entry-type histogram.
type transcriptCensus struct {
	lines    int
	seqs     map[int]int
	kinds    map[string]int
	entries  map[string]int
	rawFirst string
}

func censusTranscript(t *testing.T, harp string) transcriptCensus {
	t.Helper()
	c := transcriptCensus{seqs: map[int]int{}, kinds: map[string]int{}, entries: map[string]int{}}
	p, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Logf("census: no transcript at %s: %v", p, err)
		return c
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		c.lines++
		if c.rawFirst == "" {
			c.rawFirst = line
		}
		var rec struct {
			Seq   int    `json:"seq"`
			Kind  string `json:"kind"`
			Entry *struct {
				Type string `json:"type"`
			} `json:"entry"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		c.seqs[rec.Seq]++
		c.kinds[rec.Kind]++
		if rec.Entry != nil {
			c.entries[rec.Entry.Type]++
		}
	}
	return c
}

func (c transcriptCensus) String() string {
	return fmt.Sprintf("lines=%d distinctSeq=%d seqs=%v kinds=%v entries=%v", c.lines, len(c.seqs), c.seqs, c.kinds, c.entries)
}

func writeExperimentConfig(t *testing.T, appDir, body string) *config.Config {
	t.Helper()
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0o644))
	cfg, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	return cfg
}

// runExperiment drives ONE agent_run through the production spawner and
// returns the child's harp plus the transcript census after settleFor.
func runExperiment(t *testing.T, agentName, cfgBody string, settleFor time.Duration) (string, transcriptCensus) {
	t.Helper()
	resetStrictness(t)
	t.Setenv("ANTHROPIC_API_KEY", "grave-able-experiment-key")
	projectDir := testsupport.ProjectDir(t)
	// A git repo so the worktree axis has something to work with; the run
	// itself asks for workspace "none".
	for _, args := range [][]string{{"init"}, {"config", "user.email", "e@example.com"}, {"config", "user.name", "E"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = projectDir
		require.NoError(t, cmd.Run(), "git %v", args)
	}
	appDir := filepath.Join(projectDir, ".ctxloom")
	cfg := writeExperimentConfig(t, appDir, cfgBody)

	c, err := New(Options{ProjectDir: projectDir, ProjectKey: "grave-able-exp", Cfg: cfg})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	seed := "GRAVE-ABLE-SEED-" + randID("", 6)
	out, err := c.AgentRun(ctx, ownerIdentity(), agentName, seed, "none", "")
	require.NoError(t, err)
	t.Logf("agent_run → harp=%s engine=%s runtime=%s degraded=%v", out.Harp, out.Engine, out.Runtime, out.Degraded)

	deadline := time.Now().Add(settleFor)
	for time.Now().Before(deadline) {
		cen := censusTranscript(t, out.Harp)
		if cen.entries["assistant"] > 0 {
			t.Logf("census (assistant seen): %s", cen)
			return out.Harp, cen
		}
		time.Sleep(500 * time.Millisecond)
	}
	cen := censusTranscript(t, out.Harp)
	t.Logf("census (settled, no assistant): %s", cen)
	t.Logf("first record: %s", cen.rawFirst)
	return out.Harp, cen
}

const experimentAgents = `version: 6
isolation_images:
  mock: ` + experimentImage + `
agents:
  mockbox:
    engine: mock
    permissions: bypass
    runtime: container
  mockhost:
    engine: mock
    permissions: bypass
    runtime: host
`

// TestProdSpawnerContainerExperiment_MockHostControl is the CONTROL: the same
// production path, runtime host. If this is healthy and the container variants
// are not, the split is the runtime axis.
func TestProdSpawnerContainerExperiment_MockHostControl(t *testing.T) {
	_, cen := runExperiment(t, "mockhost", experimentAgents, 60*time.Second)
	t.Logf("HOST CONTROL census: %s", cen)
	require.Positive(t, cen.lines, "host control must produce a transcript")
}

// TestProdSpawnerContainerExperiment_MockContainerLegacy drives mock (NOT in
// viaStartRunBackends) with runtime container — the legacy go-plugin dial.
func TestProdSpawnerContainerExperiment_MockContainerLegacy(t *testing.T) {
	if !(isolation.Docker{}).Available() {
		t.Skip("docker unavailable")
	}
	buildExperimentImage(t)
	_, cen := runExperiment(t, "mockbox", experimentAgents, 120*time.Second)
	t.Logf("CONTAINER (legacy dial) census: %s", cen)
}

// TestProdSpawnerContainerExperiment_MockContainerStartRun forces mock onto the
// StartRun path — the wire path the failing claude-code container children took.
// EXPERIMENT-ONLY mutation of viaStartRunBackends; never shipped.
func TestProdSpawnerContainerExperiment_MockContainerStartRun(t *testing.T) {
	if !(isolation.Docker{}).Available() {
		t.Skip("docker unavailable")
	}
	buildExperimentImage(t)
	viaStartRunBackends["mock"] = true
	t.Cleanup(func() { delete(viaStartRunBackends, "mock") })
	_, cen := runExperiment(t, "mockbox", experimentAgents, 180*time.Second)
	t.Logf("CONTAINER (StartRun path) census: %s", cen)
}
