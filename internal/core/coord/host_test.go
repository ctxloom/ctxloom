package coord

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// recordingHostApp is the HostApp double: it records every call and answers
// with the body its fn returns.
type recordingHostApp struct {
	mu    sync.Mutex
	calls []hostCall
	fn    func(ctx context.Context, caller Identity, req HostRequest) (HostResult, error)
}

type hostCall struct {
	caller Identity
	req    HostRequest
}

func (a *recordingHostApp) Serve(ctx context.Context, caller Identity, req HostRequest) (HostResult, error) {
	a.mu.Lock()
	a.calls = append(a.calls, hostCall{caller: caller, req: req})
	a.mu.Unlock()
	if a.fn == nil {
		return HostResult{Body: json.RawMessage(`{"ok":true}`)}, nil
	}
	return a.fn(ctx, caller, req)
}

func (a *recordingHostApp) last(t *testing.T) hostCall {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	require.NotEmpty(t, a.calls, "the host app was never asked")
	return a.calls[len(a.calls)-1]
}

// newTestCoordinatorWithHost is newTestCoordinator composed with a HostApp.
func newTestCoordinatorWithHost(t *testing.T, sp Spawner, host HostApp) *Coordinator {
	t.Helper()
	teeHome(t)
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Spawner:    sp,
		Host:       host,
		OwnerHarp:  ownerIdentity().Harp,
	})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)
	return c
}

// TestHost_DispatchesToTheComposedHostAppUnderTheCallersIdentity: the Host
// verb is the ONE arm for every host-relayed tool — it hands the request to
// the application service the coordinator was composed with, under the
// CALLER's identity (the child's harp, the caller's project), and returns
// the body the service answered.
func TestHost_DispatchesToTheComposedHostAppUnderTheCallersIdentity(t *testing.T) {
	app := &recordingHostApp{fn: func(_ context.Context, caller Identity, req HostRequest) (HostResult, error) {
		return HostResult{Body: json.RawMessage(`{"tool":"` + req.Tool + `","project":"` + caller.Project + `"}`)}, nil
	}}
	c := newTestCoordinatorWithHost(t, newFakeSpawner(nil, nil), app)

	caller := Identity{Harp: "child-harp-1", RunID: "run-1", Depth: 1, Project: c.projectDir}
	res, err := c.Host(context.Background(), caller, HostRequest{Tool: "list_sessions", Args: json.RawMessage(`{"limit":1}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `{"tool":"list_sessions","project":"`+c.projectDir+`"}`, string(res.Body))

	got := app.last(t)
	assert.Equal(t, caller, got.caller, "the service answers under the caller's identity, never the host process's")
	assert.Equal(t, "list_sessions", got.req.Tool)
	assert.JSONEq(t, `{"limit":1}`, string(got.req.Args))
}

// TestHost_WithoutAHostApp_IsUnimplemented: a coordinator composed without
// an application service refuses every relayed tool, loudly.
func TestHost_WithoutAHostApp_IsUnimplemented(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	_, err := c.Host(context.Background(), ownerIdentity(), HostRequest{Tool: "list_sessions"})
	require.ErrorIs(t, err, ErrNoHostApp)
}

// TestHost_ARelayedFrameReachesTheHostAppUnderTheChildsIdentity: a child's
// plane-2 host frame — the typed HostRequest, not an open extension — is
// dispatched through the Host verb; the service sees the CHILD's identity
// (its harp, the coordinator's project) and the body rides back typed.
func TestHost_ARelayedFrameReachesTheHostAppUnderTheChildsIdentity(t *testing.T) {
	resetStrictness(t)
	app := &recordingHostApp{}
	c := newTestCoordinatorWithHost(t, researcherSpawner(), app)
	out := spawnResearcher(t, c)
	h := childHome(t, c, out.RunID)

	args, err := structpb.NewStruct(map[string]any{"limit": 2})
	require.NoError(t, err)
	resp, err := h.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_Host{Host: &agentcoordpb.HostRequest{Tool: "list_sessions", Args: args}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	assert.Equal(t, map[string]any{"ok": true}, resp.GetHost().GetBody().AsMap())

	got := app.last(t)
	assert.Equal(t, out.Harp, got.caller.Harp, "the caller is the child that relayed, by its credential")
	assert.Equal(t, c.projectDir, got.caller.Project, "a relayed tool answers under the caller's project")
	assert.Equal(t, "list_sessions", got.req.Tool)
	assert.JSONEq(t, `{"limit":2}`, string(got.req.Args))
}

// TestHost_AnOversizedAnswerIsRefusedUnderTheFrameCap: the relay's size
// discipline is the verb's: an answer past the frame cap is refused with the
// remedy rather than failed by the transport.
func TestHost_AnOversizedAnswerIsRefusedUnderTheFrameCap(t *testing.T) {
	app := &recordingHostApp{fn: func(context.Context, Identity, HostRequest) (HostResult, error) {
		big := make([]byte, relayCapBytes+1)
		for i := range big {
			big[i] = 'x'
		}
		return HostResult{Body: json.RawMessage(`{"blob":"` + string(big) + `"}`)}, nil
	}}
	c := newTestCoordinatorWithHost(t, newFakeSpawner(nil, nil), app)
	resp := serveWire(c, ownerIdentity(), &agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_Host{Host: &agentcoordpb.HostRequest{Tool: "list_sessions"}}})
	assert.EqualValues(t, codes.ResourceExhausted, resp.GetStatus().GetCode())
}
