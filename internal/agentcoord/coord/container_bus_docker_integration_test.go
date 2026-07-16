//go:build docker_integration

// This file is crazy-yarn's docker-gated proof that the coordinator<->
// delegated-child BUS actually works across a REAL container boundary — not
// the transport-only proof internal/lm/isolation's docker_integration suite
// already gives (Info() over a plugin-in-container gRPC dial), and not the
// in-process StartRun simulation coord's own hermetic suite gives
// (fake_test.go's fakeSpawner.StartEngine, startrun_test.go,
// livetap_test.go) — those all fake or in-process the runner half. Here the
// runner half is the REAL `ctxloom llm serve mock` binary running as PID 1
// inside a REAL docker container, dialing home over the coordinator's real
// container-reachable (bridge/host-interface) listener, exactly as a
// production claude/codex/kiro/acp container child would.
//
// Every assertion below reads a DELIVERED PAYLOAD (echoed text, mailbox
// body, report text, or fetched bytes) — never just a tool call's exit
// status — per this project's characteristic silent-no-op failure mode
// (exit 0, success message, zero bytes actually delivered).
//
// Run with:
//
//	just test-docker-integration
//	GOWORK=off just test-pkg ./internal/agentcoord/coord/... -tags docker_integration -run ContainerBus
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
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// busIntegrationImage is this file's own minimal image (a distinct tag from
// internal/lm/isolation's ctxloom-iso-itest — different package, own
// namespace) — alpine + the freshly built static ctxloom binary, exactly
// mirroring isolation's buildIntegrationImage so the tree-under-test is
// always what this run actually built.
const busIntegrationImage = "ctxloom-coord-bus-itest:latest"

// containerBusAgentName is the one agent dockerBusSpawner resolves.
const containerBusAgentName = "bus-container-worker"

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
	// latent bug in internal/cli/mcp_runner.go's resolveCellPath (root=="/"
	// makes the "absRoot+separator" containment check compare against "//",
	// which no real absolute path ever has a prefix of, so EVERY relative
	// publish_paths/dest_path is rejected as "escapes the working
	// directory" — see this file's TestCoordContainerBus_RoundTrip doc and
	// the worker's final report for the reproduction). Real ctxloom agent
	// images always set WORKDIR to the bind-mounted project path (never
	// bare "/"), so this never fires in production; it is a real bug this
	// test's image sidesteps rather than a bus defect, and is reported, not
	// silently patched around, per this task's do-not-refactor-the-bus
	// scope.
	dockerfile := "FROM alpine:latest\nWORKDIR /work\nCOPY ctxloom /usr/local/bin/ctxloom\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644))

	img := exec.Command("docker", "build", "-t", busIntegrationImage, dir)
	if out, err := img.CombinedOutput(); err != nil {
		t.Fatalf("docker build bus integration image: %v\n%s", err, out)
	}
	return busIntegrationImage
}

// dockerBusSpawner is the docker_integration test double for Spawner: Resolve
// hands out one fixed "container, mock backend, StartRun-migrated" plan
// (mirrors fakeSpawner/liveTapSpawner's shape — see fake_test.go and
// livetap_test.go), and StartEngine launches a REAL `docker run` of
// busIntegrationImage running `ctxloom llm serve mock` as PID 1, with the
// coordinator reach-back trio (runnerEnv) stamped as bare-name `-e` forms —
// exactly runnerEnv's own doc: "container: bare-name -e forms with the
// values on the run-process env". Everything downstream of that (dial-home,
// StartRun, the runner-local MCP surface) is 100% production code
// (internal/cli/llm_serve.go) — this spawner's only job is standing the
// container up; it does not simulate any runner behavior itself.
type dockerBusSpawner struct {
	image string

	mu         sync.Mutex
	containers []string
}

func (s *dockerBusSpawner) Resolve(_ context.Context, agentName string) (*SpawnPlan, error) {
	if agentName != containerBusAgentName {
		return nil, fmt.Errorf("dockerBusSpawner: unknown agent %q", agentName)
	}
	perm := agent.PermissionBypass
	return &SpawnPlan{
		AgentName:   agentName,
		Backend:     "mock",
		Label:       "fast",
		Runtime:     "container",
		Perm:        perm,
		Ladder:      presetLadder(perm),
		ViaStartRun: true,
	}, nil
}

