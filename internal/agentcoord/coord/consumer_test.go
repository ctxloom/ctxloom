package coord

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// D1 hermetic coverage: ConsumerService against a LIVE coordinator endpoint
// (real gRPC listeners, real StartRun-migrated child) — the same style as
// runchannel_test.go's plane-2 conformance suite.

// dialConsumer opens a bare gRPC connection carrying token as the bearer
// credential — deliberately NOT going through Home (which only speaks the
// runner/run channels); ConsumerService is a third, independent client.
func dialConsumer(t *testing.T, coordURL, token string) (agentcoordpb.ConsumerServiceClient, *grpc.ClientConn) {
	t.Helper()
	target, err := grpcTarget(coordURL)
	require.NoError(t, err)
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearerCreds(token)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return agentcoordpb.NewConsumerServiceClient(conn), conn
}

// TestConsumerService_WatchRuns_SnapshotThenLiveDeltaText is the Recon #1
// fix, proven: a StartRun-migrated child's activity — invisible to the
// legacy agentbus TapHub (children.go's driveChild/hub.Tee is never called
// on the viaStartRun path) — arrives live on ConsumerService.WatchRuns,
// FULL payload including delta TEXT (not the journal's counts-only
// projection). The first frame is always the roster snapshot.
func TestConsumerService_WatchRuns_SnapshotThenLiveDeltaText(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)

	consumerToken := c.consumerCreds.token()
	require.NotEmpty(t, consumerToken, "Serve() must mint the consumer credential")
	client, _ := dialConsumer(t, c.LoopbackURL(), consumerToken)

	stream, err := client.WatchRuns(context.Background(), &agentcoordpb.WatchRunsRequest{})
	require.NoError(t, err)

	first, err := stream.Recv()
	require.NoError(t, err)
	_, isSnapshot := first.GetKind().(*agentcoordpb.WatchEvent_Snapshot)
	require.True(t, isSnapshot, "the first WatchRuns frame must be a RosterSnapshot, got %T", first.GetKind())

	// Spawn the migrated child AFTER the watcher attached — proving genuinely
	// LIVE delivery, not a replay.
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing")
	require.NoError(t, err)

	frames := make(chan *agentcoordpb.WatchEvent, 32)
	go func() {
		for {
			f, ferr := stream.Recv()
			if ferr != nil {
				close(frames)
				return
			}
			frames <- f
		}
	}()

	deadline := time.After(conformanceWait)
	var sawDeltaText string
	for sawDeltaText == "" {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatal("WatchRuns stream ended before a delta event arrived")
			}
			ev := f.GetEvent()
			if ev == nil {
				continue
			}
			assert.Equal(t, out.RunID, ev.GetRunId(), "every live event self-identifies its run")
			if d := ev.GetMessageDelta(); d != nil && strings.Contains(d.GetText(), "echo:") {
				sawDeltaText = d.GetText()
			}
		case <-deadline:
			t.Fatal("timed out waiting for a live delta-text event")
		}
	}
	assert.Contains(t, sawDeltaText, "do the thing", "the scripted engine echoes the turn text verbatim")
}

// TestConsumerService_ListRuns pins the unary poll alternative: the same
// roster projection plane-2 ListRuns exposes, reachable without a stream.
func TestConsumerService_ListRuns(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing")
	require.NoError(t, err)

	client, _ := dialConsumer(t, c.LoopbackURL(), c.consumerCreds.token())
	require.Eventually(t, func() bool {
		res, err := client.ListRuns(context.Background(), &agentcoordpb.ListRunsRequest{})
		if err != nil || len(res.GetRuns()) != 1 {
			return false
		}
		return res.GetRuns()[0].GetRunId() == out.RunID
	}, conformanceWait, 10*time.Millisecond)
}

// TestConsumer_CredentialRejectedOnCoordinatorService is the read-only scope
// enforcement: a consumer credential authenticates ConsumerService only — it
// must be a rejected IDENTITY (PermissionDenied), not merely an unauthorized
// verb, on RunnerChannel/RunChannel/PublishEvents.
func TestConsumer_CredentialRejectedOnCoordinatorService(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	token := c.consumerCreds.token()
	require.NotEmpty(t, token)

	target, err := grpcTarget(c.LoopbackURL())
	require.NoError(t, err)
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearerCreds(token)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	coordClient := agentcoordpb.NewCoordinatorServiceClient(conn)

	_, err = coordClient.PublishEvents(context.Background(), &agentcoordpb.PublishEventsRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "a consumer credential must not authenticate CoordinatorService: %v", err)

	stream, err := coordClient.RunnerChannel(context.Background())
	require.NoError(t, err) // stream establishment succeeds; the interceptor rejects per-stream
	require.NoError(t, stream.Send(&agentcoordpb.RunnerFrame{Kind: &agentcoordpb.RunnerFrame_Hello{Hello: &agentcoordpb.RunnerHello{}}}))
	_, err = stream.Recv()
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

// TestConsumer_CredentialPersistedInEndpointFile pins the D1 discovery
// mechanism: an out-of-process viewer has no spawn-time env seam, so
// endpoint.json (0600, host-local) is its only way to learn the URL AND the
// read-only credential.
func TestConsumer_CredentialPersistedInEndpointFile(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)

	raw, err := os.ReadFile(filepath.Join(c.stateDir, "endpoint.json"))
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(c.stateDir, "endpoint.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "endpoint.json must stay 0600 — it now carries a credential")

	var ep endpointState
	require.NoError(t, json.Unmarshal(raw, &ep))
	assert.NotEmpty(t, ep.ConsumerCred)
	assert.Equal(t, c.consumerCreds.token(), ep.ConsumerCred)

	// The persisted token actually authenticates ConsumerService.
	client, _ := dialConsumer(t, c.LoopbackURL(), ep.ConsumerCred)
	_, err = client.ListRuns(context.Background(), &agentcoordpb.ListRunsRequest{})
	require.NoError(t, err)
}
