//go:build windows

package claude

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// listenEndpoint listens on a named pipe, which is what dialMessaging dials
// here (winio.DialPipeContext), closed at cleanup. The name is unique per
// process and call so parallel test binaries never contend for one pipe.
func listenEndpoint(t *testing.T) (string, net.Listener) {
	t.Helper()
	path := fmt.Sprintf(`\\.\pipe\ctxloom-wake-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	ln, err := winio.ListenPipe(path, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return path, ln
}

// Here claude admits a sender by the token alone, so a bind without one is
// refused up front rather than posting something claude would drop.
func TestMessagingWake_BindRefusesWithoutAToken(t *testing.T) {
	path, _ := listenEndpoint(t)
	_, err := messagingWake{}.Bind(context.Background(), envOf(map[string]string{envMessagingSocket: path}))
	require.ErrorIs(t, err, engine.ErrWakeUnbound)
	assert.Contains(t, err.Error(), envMessagingToken)
}