// AssignSession mints a REAL session-index harp (operations.AssignSession —
// the same call prodSpawner.AssignSession makes) so
// operations.WatchSessionFeed's discovery can resolve it (mirrors
// liveTapSpawner.AssignSession's identical rationale, livetap_test.go).
func (s *dockerBusSpawner) AssignSession(projectDir, backend string) (string, error) {
	entry, err := operations.AssignSession(projectDir, backend)
	if err != nil {
		return "", err
	}
	return entry.HarpName, nil
}

func (s *dockerBusSpawner) Launch(context.Context, *SpawnPlan, string, map[string]string, map[string]string) (*operations.AgentChatLaunch, error) {
	return nil, fmt.Errorf("dockerBusSpawner: legacy Launch is unused (this agent always routes ViaStartRun)")
}

// StartEngine launches the REAL container. Kill (docker rm -f) becomes
// rt.close, so Coordinator.Close/terminateRun tears the container down
// exactly as production's isolation.Container policy's Kill would.
func (s *dockerBusSpawner) StartEngine(ctx context.Context, plan *SpawnPlan, env, runnerEnv map[string]string) (*EngineSpawn, error) {
	name := "ctxloom-coord-itest-bus-" + randID("", 6)
	args := []string{"run", "-d", "--rm", "--name", name}
	for k, v := range runnerEnv {
		args = append(args, "-e", k+"="+v)
	}
	// The go-plugin magic cookie: without it, plugin.Serve (llm_serve.go's
	// unconditional tail call) refuses to run at all ("this binary is a
	// plugin") — see internal/lm/grpc/shared.go's HandshakeConfig. Isolation's
	// container policy stamps this itself for a go-plugin-launched child;
	// we launch via bare `docker run`, so we stamp it ourselves.
	args = append(args, "-e", "CTXLOOM_PLUGIN=ai-backend-v1")
	args = append(args, s.image, "ctxloom", "llm", "serve", "mock")

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker run bus child %s: %w: %s", name, err, out)
	}
	s.mu.Lock()
	s.containers = append(s.containers, name)
	s.mu.Unlock()

	kill := sync.OnceFunc(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	return &EngineSpawn{WorkDir: "/", Env: env, Model: "mock", MCPServers: plan.MCPServers, Kill: kill}, nil
}

func (s *dockerBusSpawner) ResumeContext(_ context.Context, plan *SpawnPlan, _ string) string { return plan.Context }
func (s *dockerBusSpawner) MarkSessionEnded(string)                                            {}

func (s *dockerBusSpawner) containerNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.containers...)
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

// discoverChildSocket polls (via docker exec) for the runner's local MCP
// unix socket to appear inside the container (mcp_runner.go's
// runnerSocketPath — created only AFTER the runner's dial-home succeeds), so
// the test never assumes a fixed PID.
func discoverChildSocket(t *testing.T, containerName string) string {
	t.Helper()
	var path string
	require.Eventually(t, func() bool {
		out, err := exec.Command("docker", "exec", containerName, "sh", "-c", "ls /run/ctxloom/local/*.sock 2>/dev/null").Output()
		if err != nil {
			return false
		}
		p := strings.TrimSpace(string(out))
		if p == "" {
			return false
		}
		path = strings.SplitN(p, "\n", 2)[0]
		return true
	}, 30*time.Second, 250*time.Millisecond, "the runner's local MCP socket must appear inside the container")
	return path
}

// connectChildMCP dials the container's runner-local MCP surface via `docker
// exec -i ctxloom mcp` — the IDENTICAL command a real harness's stdio shim
// runs (mcp_forward.go's forward mode), just invoked from the host over
// docker exec instead of being the harness's own child process. This is
// REAL production code on both ends: no test-only binary.
func connectChildMCP(ctx context.Context, t *testing.T, containerName, socketPath string) *mcp.ClientSession {
	t.Helper()
	var cs *mcp.ClientSession
	require.Eventually(t, func() bool {
		cmd := exec.CommandContext(ctx, "docker", "exec", "-i",
			"-e", "CTXLOOM_MCP_SOCKET="+socketPath,
			containerName, "ctxloom", "mcp")
		transport := &mcp.CommandTransport{Command: cmd}
		client := mcp.NewClient(&mcp.Implementation{Name: "itest-child", Version: "0"}, nil)
		s, err := client.Connect(ctx, transport, nil)
		if err != nil {
			return false
		}
		cs = s
		return true
	}, 30*time.Second, 500*time.Millisecond, "docker exec ctxloom mcp (forward mode) must connect to the runner's local socket")
	return cs
}

