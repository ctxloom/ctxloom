package coord

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// U024-F20: the host-relay handler table was written by SetCustomHandlers with
// no lock and read by serveCustom on every child's own dispatch goroutine, so
// its safety rested entirely on the unenforced convention "called once at
// hosting setup, before any runner connects". A convention is not a
// synchronisation primitive: a Go map read concurrent with a map write is a data
// race whatever the intended call order, and the race detector says so.
//
// Run under -race (the repo's test-pkg always is), this is the pin: it fails on
// the unguarded table and passes on the guarded one. The dispatch assertion is
// there too, so a "fix" that simply stopped reading the map could not pass.
func TestSetCustomHandlers_ConcurrentWithRelayDispatch_IsRaceFree(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	handler := func(_ context.Context, _ Identity, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}
	c.SetCustomHandlers(map[string]CustomHandler{"ctxloom/probe": handler})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.SetCustomHandlers(map[string]CustomHandler{"ctxloom/probe": handler})
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.serveCustom(ownerIdentity(), &agentcoordpb.CustomRequest{Name: "ctxloom/probe"})
		}()
	}
	wg.Wait()

	resp := c.serveCustom(ownerIdentity(), &agentcoordpb.CustomRequest{Name: "ctxloom/probe"})
	require.NotNil(t, resp)
	assert.Zero(t, resp.GetStatus().GetCode(),
		"the installed handler must still be dispatched, not merely un-raced: %s", resp.GetStatus().GetMessage())
}
