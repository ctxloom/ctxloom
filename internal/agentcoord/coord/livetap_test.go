package coord_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/cli/tui"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/termui"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// tuiGeo is a plain small overlay geometry, sized like model_test.go's
// testGeo() (unreachable from this external package).
func tuiGeo() termui.OverlayGeometry {
	return termui.OverlayGeometry{Cols: 100, Rows: 30, PanelRows: 10}
}

// This file is the F1 hermetic live-tap end-to-end harness (Wave F playbook,
// item 4): a REAL in-process coord.Coordinator + a minimal Spawner double
// driving the MIGRATED (StartRun) engine path -> the coordinator's own
// watchHub (runchannel.go:277, fed by handleAgentEvent on a live RunChannel
// — recon note: the LEGACY spawner.Launch path children.go's driveChild
// documents is NOT live-observable; only StartRun-migrated children reach
// watchHub) -> ConsumerService.WatchRuns -> operations.WatchSessionFeed's
// live path (sessionfeed.go:95/:169) -> the real tui.Overlay (the same
// Run entry point overlay_test.go drives, and run_terminal_ui.go:77 wires
// in production) -> the child's assistant entry rendered as a real overlay
// item.
//
// Placement: package coord_test (external). coord/fake_test.go's
// fakeSpawner/fakeEngine/scriptedChat already prove this exact StartEngine
// bridging (NewEngineHost/NewHome/HomeConfig), but they are unexported and
// package-private to coord's own tests, so this file re-derives the same
// idiom using only coord's EXPORTED Spawner surface (Resolve/AssignSession/
// StartEngine/...) — the same seam fakeSpawner.StartEngine itself is built
// from. Because internal/cli/tui already imports internal/agentcoord/coord
// in production (roster.go), an INTERNAL coord test importing tui would
// cycle; an EXTERNAL coord_test package importing tui does not (tui is a
// consumer of coord, not the reverse), so package coord_test is required
// here, not merely a style choice.
//
// Gap markers (feed.go:27 — itemsFromFeedEvent's Gap>0 branch): NOT
// exercised end-to-end in THIS test (it drives one clean turn end-to-end;
// no drop is ever provoked here) — but the note this comment used to carry
// ("Gap is never set anywhere on the live path any more... the field is a
// vestige of the retired agentbus tap") is now obsolete: watchHub's
// broadcast (consumer.go) is a real, live producer of drops again (a full
// subscriber ring), operations.adaptConsumerFeed (sessionfeed.go) now
// detects the resulting seq discontinuity and emits Gap for it, and
// livetap_gap_test.go in this same package proves the rendering side
// end-to-end: a real seq gap on the wire reaching a real notice in the real
// tui.Overlay. model_test.go's TestModel_GapRendersNotice remains the
// synthetic unit-level check one level further down (Gap>0 -> notice item).

// liveTapChat is a minimal agent.StructuredChat: one assistant entry plus a
// turn boundary per received turn. It stands in for coord/enginehost_test.go's
// unexported scriptedChat (package-private, unreachable from here) using
// only the exported agent.StructuredChat contract that type also implements.
//
// turnGate (non-nil) holds the turn's content until released: a fast
// in-process StartRun round trip can otherwise complete and broadcast to
// watchHub BEFORE the test's own ConsumerService.WatchRuns subscription
// attaches (watchHub is forward-only fan-out — a broadcast with no
// subscriber yet is simply gone, and this test's synthetic child writes no
// transcript for the store-fallback scrollback to replay) — the same
// ordering hazard TestConsumerService_WatchRuns_SnapshotThenLiveDeltaText's
// own doc comment names ("Spawn the migrated child AFTER the watcher
// attached — proving genuinely LIVE delivery, not a replay"); that test can
// subscribe before spawning because it dials the bare ConsumerService
// directly, but operations.WatchSessionFeed's higher-level discovery can
// only resolve a harp that ListRuns already reports live, so the ordering
// has to be enforced by gating the content instead.
type liveTapChat struct {
	turnGate chan struct{}
}

