//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/adapters/runner/coordtest"
	runnermcp "github.com/ctxloom/ctxloom/internal/adapters/runner/mcp"
	"github.com/ctxloom/ctxloom/internal/adapters/spawn"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// THE PERMISSIONCONTRACT LANE: the approval route end to end, on a REAL
// coordinator and its ApprovalQueue, with a mock child turn.
//
// The owner delegates a child whose approver is the human. The production
// spawner launches it on an in-process runner (coordtest's double: only the
// process boundary is faked) that DELIVERS the launch — so the engine's
// approval hook lands where the engine reads it — and SERVES the session
// endpoint, /hook included. The child makes a call its rules leave open; its
// hook runs the BUILT ctxloom binary in exec form (`ctxloom hook permission`),
// which posts to /hook under the bearer; the runner matches the ask against
// the turn's ledger and parks it over the run's gRPC link in the coordinator's
// queue, where this test answers it as the human would. What the engine did
// with the decision comes back as the child's automatic turn report.
//
// claude's half of the same contract — that claude -p awaits and honours the
// hook — is the @live P12 rung.
//
// Every wait below is on a synchronised channel (the queue's subscription,
// the owner's mailbox, the endpoint's recorded loadout); none polls.

const (
	askerAgent   = "asker"
	contractTool = "Bash"
	contractCall = `{"command":"ls"}`
	ownerHarp    = "coordinator-harp"
	laneWait     = 60 * time.Second
)

// contractLane is one coordinator with a human-approved mock child to delegate.
type contractLane struct {
	c      *coord.Coordinator
	owner  coord.Identity
	queue  <-chan coord.QueueEvent
	served chan delivery.Loadout
}

func newContractLane(t *testing.T) *contractLane {
	t.Helper()
	ctxloomOnPath(t)
	t.Setenv("HOME", t.TempDir())
	cfg, root := askerFixture(t)

	runners := coordtest.NewRunners(engines.Registry())
	records, err := fsstatic.NewRecords(afero.NewOsFs(), filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	served := make(chan delivery.Loadout, 4)
	runners.Static, runners.Records = fsstatic.New(afero.NewOsFs()), records
	runners.Endpoint = func(h *runner.Home) delivery.Dynamic {
		return recordedEndpoint{Endpoint: runnermcp.Endpoint{Home: h}, served: served}
	}
	t.Cleanup(runners.Close)

	c, err := coord.New(coord.Options{
		Spawner: spawn.New(nil, laneApp(t, cfg), root, runners.Starter), ProjectDir: root, StateDir: t.TempDir(),
		OwnerHarp: ownerHarp,
	})
	require.NoError(t, err)
	require.NoError(t, coordgrpc.Serve(c))
	t.Cleanup(c.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &contractLane{c: c, owner: coord.Identity{Harp: ownerHarp, Depth: 0}, queue: c.Approvals().Subscribe(ctx), served: served}
}

// delegate starts the child on a turn that asks about contractTool.
func (l *contractLane) delegate(t *testing.T) *coord.RunOutcome {
	t.Helper()
	out, err := l.c.AgentRun(context.Background(), l.owner, askerAgent, mock.Ask(contractTool, contractCall), "", "")
	require.NoError(t, err)
	return out
}

// nextQueueEvent is the queue's next change.
func (l *contractLane) nextQueueEvent(t *testing.T) coord.QueueEvent {
	t.Helper()
	select {
	case ev := <-l.queue:
		return ev
	case <-time.After(laneWait):
		t.Fatal("the approval queue never changed")
		return coord.QueueEvent{}
	}
}

// parked is the one request the child's ask parked.
func (l *contractLane) parked(t *testing.T) coord.PendingApproval {
	t.Helper()
	ev := l.nextQueueEvent(t)
	require.Equal(t, coord.QueueAdded, ev.Kind, "the child's ask parks in the queue")
	pending := l.c.Approvals().Pending()
	require.Len(t, pending, 1)
	require.Equal(t, ev.ID, pending[0].ID)
	return pending[0]
}

// turnReport is the child's automatic turn report, as the owner receives it.
func (l *contractLane) turnReport(t *testing.T, childHarp string) coord.Message {
	t.Helper()
	deadline := time.Now().Add(laneWait)
	for time.Now().Before(deadline) {
		msgs, err := l.c.AgentRecv(context.Background(), l.owner, time.Until(deadline))
		require.NoError(t, err)
		for _, m := range msgs {
			if m.From == childHarp && coord.IsAutoReport(m.Structured) {
				return m
			}
		}
	}
	t.Fatal("the child's turn never reported")
	return coord.Message{}
}

// blocked is the report's refused calls.
func blocked(t *testing.T, m coord.Message) []coord.BlockedCall {
	t.Helper()
	var s struct {
		Blocked []coord.BlockedCall `json:"blocked"`
	}
	require.NoError(t, json.Unmarshal(m.Structured, &s))
	return s.Blocked
}

// TestPermissionContract_AnAskReachesTheQueueAndItsDecisionReturns: the
// child's ask parks ONE request in the root's queue, naming the call its own
// stream announced, and the human's decision is what the engine applies — an
// allow runs the call, a deny refuses it with the human's message.
func TestPermissionContract_AnAskReachesTheQueueAndItsDecisionReturns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  coord.ApprovalDecision
		refused bool
	}{
		{"allow", coord.ApprovalDecision{Allow: true, Decider: agent.DeciderHuman}, false},
		{"deny", coord.ApprovalDecision{Message: "not today", Decider: agent.DeciderHuman}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lane := newContractLane(t)
			child := lane.delegate(t)
			p := lane.parked(t)
			assert.Equal(t, contractTool, p.Ask.Tool)
			assert.JSONEq(t, contractCall, string(p.Ask.Input))
			assert.Equal(t, mock.AskCallID, p.Ask.ToolUseID, "the ledger names the call the ask matched")
			assert.NotEmpty(t, p.Transitions, "the engine's posture transitions ride the request")

			require.NoError(t, lane.c.Approvals().Answer(p.ID, tc.answer))
			calls := blocked(t, lane.turnReport(t, child.Harp))
			if !tc.refused {
				assert.Empty(t, calls, "allowed: the call ran")
				return
			}
			require.Len(t, calls, 1)
			assert.Equal(t, contractTool, calls[0].Tool)
			assert.Equal(t, "not today", calls[0].Reason)
		})
	}
}

