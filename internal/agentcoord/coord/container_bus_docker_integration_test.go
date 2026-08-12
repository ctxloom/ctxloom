//go:build docker_integration

// Shared docker-gated fixtures for this package's container integration
// tests: building the minimal test image (buildBusIntegrationImage) and
// tailing a child's live feed (feedTail/startFeedTail/waitForFeedText).
// container_direct_docker_integration_test.go's
// TestCoordContainerDirect_NoPluginNoPort is the live docker-direct proof
// that uses them now; the legacy plugin-listener bus round-trip test that
// used to live in this file (TestCoordContainerBus_RoundTrip, hand-rolling
// `docker run … llm serve mock` + the plugin magic cookie) exercised a
// production-dead path — Phase 1 replaced that container spawn — and was
// removed once TestCoordContainerDirect_NoPluginNoPort and the
// TestCoordOwnerRun_* suite covered its ground on the live docker-direct path.
//
// Run with:
//
//	just test-docker-integration
//	GOWORK=off just test-pkg ./internal/agentcoord/coord/... -tags docker_integration -run CoordContainerDirect
package coord

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// busIntegrationImage is this file's own minimal image (a distinct tag from
// internal/lm/isolation's ctxloom-iso-itest — different package, own
// namespace) — alpine + the freshly built static ctxloom binary, exactly
// mirroring isolation's buildIntegrationImage so the tree-under-test is
// always what this run actually built.
const busIntegrationImage = "ctxloom-coord-bus-itest:latest"

// buildBusIntegrationImage builds the static linux ctxloom into a minimal
// alpine image, targeting the HOST arch (a hardcoded amd64 binary would
// `exec format error` on an arm64 host, since `FROM alpine:latest` resolves
// the host's arch).
func buildBusIntegrationImage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	bin := filepath.Join(dir, "ctxloom")
	build := exec.Command("go", "build", "-o", bin, "github.com/ctxloom/ctxloom/cmd/ctxloom")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build static ctxloom: %v\n%s", err, out)
	}

	// WORKDIR is deliberate, not cosmetic: a bare "/" cwd trips a confirmed
	// latent bug in internal/mcp/mcp_runner.go's resolveCellPath (root=="/"
	// makes the "absRoot+separator" containment check compare against "//",
	// which no real absolute path ever has a prefix of, so EVERY relative
	// publish_paths/dest_path is rejected as "escapes the working
	// directory"). Real ctxloom agent images always set WORKDIR to the
	// bind-mounted project path (never bare "/"), so this never fires in
	// production; it is a real bug this test's image sidesteps rather than a
	// bus defect.
	dockerfile := "FROM alpine:latest\nWORKDIR /work\nCOPY ctxloom /usr/local/bin/ctxloom\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644))

	img := exec.Command("docker", "build", "-t", busIntegrationImage, dir)
	if out, err := img.CombinedOutput(); err != nil {
		t.Fatalf("docker build bus integration image: %v\n%s", err, out)
	}
	return busIntegrationImage
}

// feedTail accumulates every live-tap assistant entry's Content this test
// observes on one harp's feed, so multiple waitForFeedText calls against
// the SAME feed (a channel, drainable only once) can each look back over
// everything seen so far.
type feedTail struct {
	mu   sync.Mutex
	text strings.Builder
}

func startFeedTail(feed *operations.SessionFeed) *feedTail {
	ft := &feedTail{}
	go func() {
		for ev := range feed.Events {
			entry := ev.Event.GetEntry()
			if entry == nil || entry.GetContent() == "" {
				continue
			}
			ft.mu.Lock()
			ft.text.WriteString(entry.GetContent())
			ft.text.WriteString("\n---\n")
			ft.mu.Unlock()
		}
	}()
	return ft
}

func (ft *feedTail) snapshot() string {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.text.String()
}

// waitForFeedText polls the tail until want appears (or the deadline
// passes), returning the final snapshot for a failing assertion's message.
func waitForFeedText(ft *feedTail, want string, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	for {
		snap := ft.snapshot()
		if strings.Contains(snap, want) {
			return snap, true
		}
		if time.Now().After(deadline) {
			return snap, false
		}
		time.Sleep(25 * time.Millisecond)
	}
}