func (c *liveTapChat) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(out)
	send := func(ev agent.ChatEvent) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !send(agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: req.Model, SessionID: "live-tap-sess"}}) {
		return ctx.Err()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-in:
			if !ok {
				return nil
			}
			if msg.Text == "" {
				continue
			}
			if c.turnGate != nil {
				select {
				case <-c.turnGate:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if !send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "live words for " + msg.Text}}) {
				return ctx.Err()
			}
			if !send(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}) {
				return ctx.Err()
			}
		}
	}
}

// liveTapSpawner is a minimal coord.Spawner routing its one "worker" agent
// over the MIGRATED StartRun path (ViaStartRun) — the ONLY path that feeds
// the D1 watchHub (see this file's header comment). AssignSession mints a
// REAL session-index harp (sessions.AssignHarp, the same call
// operations_test.go's seedFeedHarp uses) so operations.GetSession /
// WatchSessionFeed can resolve it: this test is about the DISCOVERY +
// ADAPTATION chain, not the spawn mechanics coord's own suite already
// proves (consumer_test.go's TestConsumerService_WatchRuns_...).
type liveTapSpawner struct {
	projectDir string
	chat       *liveTapChat // the SAME instance every StartEngine call binds, so the test's gate reaches it
}

func (s *liveTapSpawner) Resolve(context.Context, string) (*coord.SpawnPlan, error) {
	return &coord.SpawnPlan{
		AgentName:   "worker",
		Backend:     "claude-code",
		Label:       "fast",
		Perm:        agent.PermissionBypass,
		ViaStartRun: true,
	}, nil
}

func (s *liveTapSpawner) AssignSession(projectDir, backend string) (string, error) {
	mgr, err := sessions.Open("")
	if err != nil {
		return "", err
	}
	entry, err := mgr.AssignHarp(projectDir, backend)
	if err != nil {
		return "", err
	}
	return entry.HarpName, nil
}

func (s *liveTapSpawner) Launch(context.Context, *coord.SpawnPlan, string, string, map[string]string, map[string]string) (*operations.AgentChatLaunch, error) {
	return nil, fmt.Errorf("liveTapSpawner: Launch is unused (this agent always routes ViaStartRun)")
}

