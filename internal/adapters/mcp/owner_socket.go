package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	runnermcp "github.com/ctxloom/ctxloom/internal/adapters/runner/mcp"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/pidalive"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// The plugin-hosted OWNER arm's endpoint. The interactive host run's engine
// reads the project .mcp.json, which names the stdio shim (`ctxloom mcp
// serve`); the shim forwards to this socket by CTXLOOM_MCP_SOCKET. A hosted
// run has none of this: its runner binds the Launch's loopback endpoint
// (runner/mcp) and its .mcp.json names the URL and bearer. This arm dies with
// the plugin protocol.

// RunnerMCP is the owner arm's endpoint: the unix listener + server.
type RunnerMCP struct {
	SocketPath string
	httpSrv    *http.Server
	cleanup    func()
}

// ServeRunnerMCP builds the owner arm's MCP surface over the process config,
// binds the unix socket and starts serving. It returns only with the socket
// LISTENING — the runner controls the engine spawn, and the socket exists
// before it (assert, don't race).
func ServeRunnerMCP(rep report.Sink, cfg *config.Config, harp string, home *runner.Home) (*RunnerMCP, error) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	s := &ctxServer{cfg: cfg, self: coord.Identity{Harp: harp, Project: cwd}}
	server, err := runnermcp.NewServer(report.To(rep), home, harp, cwd, false, configSurface{s: s})
	if err != nil {
		return nil, err
	}
	path, dir, cleanupSocket, err := runnerSocketPath()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		cleanupSocket()
		return nil, fmt.Errorf("runner MCP socket %s: %w", path, err)
	}
	if n := reapDeadRunnerSockets(dir); n > 0 {
		report.To(rep).Warnf("runner MCP: reaped %d dead runner socket(s) in %s", n, dir)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	srv := &http.Server{Handler: mux}
	go serveRunnerHTTP(report.To(rep), srv, ln, path)
	return &RunnerMCP{SocketPath: path, httpSrv: srv, cleanup: cleanupSocket}, nil
}

// configSurface is the owner arm's runnermcp.LocalSurface: the cell-local
// tools and resources over the process config, as the stdio server serves
// them.
type configSurface struct{ s *ctxServer }

func (c configSurface) Register(server *mcp.Server) []string {
	c.s.registerResources(server)
	return c.s.registerContextTools(server)
}

// serveRunnerHTTP runs the endpoint until it stops. http.Server's Serve
// ALWAYS returns an error; on a deliberate shutdown that error is
// http.ErrServerClosed, so any other value means the endpoint died while the
// runner carried on advertising a socket nothing answers on — the shim then
// dials a live-looking path and every ctxloom tool in that session fails
// with a transport error that names nothing about the real cause.
func serveRunnerHTTP(rep report.Reporter, srv *http.Server, ln net.Listener, socketPath string) {
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		rep.Warnf("runner MCP endpoint at %s stopped serving: %v (ctxloom tools in this session will fail until the runner is restarted)", socketPath, err)
	}
}

// Close shuts the endpoint down and removes its socket.
func (r *RunnerMCP) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.httpSrv.Shutdown(ctx)
	r.cleanup()
}

// runnerSocketPath picks the socket location on host user-private filesystem:
// $XDG_RUNTIME_DIR/ctxloom, else a private temp dir. Paths are kept short for
// the sun_path limit. Returns the socket path, the directory it lives in, and
// the cleanup that unlinks it.
func runnerSocketPath() (path string, dir string, cleanup func(), err error) {
	const sunPathHeadroom = 100
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		hostDir := filepath.Join(runtimeDir, "ctxloom")
		if mkErr := os.MkdirAll(hostDir, 0o700); mkErr == nil {
			p := filepath.Join(hostDir, fmt.Sprintf("mcp-%d.sock", os.Getpid()))
			if len(p) <= sunPathHeadroom {
				_ = os.Remove(p) // a stale same-pid socket from a recycled pid
				return p, hostDir, func() { _ = os.Remove(p) }, nil
			}
		}
	}
	tmpDir, mkErr := os.MkdirTemp("", "ctxloom-mcp-")
	if mkErr != nil {
		return "", "", nil, fmt.Errorf("runner MCP socket dir: %w", mkErr)
	}
	p := filepath.Join(tmpDir, "mcp.sock")
	if len(p) > sunPathHeadroom {
		_ = os.RemoveAll(tmpDir)
		return "", "", nil, fmt.Errorf("runner MCP socket path %q exceeds the portable sun_path limit", p)
	}
	return p, tmpDir, func() { _ = os.RemoveAll(tmpDir) }, nil
}

// reapDeadRunnerSockets unlinks socket files in dir whose owning pid is
// confirmed gone, returning how many it removed. Never reap on uncertainty:
// pidalive.Probe errs toward Alive under pid reuse, so an unsure verdict
// skips — removing a LIVE runner's socket would sever the only route to a
// working session, which is far worse than the leak this fixes. It exists
// because graceful cleanup cannot reach a process that died by SIGKILL, a
// crash or an OOM kill; the reap happens on the next startup instead. The pid
// is read from the FILENAME because that is where runnerSocketPath puts it
// (mcp-<pid>.sock); the private-temp tier names its socket mcp.sock inside a
// per-process directory, does not match this glob and is removed wholesale by
// its own cleanup.
func reapDeadRunnerSockets(dir string) int {
	paths, err := filepath.Glob(filepath.Join(dir, "mcp-*.sock"))
	if err != nil {
		return 0
	}
	removed := 0
	self := os.Getpid()
	for _, path := range paths {
		pid, ok := pidFromSocketName(filepath.Base(path))
		if !ok || pid == self {
			continue
		}
		if pidalive.Probe(pid).MaybeAlive() {
			continue // alive, or unsure — never reap on uncertainty
		}
		if os.Remove(path) == nil {
			removed++
		}
	}
	return removed
}

// pidFromSocketName extracts the owner pid from a runner socket's base name,
// reporting false for anything that is not exactly mcp-<digits>.sock. It is
// strict on purpose: a name this cannot parse is a file some other writer owns,
// and the sweep must leave it alone rather than guess.
func pidFromSocketName(base string) (int, bool) {
	const prefix, suffix = "mcp-", ".sock"
	if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, suffix) {
		return 0, false
	}
	pid, err := strconv.Atoi(base[len(prefix) : len(base)-len(suffix)])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
