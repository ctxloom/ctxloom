//go:build docker_integration

// divisive-guru fork 3 (mint fresh), proven against a REAL container child:
// every launch of a harp, a resume included, carries its own session
// endpoint, and the in-container runner's guard honours only that launch's
// bearer. A bearer that outlived its incarnation is refused 401.
//
// The resume is the idle reaper's: the one terminal that leaves a container
// child resumable, so the next mail starts a new incarnation through the
// resume arm (SpawnStart.Resumed). The clock is injected and the sweep is
// invoked, never awaited.
//
// The spawner mints the endpoint per ResolveLaunch, as launch.Resolve does
// in production; what these tests prove is everything downstream of that:
// the coordinator re-resolves on resume, carries the new launch to the new
// incarnation's runner in its container, and that runner serves the new
// bearer and refuses the old one.
//
//	GOWORK=off just test-pkg ./internal/core/coord -tags docker_integration -run CoordContainerResume
package coord_test

import (
	"context"
	"net"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/daemonfixture"
)

const resumeAgentName = "resume-container-worker"

// resumeSpawner is progressSpawner's real path with the endpoint minted per
// resolve: a free loopback port and a random bearer, recorded in order, and
// the container each incarnation started in, recorded in the same order.
type resumeSpawner struct {
	image      string
	projectDir string

	mu         sync.Mutex
	cells      map[string]preparedContainerCell
	minted     []sessions.Endpoint
	containers []string
	cleanups   []func()
}

func (s *resumeSpawner) Resolve(_ context.Context, agentName string) (*coord.SpawnPlan, error) {
	if agentName != resumeAgentName {
		return nil, &unknownAgentError{agentName}
	}
	return &coord.SpawnPlan{AgentName: agentName, Backend: "mock", Label: "fast", Runtime: containerAxes("docker").Runtime, Permission: "bypass"}, nil
}

func (s *resumeSpawner) AssignSession(projectDir, backend string) (string, error) {
	entry, err := operations.OpenedApp(nil, operations.Handed{Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims}).AssignSession(context.Background(), projectDir, backend, filepath.Join(projectDir, ".test-output"))
	if err != nil {
		return "", err
	}
	return entry.HarpName, nil
}

// mint is a fresh endpoint: a port free on this host (the container may
// share its network) and a bearer no other launch holds.
func mint() (sessions.Endpoint, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return sessions.Endpoint{}, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return sessions.Endpoint{URL: "http://127.0.0.1:" + strconv.Itoa(port) + "/mcp", Credential: coord.RandID("bearer-", 16)}, nil
}

func (s *resumeSpawner) ResolveLaunch(ctx context.Context, plan *coord.SpawnPlan, start coord.SpawnStart) (coord.Resolved, error) {
	env := sessions.HookEnv(start.Identity)
	cenv, err := preparedContainer(ctx, "docker", coord.ContainerStoryBackend(plan), s.image, s.projectDir, isolation.SessionState{Harp: start.Identity.Harp, ProjectID: start.Identity.Project})
	if err != nil {
		return coord.Resolved{}, err
	}
	ep, err := mint()
	if err != nil {
		return coord.Resolved{}, err
	}
	s.mu.Lock()
	if s.cells == nil {
		s.cells = map[string]preparedContainerCell{}
	}
	s.cells[start.Identity.Harp] = preparedContainerCell{env: cenv, backend: plan.Backend, label: plan.Label}
	s.minted = append(s.minted, ep)
	s.mu.Unlock()
	l := coord.OwnerLaunch(start.Identity.Harp, plan.Backend, plan.Label, "mock", cenv.Placement().Paths.Paths().ProjectRoot.Host, "bypass")
	l.Identity = start.Identity
	l.Prompt = start.Prompt
	l.Cell.Env = env
	l.Cell.Listen = cenv.Listen()
	l.Axes.Runtime = containerAxes("docker").Runtime
	l.MCP = ep
	plan.Launch = l
	return coord.Resolved{Launch: l}, nil
}

