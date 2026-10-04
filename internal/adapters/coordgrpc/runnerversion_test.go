package coordgrpc

import (
	"context"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/version"
	"github.com/ctxloom/ctxloom/internal/testsupport/coordharness"
	"github.com/ctxloom/ctxloom/internal/testsupport/spooltest"
)

const (
	coordStamp = "v0.7.0-aaaaaaa-20261004T010000"
	olderStamp = "v0.7.0-bbbbbbb-20261001T010000"
)

// A runner that outlived its coordinator re-Hellos against the re-bindable
// endpoint and may be a different build. The mismatch is REPORTED, never
// refused: refusing would strand a container runner holding live runs. A
// runner whose version cannot be verified (empty, or not a whole stamp) is
// not a mismatch.
func TestRunnerChannel_RunnerBuildMismatch_IsReportedAndAccepted(t *testing.T) {
	for _, tc := range []struct {
		name, runner string
		warns        bool
	}{
		{"different build", olderStamp, true},
		{"same build", coordStamp, false},
		{"empty version", "", false},
		{"dev version", "dev", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			warnings := helloRunner(t, coordStamp, tc.runner)
			if !tc.warns {
				assert.Empty(t, warnings)
				return
			}
			require.Len(t, warnings, 1)
			assert.Contains(t, warnings[0], olderStamp)
			assert.Contains(t, warnings[0], coordStamp)
		})
	}
}

// A coordinator that cannot name its own build cannot judge anyone else's.
func TestRunnerChannel_UnstampedCoordinator_ReportsNoMismatch(t *testing.T) {
	assert.Empty(t, helloRunner(t, "", olderStamp))
}

// helloRunner serves a coordinator whose build is coordVersion, opens a
// RunnerChannel announcing runnerVersion, and returns the advisory findings
// reported by the time the HelloAck arrived — the handshake is ACCEPTED in
// every case.
func helloRunner(t *testing.T, coordVersion, runnerVersion string) []string {
	t.Helper()
	prev := version.Version
	version.Version = coordVersion
	t.Cleanup(func() { version.Version = prev })

	var mu sync.Mutex
	var warnings []string
	spooltest.TeeHome(t)
	dir := t.TempDir()
	c, err := coord.New(coord.Options{
		ProjectDir: dir,
		StateDir:   dir,
		Spawner:    coordharness.NopSpawner{},
		OwnerHarp:  coordharness.OwnerHarp,
		Reporter: report.SinkFunc(func(f report.Finding) {
			if f.Kind == "" {
				mu.Lock()
				warnings = append(warnings, f.Text)
				mu.Unlock()
			}
		}),
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	require.NoError(t, Serve(c))
	token, err := c.RegisterSessionOwner(coordharness.OwnerHarp)
	require.NoError(t, err)

	u, err := url.Parse(c.LoopbackURL())
	require.NoError(t, err)
	conn, err := grpc.NewClient(u.Host,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearerOf(token)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	stream, err := agentcoordpb.NewCoordinatorServiceClient(conn).RunnerChannel(ctx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&agentcoordpb.RunnerFrame{Kind: &agentcoordpb.RunnerFrame_Hello{
		Hello: &agentcoordpb.RunnerHello{Version: runnerVersion, Harnesses: []string{"mock"}},
	}}))
	frame, err := stream.Recv()
	require.NoError(t, err)
	require.True(t, frame.GetHelloAck().GetAccepted(), "a version mismatch is reported, never refused")

	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), warnings...)
}