// TestPermissionContract_AForgedAskNeverReachesTheQueue: a POST to the
// child's /hook under its own bearer, naming a call the engine never
// announced — a model's own curl, say — is refused by the ledger and never
// parked. The child's genuine ask, made first, is the control: the queue does
// receive asks, so its silence afterwards is the refusal and not a dead route.
func TestPermissionContract_AForgedAskNeverReachesTheQueue(t *testing.T) {
	lane := newContractLane(t)
	lane.delegate(t)
	genuine := lane.parked(t)

	var lo delivery.Loadout
	select {
	case lo = <-lane.served:
	case <-time.After(laneWait):
		t.Fatal("the child's endpoint was never served")
	}
	ep, err := url.Parse(lo.MCP.URL)
	require.NoError(t, err)
	ep.Path, ep.RawQuery = runner.HookPath, url.Values{runner.HookEventParam: {wire.HookEventPermissionAsk}}.Encode()
	req, err := http.NewRequest(http.MethodPost, ep.String(), bytes.NewReader([]byte(`{"tool":"Bash","input":{"command":"rm -rf /"}}`)))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+lo.MCP.Credential)
	// Bounded: a ledger that let the forged ask join the genuine call's
	// decision would hold this POST until that decision, which this test
	// gives only after the answer arrives.
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode, "%s", body)
	assert.JSONEq(t, `{"allow":false,"message":"ctxloom: the permission request matches no open tool call of this turn"}`, string(body))

	// The forged POST has had its answer; release the genuine ask, and the
	// next change the queue reports is THAT one resolving — nothing parked
	// in between.
	require.NoError(t, lane.c.Approvals().Answer(genuine.ID, coord.ApprovalDecision{Allow: true, Decider: agent.DeciderHuman}))
	ev := lane.nextQueueEvent(t)
	assert.Equal(t, coord.QueueResolved, ev.Kind, "the forged ask parked nothing: %+v", ev)
	assert.Equal(t, genuine.ID, ev.ID)
	assert.Zero(t, ev.Pending)
}