// writeContainerFile / readContainerFile drive the container's filesystem
// directly (docker exec) so the artifact scenario's source/dest bytes are
// observed on the SAME side of the boundary the runner itself reads/writes.
func writeContainerFile(t *testing.T, containerName, path, content string) {
	t.Helper()
	cmd := exec.Command("docker", "exec", "-i", containerName, "sh", "-c", "cat > "+path)
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "write %s inside container %s: %s", path, containerName, out)
}

func readContainerFile(t *testing.T, containerName, path string) string {
	t.Helper()
	out, err := exec.Command("docker", "exec", containerName, "cat", path).CombinedOutput()
	require.NoError(t, err, "read %s inside container %s: %s", path, containerName, out)
	return string(out)
}

// decodeStructured re-marshals a CallToolResult's StructuredContent into out
// (mirrors internal/cli's identically-named test helper, re-derived here
// since that one is unexported to a different package).
func decodeStructured(res *mcp.CallToolResult, out any) error {
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// TestCoordContainerBus_RoundTrip is crazy-yarn's headline proof: agent_run
// spawns a REAL containerized child (runtime: container) over the
// agentcoord path, and every leg of the coordinator<->child bus round-trips
// an actual payload across the real container boundary:
//
//  1. the container is genuinely running (docker ps), not a stub;
//  2. the runner inside it dials home and completes StartRun over the
//     coordinator's real container-reachable listener (roster reflects it);
//  3. coordinator -> child: the composed prompt AND a later agent_send both
//     cross into the container and the mock engine's echo of each crosses
//     back out, observed on the coordinator's own live tap
//     (operations.WatchSessionFeed) — not just an exit code;
//  4. child -> coordinator: a REAL `agent_send(to: parent)` and a REAL
//     `agent_report`, both issued from INSIDE the container over its own
//     runner-local MCP surface (docker exec'd `ctxloom mcp`, production
//     forward-mode code), land in the parent's mailbox (agent_recv) and the
//     roster's report projection (LatestReport), byte for byte;
//  5. an artifact (arbitrary bytes) published from inside the container is
//     fetched back to a DIFFERENT path inside the SAME container, round-
//     tripping through ArtifactTransferService across the boundary twice;
//  6. closing the coordinator force-removes the child's container.
func TestCoordContainerBus_RoundTrip(t *testing.T) {
	if !(isolation.Docker{}).Available() {
		t.Skip("docker unavailable; skipping the coordinator<->containerized-child bus integration test")
	}
	resetStrictness(t)

	// Build BEFORE isolating cwd (testsupport.ProjectDir chdirs the whole
	// process into a temp dir outside the module, which would break `go
	// build`'s go.mod discovery for buildBusIntegrationImage below).
	image := buildBusIntegrationImage(t)
	projectDir := testsupport.ProjectDir(t) // isolated HOME + cwd; never touches the real ~/.ctxloom

	sp := &dockerBusSpawner{image: image}
	c, err := New(Options{ProjectDir: projectDir, ProjectKey: "bus-itest", Spawner: sp})
	require.NoError(t, err)
	// Serve must write endpoint.json where discover.List() looks — the SAME
	// discovery operations.WatchSessionFeed's live path depends on
	// (livetap_test.go's identical rationale).
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close) // safety net if the test fails before the explicit Close below

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, name := range sp.containerNames() {
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Logf("container %s logs:\n%s", name, logs)
		}
	})

	owner := ownerIdentity()
	seedPayload := "SEED-" + randID("", 6)
	out, err := c.AgentRun(ctx, owner, containerBusAgentName, seedPayload, "")
	require.NoError(t, err)
	require.NotEmpty(t, out.Harp)
	require.Equal(t, "container", out.Runtime, "AgentRun must report the container runtime axis it actually resolved")
	childHarp := out.Harp

	// Subscribe to the live tap IMMEDIATELY — before the container has even
	// been `docker run` (StartEngine runs asynchronously off AgentRun's own
	// goroutine; the container's actual boot + dial-home + StartRun takes
	// real seconds). watchHub delivers a roster SNAPSHOT plus LIVE deltas
	// only — no replay of item text (consumer_test.go's
	// TestConsumerService_WatchRuns_SnapshotThenLiveDeltaText: "Spawn the
	// migrated child AFTER the watcher attached — proving genuinely LIVE
	// delivery, not a replay") — so subscribing late would silently miss the
	// seed echo below, not prove it absent. enqueueRun registers the harp in
	// the roster synchronously inside AgentRun, so discovery should resolve
	// within a beat; retried defensively against that propagation window.
	var feed *operations.SessionFeed
	require.Eventually(t, func() bool {
		f, ferr := operations.WatchSessionFeed(ctx, operations.SessionFeedRequest{Harp: childHarp})
		if ferr != nil || f.Source != "live" {
			return false
		}
		feed = f
		return true
	}, 10*time.Second, 50*time.Millisecond, "the live tap must resolve for the just-enqueued container child")
	tail := startFeedTail(feed)

	// --- Scenario 1: a REAL container, not a stub. ---
	require.Eventually(t, func() bool { return len(sp.containerNames()) > 0 }, 10*time.Second, 50*time.Millisecond)
	containerName := sp.containerNames()[0]
	require.Eventually(t, func() bool {
		psOut, _ := exec.Command("docker", "ps", "--filter", "name="+containerName, "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(psOut)) == containerName
	}, 30*time.Second, 250*time.Millisecond, "the child's container must actually be running")

	// --- Scenario 2/3: coordinator -> child, PAYLOAD-asserted via the live
	// tap — never just an exit code. The echo can only appear if the
	// containerized runner dialed home over the coordinator's REAL
	// container-reachable listener, completed StartRun, and ran a real turn
	// through EngineHost/Mock.Chat — so this single assertion proves the
	// WHOLE spawn-to-first-turn path, not just that a container exists.
	wantSeedEcho := "mock chat: " + seedPayload
	if snap, ok := waitForFeedText(tail, wantSeedEcho, 60*time.Second); !ok {
		t.Fatalf("the container's mock engine never echoed the seed prompt delivered across the container boundary; want substring %q, saw:\n%s", wantSeedEcho, snap)
	}

	require.Eventually(t, func() bool { return rosterState(c, childHarp) == StateIdle }, 30*time.Second, 50*time.Millisecond,
		"the child must return to idle before a follow-up agent_send")
	followUp := "FOLLOWUP-" + randID("", 6)
	_, err = c.AgentSend(owner, childHarp, "message", followUp, nil, "")
	require.NoError(t, err)
	// frameCoordinatorMessage (enginehost.go) frames a coordinator-delivered
	// turn as "[coordinator-delivered message from=<sender harp> kind=<kind>]"
	// — the sender is the OWNER's own harp (ownerIdentity()'s
	// "coordinator-harp"), not the literal "parent" (that's the address a
	// CHILD uses to reach its parent, not what a parent->child message is
	// attributed to).
	wantFollowUpEcho := "mock chat: [coordinator-delivered message from=coordinator-harp kind=message]\n" + followUp
	if snap, ok := waitForFeedText(tail, wantFollowUpEcho, 45*time.Second); !ok {
		t.Fatalf("a coordinator agent_send payload never reached the real container (echo missing); want substring %q, saw:\n%s", wantFollowUpEcho, snap)
	}

	// --- Scenario 4: child -> coordinator, via the child's OWN runner-local
	// MCP surface, docker-exec'd for real (production forward-mode code on
	// both ends — no test-only binary). ---
	socketPath := discoverChildSocket(t, containerName)
	cs := connectChildMCP(ctx, t, containerName, socketPath)
	t.Cleanup(func() { _ = cs.Close() })

	sendPayload := "CHILD-SEND-" + randID("", 6)
	sendReq := &agentcoordpb.PeerSendRequest{ToRole: "parent", Text: sendPayload}
	sendRaw, err := protojson.Marshal(sendReq)
	require.NoError(t, err)
	sendRes, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: mcpschema.ToolAgentSend, Arguments: json.RawMessage(sendRaw)})
	require.NoError(t, err)
	require.False(t, sendRes.IsError, "agent_send from inside the container failed: %+v", sendRes)

	msgs, err := c.AgentRecv(ctx, owner, 15*time.Second)
	require.NoError(t, err)
	require.Len(t, msgs, 1, "the parent's mailbox must carry exactly the one message the container sent")
	assert.Equal(t, sendPayload, msgs[0].Body, "the parent's mailbox body must match the child's agent_send payload byte for byte")
	assert.Equal(t, childHarp, msgs[0].From, "the mailbox message must be attributed to the containerized child's harp")

	reportPayload := "CHILD-REPORT-" + randID("", 6)
	reportReq := &agentcoordpb.Summary{Scope: agentcoordpb.Summary_SCOPE_FINAL, Text: reportPayload}
	reportRaw, err := protojson.Marshal(reportReq)
	require.NoError(t, err)
	reportRes, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: mcpschema.ToolAgentReport, Arguments: json.RawMessage(reportRaw)})
	require.NoError(t, err)
	require.False(t, reportRes.IsError, "agent_report from inside the container failed: %+v", reportRes)
	require.Eventually(t, func() bool {
		return strings.Contains(c.LatestReport(childHarp), reportPayload)
	}, 15*time.Second, 100*time.Millisecond, "the roster's LatestReport must reflect the child's agent_report payload")

	// --- Scenario 5: artifacts/structured payloads round-trip. ---
	artifactContent := "artifact-bytes-" + randID("", 8)
	writeContainerFile(t, containerName, "/work/artifact.txt", artifactContent)

	pubReq := &agentcoordpb.Summary{Scope: agentcoordpb.Summary_SCOPE_STEP, Text: "publishing an artifact", PublishPaths: []string{"artifact.txt"}}
	pubRaw, err := protojson.Marshal(pubReq)
	require.NoError(t, err)
	pubRes, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: mcpschema.ToolAgentReport, Arguments: json.RawMessage(pubRaw)})
	require.NoError(t, err)
	require.False(t, pubRes.IsError, "agent_report publish_paths failed: %+v", pubRes)
	var pubOut struct {
		ArtifactIDs []string `json:"artifact_ids"`
	}
	require.NoError(t, decodeStructured(pubRes, &pubOut))
	require.Len(t, pubOut.ArtifactIDs, 1)
	artifactID := pubOut.ArtifactIDs[0]
	require.Equal(t, "file/artifact.txt", artifactID)

	fetchReq := &agentcoordpb.FetchArtifactRequest{AgentId: childHarp, ArtifactId: artifactID, DestPath: "fetched.txt"}
	fetchRaw, err := protojson.Marshal(fetchReq)
	require.NoError(t, err)
	fetchRes, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: mcpschema.ToolAgentFetchArtifact, Arguments: json.RawMessage(fetchRaw)})
	require.NoError(t, err)
	require.False(t, fetchRes.IsError, "agent_fetch_artifact failed: %+v", fetchRes)

	got := readContainerFile(t, containerName, "/work/fetched.txt")
	assert.Equal(t, artifactContent, got,
		"the fetched artifact bytes must match what was published, byte for byte, after crossing the container boundary twice (upload then download)")

	// --- Scenario 6: closing the coordinator force-removes the container. ---
	c.Close()
	assert.Eventually(t, func() bool {
		psOut, _ := exec.Command("docker", "ps", "-a", "--filter", "name="+containerName, "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(psOut)) == ""
	}, 15*time.Second, 250*time.Millisecond, "closing the coordinator must force-remove the child's container")
}

// compile-time reminders that this file's helpers use the packages they
// import (pb.WatchEvent lives behind operations.SessionFeedEvent.Event; kept
// as an explicit reference so a refactor upstream that changes the feed
// event shape breaks this file loudly, at compile time, not silently).
var _ = func(ev operations.SessionFeedEvent) *pb.WatchEvent { return ev.Event }