// StartEngine bridges the coordinator's own RunChannel to liveTapChat,
// mirroring fake_test.go's fakeSpawner.StartEngine (coord/fake_test.go:229)
// via the SAME exported constructors it uses internally.
func (s *liveTapSpawner) StartEngine(ctx context.Context, plan *coord.SpawnPlan, env, runnerEnv map[string]string) (*coord.EngineSpawn, error) {
	sctx, cancel := context.WithCancel(ctx)
	host := coord.NewEngineHost(sctx, s.chat, plan.Backend, runnerEnv[coord.EnvRunID])
	home, err := coord.NewHome(sctx, coord.HomeConfig{
		URL:     runnerEnv[coord.EnvCoordURL],
		Token:   runnerEnv[coord.EnvCoordCred],
		RunID:   runnerEnv[coord.EnvRunID],
		Harness: plan.Backend,
		Version: "test",
		Engine:  host.Handle,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	host.BindHome(home)
	return &coord.EngineSpawn{WorkDir: "/work", Env: env, Model: "test-model", Kill: cancel}, nil
}

func (s *liveTapSpawner) ResumeContext(context.Context, *coord.SpawnPlan, string) string { return "" }
func (s *liveTapSpawner) MarkSessionEnded(string)                                        {}

// syncBuf is a goroutine-safe io.Writer (the overlay's tty writer runs on
// its own goroutine, same as overlay_test.go's syncBuffer — re-derived here
// since that type is unexported to package tui).
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitForLiveTap(t *testing.T, what string, cond func() bool) {
	t.Helper()
	// 60s, not conformanceWait's 5s: this path is a REAL dial-home + StartRun
	// round trip over a real loopback listener — this test's own bound, not
	// children.go's defaultRunnerAwaitTimeout (which this coordinator gets
	// unmodified and is now wider still) — and it ran visibly slower under a
	// full `-race ./...` package run than in isolation — a scheduling-load
	// sensitivity, not a logic race (repeated isolated -count=1 -race runs of
	// this test were instant and stable).
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestLiveTap_ChildItemsReachTheOverlay is the hermetic end-to-end proof:
// real Coordinator + a StartRun-migrated child's real event stream -> real
// watchHub -> real ConsumerService.WatchRuns -> operations.WatchSessionFeed
// resolving the live tap via ~/.ctxloom/coord/*/endpoint.json discovery
// (the coordinator's OWN Serve() writes this; no fakeConsumerServer
// stand-in anywhere in this test) -> the real tui.Overlay rendering the
// child's assistant entry as a feed item.
func TestLiveTap_ChildItemsReachTheOverlay(t *testing.T) {
	home := testsupport.Isolate(t)
	projectDir := home + "/proj"
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	gate := make(chan struct{})
	chat := &liveTapChat{turnGate: gate}
	sp := &liveTapSpawner{projectDir: projectDir, chat: chat}
	c, err := coord.New(coord.Options{ProjectDir: projectDir, ProjectKey: "livetap-proj", Spawner: sp})
	require.NoError(t, err)
	require.NoError(t, c.Serve(), "Serve must write endpoint.json where discover.List() looks")
	t.Cleanup(c.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	owner := coord.Identity{Harp: "coordinator-harp", Depth: 0}
	out, err := c.AgentRun(ctx, owner, "worker", "hello from the coordinator", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, out.Harp)

	// The child must show up live (non-terminal) in ListRuns for
	// operations.WatchSessionFeed's discovery to resolve it — wait for the
	// StartRun handshake to complete rather than racing it.
	waitForLiveTap(t, "the child to appear live on the coordinator's own roster", func() bool {
		for _, e := range c.Roster() {
			if e.Harp == out.Harp {
				return true
			}
		}
		return false
	})

	// Resolve the live feed BEFORE the gated turn ever runs: this is the
	// exact seam operations-backed frontends use (terminalUISources /
	// `ctxloom session transcript watch`, run_terminal_ui.go:105-113) — discovery and
	// all — called directly (not lazily from the overlay's Watch closure) so
	// the test can confirm the ConsumerService.WatchRuns subscription is
	// live BEFORE releasing the gate below.
	feed, err := operations.WatchSessionFeed(ctx, operations.SessionFeedRequest{Harp: out.Harp})
	require.NoError(t, err)
	require.Equal(t, "live", feed.Source, "the coordinator holds this child; the tap must be live, not the store fallback")

	close(gate) // now safe: the subscription is attached, so the broadcast can't be lost

	src := tui.Sources{
		Roster: func(context.Context) ([]tui.RosterRow, error) {
			return []tui.RosterRow{{Harp: out.Harp, State: "live"}}, nil
		},
		Watch: func(context.Context, string) (*tui.Feed, error) {
			return &tui.Feed{Source: feed.Source, Events: feed.Events, Errs: feed.Errs, Cancel: func() {}}, nil
		},
		Now: time.Now,
	}

	ov := tui.NewOverlay(ctx, src, 0x1d)
	pr, pw := io.Pipe()
	defer pw.Close()
	var tty syncBuf
	done := make(chan error, 1)
	go func() { done <- ov.Run(pr, &tty, tuiGeo()) }()

	waitForLiveTap(t, "the feed to resolve live (not the store fallback)", func() bool {
		return strings.Contains(tty.String(), "· live")
	})
	waitForLiveTap(t, fmt.Sprintf("the child's real assistant entry to render as an overlay item (roster: %v)", c.Roster()), func() bool {
		return strings.Contains(tty.String(), "asst  < live words for hello from the coordinator")
	})

	_, err = pw.Write([]byte("q"))
	require.NoError(t, err)
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(3 * time.Second):
		t.Fatal("overlay did not quit")
	}
}
