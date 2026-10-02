//go:build windows

package claude

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// tokenRequired: on Windows claude admits a sender as self-sent by the token
// alone (source-read; the pipe itself is measured on CI).
const tokenRequired = true

// dialMessaging connects to claude's messaging named pipe (\\.\pipe\...).
func dialMessaging(ctx context.Context, endpoint string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, endpoint)
}
