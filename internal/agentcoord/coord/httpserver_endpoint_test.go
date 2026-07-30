package coord

import (
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/discover"
)

// servedCoordinator stands a coordinator's listeners up over stateDir.
func servedCoordinator(t *testing.T, stateDir string) *Coordinator {
	t.Helper()
	c := newTestCoordinatorAt(t, stateDir)
	t.Cleanup(c.Close)
	require.NoError(t, c.Serve())
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

// TestEnsureWide_PersistsTheWidePortBeforeReturning: the wide port is written
// so the NEXT coordinator re-binds it, and the caller's very next move is to
// spawn a container child that discovers the coordinator through this file. The
// persist used to be dispatched on a goroutine purely because the calling
// function still held the lock the writer takes — an invisible re-entrancy
// dependency whose visible cost is that the file lags the return.
func TestEnsureWide_PersistsTheWidePortBeforeReturning(t *testing.T) {
	if len(containerReachIPs()) == 0 {
		t.Skip("no container-reachable host interface on this machine: ensureWide has nothing to bind")
	}
	stateDir := t.TempDir()
	c := servedCoordinator(t, stateDir)

	advertised, err := c.srv.ensureWide()
	require.NoError(t, err)
	parsed, err := url.Parse(advertised)
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(parsed.Host)
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(stateDir, discover.FileName))
	require.NoError(t, err)
	var ep discover.State
	require.NoError(t, json.Unmarshal(raw, &ep))
	assert.Equal(t, port, strconv.Itoa(ep.WidePort), "the wide port must be on disk by the time ensureWide returns")
}