func (s *resumeSpawner) Start(ctx context.Context, l launch.Launch, reach sessions.Endpoint) (*coord.EngineSpawn, error) {
	s.mu.Lock()
	cell := s.cells[l.Identity.Harp]
	s.mu.Unlock()
	handle, err := cell.env.Start(ctx, isolation.RunnerRequest{Engine: cell.backend, Label: cell.label, Env: sessions.EncodeReach(reach, l.Identity.RunID)})
	if err != nil {
		_ = cell.env.Cleanup()
		return nil, err
	}
	kill := sync.OnceFunc(func() {
		handle.Kill()
		_ = cell.env.Cleanup()
	})
	s.mu.Lock()
	s.containers = append(s.containers, handle.Name)
	s.cleanups = append(s.cleanups, kill)
	s.mu.Unlock()
	return &coord.EngineSpawn{Kill: kill}, nil
}

func (s *resumeSpawner) ResumeHistory(context.Context, string) string        { return "" }
func (s *resumeSpawner) RecordEngineVersion(context.Context, string, string) {}
func (s *resumeSpawner) MarkSessionEnded(string)                             {}
func (s *resumeSpawner) BindNativeSession(string, string)                    {}
func (s *resumeSpawner) NativeSession(string) string                         { return "" }

// incarnation is the endpoint and container of the i-th launch.
func (s *resumeSpawner) incarnation(t *testing.T, i int) (sessions.Endpoint, string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Greater(t, len(s.minted), i, "launch %d was never resolved", i)
	require.Greater(t, len(s.containers), i, "launch %d never started a container", i)
	return s.minted[i], s.containers[i]
}

func (s *resumeSpawner) cleanup() {
	s.mu.Lock()
	fns := append([]func(){}, s.cleanups...)
	s.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

var httpStatus = regexp.MustCompile(`HTTP/1\.[01] (\d{3})`)

// probeStatus POSTs an MCP ping to url from INSIDE container under bearer,
// the way an orphaned relay or hook in that incarnation would, and returns
// the HTTP status the endpoint answered.
func probeStatus(t *testing.T, container, url, bearer string) int {
	t.Helper()
	var status int
	require.Eventually(t, func() bool {
		out, _ := exec.Command("docker", "exec", container, "wget", "-S", "-q", "-O", "/dev/null",
			"--header", "Authorization: Bearer "+bearer,
			"--header", "Content-Type: application/json",
			"--header", "Accept: application/json, text/event-stream",
			"--post-data", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, url).CombinedOutput()
		m := httpStatus.FindSubmatch(out)
		if m == nil {
			return false
		}
		status, _ = strconv.Atoi(string(m[1]))
		return true
	}, 20*time.Second, 250*time.Millisecond, "no HTTP answer from %s inside %s", url, container)
	return status
}

// awaitState waits for harp's roster state.
func awaitState(t *testing.T, c *coord.Coordinator, harp, state string, within time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool { return rosterState(c, harp) == state }, within, 100*time.Millisecond,
		"harp %s never reached %q (last %q)", harp, state, rosterState(c, harp))
}

// awaitResult waits for a result in the owner's mail that mentions want.
func awaitResult(t *testing.T, c *coord.Coordinator, want string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		msgs, err := coord.OwnerMail(t, c, 200*time.Millisecond)
		if err != nil {
			continue
		}
		for _, m := range msgs {
			if m.Kind == "result" && strings.Contains(m.Body, want) {
				return
			}
		}
	}
	t.Fatalf("no result mentioning %q within %s", want, within)
}

