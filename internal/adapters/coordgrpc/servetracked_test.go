package coordgrpc

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport/coordharness"
	"github.com/ctxloom/ctxloom/internal/testsupport/spooltest"
)

// TestServeTracked_EndsUnavailableOnceTheCoordinatorCloses: a stream that
// reaches serveTracked after the coordinator's Close began cannot have its
// pump or reader tracked, so it is served by neither — the stream ends
// Unavailable, the code a runner reads as "the coordinator is going, redial".
func TestServeTracked_EndsUnavailableOnceTheCoordinatorCloses(t *testing.T) {
	spooltest.TeeHome(t)
	dir := t.TempDir()
	c, err := coord.New(coord.Options{
		ProjectDir: dir,
		StateDir:   dir,
		Spawner:    coordharness.NopSpawner{},
		OwnerHarp:  coordharness.OwnerHarp,
	})
	require.NoError(t, err)
	c.Close()

	var pumped, read atomic.Bool
	err = serveTracked(c, context.Background(), "test stream closed",
		func() { pumped.Store(true) },
		func() error { read.Store(true); return nil })

	assert.Equal(t, codes.Unavailable, status.Code(err), "stream ended %v", err)
	assert.False(t, pumped.Load(), "the closed coordinator ran the stream's pump")
	assert.False(t, read.Load(), "the closed coordinator ran the stream's reader")
}
