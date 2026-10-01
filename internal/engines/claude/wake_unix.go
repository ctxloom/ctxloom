//go:build !windows

package claude

import (
	"context"
	"net"
)

// tokenRequired: on Linux and macOS claude admits a self-sent post by process
// ancestry, so the token is sent when exported but a bind does not need it.
const tokenRequired = false

// dialMessaging connects to claude's messaging unix socket.
func dialMessaging(ctx context.Context, endpoint string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", endpoint)
}
