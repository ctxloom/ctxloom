package acp

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
)

// TestContainerReachBackEnv_NoSocket_ReturnsNothing pins the existing no-op
// case unchanged by this fallback: when the runner never exported
// CTXLOOM_MCP_SOCKET (no coordinator dial-home), there is nothing to share
// with the container and no bridge to stand up — regardless of host OS.
func TestContainerReachBackEnv_NoSocket_ReturnsNothing(t *testing.T) {
	// t.Setenv("", "") would panic on an empty key; Unsetenv plus a t.Cleanup
	// restore is the standard pattern for clearing (not just overriding) an
	// AMBIENT var — this process may be running inside a real ctxloom
	// session whose runner already exported CTXLOOM_MCP_SOCKET into this
	// shell, and this test must isolate from that regardless.
	prev, had := os.LookupEnv(mcpSocketEnvVar)
	require.NoError(t, os.Unsetenv(mcpSocketEnvVar))
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(mcpSocketEnvVar, prev)
		}
	})

	for _, goos := range []string{"linux", "darwin", "windows"} {
		env, mounts, closeFn, err := containerReachBackEnv(isolation.Docker{}, goos)
		require.NoError(t, err, "goos=%s", goos)
		assert.Nil(t, env, "goos=%s", goos)
		assert.Nil(t, mounts, "goos=%s", goos)
		assert.Nil(t, closeFn, "goos=%s", goos)
	}
}

// TestContainerReachBackEnv_Linux_UsesUnixBindMount pins the PROVEN, UNCHANGED
// Linux path: the env var passes through byte-for-byte and the socket's
// directory is bind-mounted identical-path — no TCP bridge is started (nil
// close func), matching the pre-fallback behavior exactly.
func TestContainerReachBackEnv_Linux_UsesUnixBindMount(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "mcp.sock")
	t.Setenv(mcpSocketEnvVar, sock)

	env, mounts, closeFn, err := containerReachBackEnv(isolation.Docker{}, "linux")
	require.NoError(t, err)
	require.Nil(t, closeFn, "linux path starts no TCP bridge")
	require.Len(t, env, 1)
	assert.Equal(t, mcpSocketEnvVar+"="+sock, env[0], "the unix path crosses unchanged")
	require.Len(t, mounts, 1)
	assert.Equal(t, isolation.Docker{}.ExposeIdentical(filepath.Dir(sock), false), mounts[0],
		"the socket's directory is bind-mounted identical-path, exactly as before this fallback")
}

// TestContainerReachBackEnv_NonLinux_BridgesToTCP is the fallback itself: off
// Linux, no mount is produced (a bind-mounted unix socket file is not a live
// endpoint across the Docker Desktop VM boundary), and the env var instead
// carries "tcp://<dial-host>:<port>" for a host-loopback bridge this function
// stands up. It proves the bridge is REAL, not just wiring: writing bytes
// into the TCP port and reading them back off a real unix listener at sock
// (round-tripping through the bridge's io.Copy splice both ways).
func TestContainerReachBackEnv_NonLinux_BridgesToTCP(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "mcp.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	// A trivial echo server standing in for the runner's real MCP HTTP
	// server — this test proves the BRIDGE (byte transparency), not the MCP
	// protocol riding it.
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	t.Setenv(mcpSocketEnvVar, sock)

	cases := []struct {
		goos     string
		rt       isolation.Runtime
		wantHost string
	}{
		{"darwin", isolation.Docker{}, "host.docker.internal"},
		{"windows", isolation.Docker{}, "host.docker.internal"},
		{"darwin", isolation.Podman{}, "host.containers.internal"},
	}
	for _, tc := range cases {
		env, mounts, closeFn, err := containerReachBackEnv(tc.rt, tc.goos)
		require.NoError(t, err, "goos=%s rt=%s", tc.goos, tc.rt.Name())
		require.NotNil(t, closeFn, "a TCP bridge must be started off Linux")
		assert.Nil(t, mounts, "no bind mount off Linux — the fallback replaces it")
		require.Len(t, env, 1)

		addr, ok := strings.CutPrefix(env[0], mcpSocketEnvVar+"="+reachBackTCPPrefix)
		require.True(t, ok, "env value %q must carry the tcp:// form", env[0])
		host, portStr, serr := net.SplitHostPort(addr)
		require.NoError(t, serr)
		assert.Equal(t, tc.wantHost, host, "goos=%s rt=%s", tc.goos, tc.rt.Name())

		// Prove the bridge actually proxies bytes: dial the reserved
		// 127.0.0.1 port directly (the container would instead resolve
		// tc.wantHost to this same host loopback via Docker/Podman
		// Desktop's networking — not reproducible in this unit test, see
		// the live-verification runbook), round-trip a payload through the
		// unix echo listener, and close cleanly.
		conn, derr := net.DialTimeout("tcp", "127.0.0.1:"+portStr, 2*time.Second)
		require.NoError(t, derr, "the bridge must accept a connection on its reserved loopback port")
		const payload = "acp-reachback-tcp-fallback"
		_, werr := conn.Write([]byte(payload))
		require.NoError(t, werr)
		buf := make([]byte, len(payload))
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, rerr := io.ReadFull(conn, buf)
		require.NoError(t, rerr, "bytes must round-trip through the bridge to the unix listener and back")
		assert.Equal(t, payload, string(buf))
		_ = conn.Close()

		require.NoError(t, closeFn())
	}
}
