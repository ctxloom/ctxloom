package coordgrpc

import (
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/discover"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/testsupport/coordharness"
)

// servedCoordinator stands a coordinator's listeners up over stateDir.
func servedCoordinator(t *testing.T, stateDir string) *coord.Coordinator {
	t.Helper()
	c := coordharness.New(t, stateDir)
	require.NoError(t, Serve(c))
	return c
}

// TestLoadEndpoint_CorruptFileIsReported: endpoint.json is what makes a
// relaunched coordinator re-bind the SAME ports, and a corrupt one silently
// decoded to the zero state — every port re-picked ephemerally, the stable-
// endpoint guarantee gone, and no way for anyone to tell that from a first
// ever start. Serving must still succeed (fault tolerance), but the loss has
// to be named.
func TestLoadEndpoint_CorruptFileIsReported(t *testing.T) {
	stateDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, discover.FileName), []byte("{ this is not json"), 0o600))
	warnings := captureWarnings(t)

	c := servedCoordinator(t, stateDir)

	assert.NotEmpty(t, c.LoopbackURL(), "a corrupt endpoint file must not stop the coordinator serving")
	assert.Contains(t, warnings.String(), discover.FileName, "the discarded endpoint state must be reported by name")
}

// TestLoadEndpoint_AbsentFileIsSilent: a first-ever start is the common case,
// not a fault — the warning above must not fire for it.
func TestLoadEndpoint_AbsentFileIsSilent(t *testing.T) {
	warnings := captureWarnings(t)

	servedCoordinator(t, t.TempDir())

	assert.NotContains(t, warnings.String(), discover.FileName, "a missing endpoint file is a first start, not corruption")
}

// TestServe_RecordedLoopbackPortIsReused pins the guarantee the file exists
// for, so the corrupt-file report above is measuring a real loss.
func TestServe_RecordedLoopbackPortIsReused(t *testing.T) {
	stateDir := t.TempDir()
	first := servedCoordinator(t, stateDir)
	firstURL := first.LoopbackURL()
	first.Close()

	second := servedCoordinator(t, stateDir)
	assert.Equal(t, firstURL, second.LoopbackURL(), "a relaunch must re-bind the recorded port")
}

// TestListen_PersistsTheAddressBeforeReturning: the address a cell had the
// coordinator listen on is on disk by the time Listen returns — the runner is
// started right after — and the NEXT coordinator re-binds it on the recorded
// port, so a container runner from before the restart redials something.
func TestListen_PersistsTheAddressBeforeReturning(t *testing.T) {
	addr := privateStandIn(t)
	stateDir := t.TempDir()
	first := servedCoordinator(t, stateDir)
	require.NoError(t, first.Transport().(*coordServing).Listen(present.Listen{Addr: addr}))

	raw, err := os.ReadFile(filepath.Join(stateDir, discover.FileName))
	require.NoError(t, err)
	var ep discover.State
	require.NoError(t, json.Unmarshal(raw, &ep))
	assert.Equal(t, []string{addr}, ep.ListenAddrs)
	firstURL := first.LoopbackURL()
	first.Close()

	second := servedCoordinator(t, stateDir)
	t.Cleanup(second.Close)
	require.Equal(t, firstURL, second.LoopbackURL())
	loop, err := url.Parse(second.LoopbackURL())
	require.NoError(t, err)
	conn, err := net.Dial("tcp", net.JoinHostPort(addr, loop.Port()))
	require.NoError(t, err, "the relaunched coordinator re-binds the recorded address")
	_ = conn.Close()
}
