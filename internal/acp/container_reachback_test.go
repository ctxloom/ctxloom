package acp

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/mcpsocket"
)

// TestContainerReachBackEnv_NoSocket_ReturnsNothing pins the existing no-op
// case unchanged by this fallback: when the runner never exported
// CTXLOOM_MCP_SOCKET (no coordinator dial-home), there is nothing to share
// with the container and no bridge to stand up — regardless of host OS.
func TestContainerReachBackEnv_NoSocket_ReturnsNothing(t *testing.T) {
	// t.Setenv registers a cleanup that restores whatever this process's
	// environment held for mcpSocketEnvVar BEFORE this test touched it
	// (Unsetenv, if it was unset) — that capture happens on this call, before
	// we mutate anything further. This process may be running inside a real
	// ctxloom session whose runner already exported CTXLOOM_MCP_SOCKET into
	// this shell, so the test still isolates from that: it unsets the var
	// again immediately below, and t.Setenv's cleanup restores the ORIGINAL
	// (pre-test) state regardless of that later mutation.
	t.Setenv(mcpSocketEnvVar, "unused-placeholder")
	require.NoError(t, os.Unsetenv(mcpSocketEnvVar))

	for _, goos := range []string{"linux", "darwin", "windows"} {
		env, mounts, closeFn, err := containerReachBackEnv(isolation.Docker{}, goos)
		require.NoError(t, err, "goos=%s", goos)
		assert.Nil(t, env, "goos=%s", goos)
		assert.Nil(t, mounts, "goos=%s", goos)
		assert.Nil(t, closeFn, "goos=%s", goos)
	}
}

// TestContainerReachBackEnv_Linux_UsesUnixBindMount pins the PROVEN, UNCHANGED
// Linux path under the DEFAULT (identity) runtime: the env var passes through
// byte-for-byte and the socket's directory is bind-mounted at its identical
// path — no TCP bridge is started (nil close func), matching the pre-fallback
// behavior exactly. This alone cannot prove the mount routes through the
// mapper seam at all (an identity mapper makes a skipped mapper() call
// byte-identical to a used one) — see
// TestContainerReachBackEnv_Linux_RoutesMountThroughMapper for that control.
func TestContainerReachBackEnv_Linux_UsesUnixBindMount(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "mcp.sock")
	t.Setenv(mcpSocketEnvVar, sock)

	env, mounts, closeFn, err := containerReachBackEnv(isolation.Docker{}, "linux")
	require.NoError(t, err)
	require.Nil(t, closeFn, "linux path starts no TCP bridge")
	require.Len(t, env, 1)
	assert.Equal(t, mcpSocketEnvVar+"="+sock, env[0], "the unix path crosses unchanged")
	require.Len(t, mounts, 1)
	assert.Equal(t, isolation.Docker{}.ExposeMapped(filepath.Dir(sock), false), mounts[0],
		"the socket's directory is bind-mounted identical-path under the default runtime, exactly as before this fallback")
}

// TestContainerReachBackEnv_Linux_RoutesMountThroughMapper is the CONTROL
// TestContainerReachBackEnv_Linux_UsesUnixBindMount cannot be: it injects a
// NON-IDENTITY runtime (via isolation.NewDockerWithMapperForTest — mapper()
// is unexported so this package cannot build its own fake Runtime) and
// proves the socket-dir mount's Container side actually carries the MAPPED
// value, not the raw host directory. If containerReachBackEnv ever skipped
// rt.ExposeMapped and hand-built Mount{Host: dir, Container: dir} instead,
// this is the test that would catch it — the sibling test above could not,
// because identity makes a skipped mapper call byte-identical to a used one.
func TestContainerReachBackEnv_Linux_RoutesMountThroughMapper(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "mcp.sock")
	t.Setenv(mcpSocketEnvVar, sock)
	dir := filepath.Dir(sock)

	rt := isolation.NewDockerWithMapperForTest(func(hostPath string) string { return "/ctr" + hostPath })

	env, mounts, closeFn, err := containerReachBackEnv(rt, "linux")
	require.NoError(t, err)
	require.Nil(t, closeFn, "linux path starts no TCP bridge")
	require.Len(t, env, 1)
	assert.Equal(t, mcpSocketEnvVar+"="+sock, env[0],
		"the env var still carries the HOST-side socket path unchanged — only the mount's Container side is mapped")
	require.Len(t, mounts, 1)
	assert.Equal(t, isolation.Mount{Host: dir, Container: "/ctr" + dir}, mounts[0],
		"the socket-dir mount's Container side must be the MAPPED path, not the raw host directory")
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

		addr, ok := strings.CutPrefix(env[0], mcpSocketEnvVar+"="+mcpsocket.TCPPrefix)
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

// TestContainerReachBackEnv_NoSocket_SaysSo is the payload half of the no-socket
// case: delivering NOTHING is the correct behaviour (there is no endpoint to
// share), but it must not be silent. This path is only ever reached under
// container isolation, and its consequence is that the in-container engine gets
// no ctxloom MCP surface at all — every ctxloom tool the session's loadout
// promised is simply absent, with the session otherwise looking healthy.
func TestContainerReachBackEnv_NoSocket_SaysSo(t *testing.T) {
	t.Setenv(mcpSocketEnvVar, "unused-placeholder")
	require.NoError(t, os.Unsetenv(mcpSocketEnvVar))

	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	env, mounts, closeFn, err := containerReachBackEnv(isolation.Docker{}, "linux")
	require.NoError(t, err, "a missing endpoint is a degraded session, not a failed one")
	assert.Nil(t, env)
	assert.Nil(t, mounts)
	assert.Nil(t, closeFn)

	assert.Contains(t, warnings.String(), mcpSocketEnvVar, "the warning names the variable that was unset")
	assert.Contains(t, warnings.String(), "no ctxloom MCP", "and what the in-container engine therefore loses")
}

// syncBuffer is a warning sink a background goroutine writes while the test
// body reads it (clidiag.Warn runs on the bridge's own goroutine).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestReachBackBridge_UnreachableSocketIsDiagnosed pins the bridge's failure
// diagnostic. When the runner's socket cannot be dialled the accepted connection
// is closed, so the in-container MCP shim sees an unexplained connection reset —
// a container-side symptom whose cause is entirely on the host side and, without
// this, appears in no log anywhere.
func TestReachBackBridge_UnreachableSocketIsDiagnosed(t *testing.T) {
	sink := &syncBuffer{}
	restore := clidiag.SetSink(sink)
	t.Cleanup(restore)

	// A path with no listener behind it: dialling it always fails.
	sock := filepath.Join(t.TempDir(), "never-bound.sock")
	bridge, err := startReachBackBridge(sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bridge.Close() })

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", bridge.port), 2*time.Second)
	require.NoError(t, err, "the bridge accepts before it discovers the socket is dead")
	t.Cleanup(func() { _ = conn.Close() })

	require.Eventually(t, func() bool { return strings.Contains(sink.String(), "reach-back") }, 3*time.Second, 10*time.Millisecond,
		"the failed dial must be diagnosed, not just reset: got %q", sink.String())
	assert.Contains(t, sink.String(), sock, "the warning names the socket that could not be dialled")
}