// TestPermissionContract_TurnEndReleasesHeldAsks: the child's turn is cut
// short while its ask is parked; the ask leaves the queue with the turn (it
// cannot take an answer any more), and an answer offered afterwards finds
// nothing to answer.
func TestPermissionContract_TurnEndReleasesHeldAsks(t *testing.T) {
	lane := newContractLane(t)
	child := lane.delegate(t)
	p := lane.parked(t)

	_, err := lane.c.ControlSteer(context.Background(), coord.ControlInitiator{Kind: coord.InitiatorHuman}, child.Harp, "stop", true)
	require.NoError(t, err)
	ev := lane.nextQueueEvent(t)
	require.Equal(t, coord.QueueResolved, ev.Kind, "the turn's end released its ask")
	assert.Equal(t, p.ID, ev.ID)
	assert.Zero(t, ev.Pending)
	assert.Empty(t, lane.c.Approvals().Pending())
	assert.ErrorIs(t, lane.c.Approvals().Answer(p.ID, coord.ApprovalDecision{Allow: true, Decider: agent.DeciderHuman}), coord.ErrApprovalResolved)
}

// recordedEndpoint serves the real session endpoint and hands the test the
// loadout it served — the child's endpoint address and bearer.
type recordedEndpoint struct {
	runnermcp.Endpoint
	served chan<- delivery.Loadout
}

func (e recordedEndpoint) Serve(ctx context.Context, lo delivery.Loadout, policy delivery.ServePolicy) (delivery.Served, error) {
	out, err := e.Endpoint.Serve(ctx, lo, policy)
	if err == nil {
		e.served <- lo
	}
	return out, err
}

// ctxloomOnPath puts the built ctxloom first on PATH: the approval hook names
// the bare executable (agent.CtxloomCommand), resolved where it fires.
func ctxloomOnPath(t *testing.T) {
	t.Helper()
	env, err := testenv.NewTestEnvironment()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, env.Cleanup()) })
	require.Equal(t, agent.CtxloomCommand(), filepath.Base(env.AppBinary), "the hook resolves the binary by this name")
	t.Setenv("PATH", filepath.Dir(env.AppBinary)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// askerFixture is a project whose one agent, "asker", is a mock child whose
// approver is the human; its config is on disk as well as in memory, as the
// spawner reads it.
func askerFixture(t *testing.T) (*config.Config, string) {
	t.Helper()
	root := t.TempDir()
	app := filepath.Join(root, ".ctxloom")
	subs := map[string]agents.Agent{askerAgent: {LLM: "fast", Permissions: agents.Permissions{
		NeutralPermissions: agents.NeutralPermissions{Approver: "human", ApprovalTimeout: "5m"},
		Engines:            map[string]map[string]any{string(mock.Name): {"mode": "default"}},
	}}}
	doc := map[string]any{
		"version": config.CurrentConfigVersion,
		"llm": map[string]any{
			"configs":  map[string]any{"fast": map[string]any{"type": string(mock.Name), "model": "m-fast"}},
			"defaults": map[string]any{"primary": "fast"},
		},
		"agents": subs,
	}
	raw, err := yaml.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(app, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(app, "config.yaml"), raw, 0o644))
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{app},
		LM: config.LMConfig{
			Configs:  map[string]config.LLMConfig{"fast": {Type: string(mock.Name), Body: map[string]any{"model": "m-fast"}}},
			Defaults: config.RoleDefaults{Primary: "fast"},
		},
		Agents: subs,
	})
	return cfg, root
}

// laneSources is a config.Sources whose every Read is the fixture.
type laneSources struct{ cfg *config.Config }

func (s laneSources) Read(context.Context) (*config.Config, []config.Warning, error) {
	return s.cfg, nil, nil
}

func (s laneSources) Readers(_ context.Context, cfg *config.Config) ([]bundles.Reader, error) {
	return []bundles.Reader{bundles.NewProjectReader(cfg.FS(), cfg.BundleReaderDirs(), bundles.WithTrustRoot(cfg.Trust().Root()))}, nil
}

func (s laneSources) TrustPorts(context.Context, *config.Config) (composite.TrustRoot, composite.ReviewRecords, composite.RetractionRecords, error) {
	root, records, retraction := compositetest.Ports()
	return root, records, retraction, nil
}

// laneApp opens the process composition over the fixture.
func laneApp(t *testing.T, cfg *config.Config) *operations.App {
	t.Helper()
	owner, err := config.Open(context.Background(), laneSources{cfg: cfg})
	require.NoError(t, err)
	return operations.OpenedApp(owner, operations.Handed{Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims})
}