// reapedAndResumed stands a container child up, has it finish one turn and
// park, reaps it on the idle clock, and resumes it with mail. It returns the
// coordinator, the spawner, the harp and both incarnations' run ids.
func reapedAndResumed(t *testing.T) (*coord.Coordinator, *resumeSpawner, string, string, string) {
	t.Helper()
	daemonfixture.Require(t, "the container-resume integration test")
	coord.ResetStrictness(t)
	image := buildBusIntegrationImage(t)
	projectDir := testsupport.ProjectDir(t)

	sp := &resumeSpawner{image: image, projectDir: projectDir}
	t.Cleanup(sp.cleanup)

	var clockMu sync.Mutex
	now := time.Now()
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }

	coord.TeeHome(t)
	c, err := coord.New(coord.Options{
		ProjectDir:         projectDir,
		ProjectID:          "resume-itest",
		Spawner:            sp,
		RunnerAwaitTimeout: 90 * time.Second,
		OwnerHarp:          coord.OwnerIdentity().Harp,
		Clock:              clock,
		IdleTimeout:        time.Minute,
	})
	require.NoError(t, err)
	require.NoError(t, coordgrpc.Serve(c))
	t.Cleanup(c.Close)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		sp.mu.Lock()
		names := append([]string(nil), sp.containers...)
		sp.mu.Unlock()
		for _, name := range names {
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Logf("container %s logs:\n%s", name, logs)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := c.AgentRun(ctx, coord.OwnerIdentity(), resumeAgentName, "first-task", "", "")
	require.NoError(t, err)
	awaitResult(t, c, "first-task", 60*time.Second)
	awaitState(t, c, out.Harp, coord.StateIdle, 30*time.Second)

	first, firstContainer := sp.incarnation(t, 0)
	require.NotEqual(t, 401, probeStatus(t, firstContainer, first.URL, first.Credential), "the first incarnation serves its own bearer")
	require.Equal(t, 401, probeStatus(t, firstContainer, first.URL, "bearer-nobody-minted"), "and refuses any other")

	clockMu.Lock()
	now = now.Add(2 * time.Minute)
	clockMu.Unlock()
	c.ReapIdleRuns()
	awaitState(t, c, out.Harp, coord.StateEnded, 30*time.Second)
	require.Equal(t, coord.CauseIdleReaped, c.RunCause(out.RunID))

	_, err = c.AgentSend(coord.OwnerIdentity(), out.Harp, coord.KindMessage, "second-task", nil, "")
	require.NoError(t, err)
	awaitResult(t, c, "second-task", 60*time.Second)
	resumedRun := c.CurrentRunID(out.Harp)
	require.NotEqual(t, out.RunID, resumedRun, "the resume is a new incarnation")
	return c, sp, out.Harp, out.RunID, resumedRun
}

// TestCoordContainerResume_FreshEndpointAndThePreviousBearerIsRefused: the
// resumed container child is launched on a NEW endpoint (address and bearer),
// its runner serves that bearer, and the previous incarnation's bearer is
// refused 401 by it.
func TestCoordContainerResume_FreshEndpointAndThePreviousBearerIsRefused(t *testing.T) {
	_, sp, _, _, _ := reapedAndResumed(t)
	first, _ := sp.incarnation(t, 0)
	second, secondContainer := sp.incarnation(t, 1)
	require.NotEqual(t, first.Credential, second.Credential, "the resume minted a fresh bearer")
	require.NotEqual(t, first.URL, second.URL, "and a fresh address")
	require.NotEqual(t, 401, probeStatus(t, secondContainer, second.URL, second.Credential), "the resumed incarnation serves its own bearer")
	require.Equal(t, 401, probeStatus(t, secondContainer, second.URL, first.Credential), "the previous incarnation's bearer is refused")
}

// TestCoordContainerIdleReap_ResumedChildWorksOnAFreshMint: a container child
// the idle reaper ended is resumed by mail into a new container on a fresh
// mint and takes the turn (reapedAndResumed waits for its result), then parks
// again, live.
func TestCoordContainerIdleReap_ResumedChildWorksOnAFreshMint(t *testing.T) {
	c, sp, harp, firstRun, resumedRun := reapedAndResumed(t)
	_, firstContainer := sp.incarnation(t, 0)
	second, secondContainer := sp.incarnation(t, 1)
	require.NotEqual(t, firstContainer, secondContainer, "the resume runs in a new container")
	require.Equal(t, coord.CauseIdleReaped, c.RunCause(firstRun))
	awaitState(t, c, harp, coord.StateIdle, 30*time.Second)
	require.Equal(t, resumedRun, c.CurrentRunID(harp))
	require.NotEqual(t, 401, probeStatus(t, secondContainer, second.URL, second.Credential), "the resumed child's endpoint is served on the fresh mint")
	sp.mu.Lock()
	mints := len(sp.minted)
	sp.mu.Unlock()
	require.Equal(t, 2, mints, "one mint per incarnation: launch, then resume")
}
