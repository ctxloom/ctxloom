package grpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"sync"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/hashicorp/go-plugin/runner"

	"github.com/ctxloom/ctxloom/internal/adapters/selfexec"
	"github.com/ctxloom/ctxloom/internal/version"
)

// ContainerRunnerFunc is go-plugin's RunnerFunc shape: given the (env-populated)
// spec command and the host temp dir go-plugin created for unix sockets, it
// returns a runner.Runner that launches the plugin — for the container transport,
// inside a container. A DEFINED type, not an alias: callers (internal/adapters/isolation)
// name the contract without importing go-plugin's exact signature, and a value
// carrying that name is a distinct type rather than any three-argument function
// that happens to match. go-plugin's own ClientConfig.RunnerFunc field is an
// unnamed func type, so a ContainerRunnerFunc remains assignable to it.
type ContainerRunnerFunc func(l hclog.Logger, cmd *exec.Cmd, tmpDir string) (runner.Runner, error)

// errRunSendHalfClosed is what a Run-stream pump gets for trying to send after
// the send direction was half-closed. It is an internal lifecycle signal, not a
// failure the caller can act on: the pump that receives it simply retires.
var errRunSendHalfClosed = errors.New("run stream: send direction is already half-closed")

// GRPCClient is the client-side implementation that communicates with the plugin.
type GRPCClient struct {
	client LLMClient
}

// Info returns metadata about the plugin.
func (c *GRPCClient) Info(ctx context.Context) (*LLMInfo, error) {
	return c.client.Info(ctx, &Empty{})
}

// RunResult contains the result of a Run call including model info.
type RunResult struct {
	ExitCode  int32
	ModelInfo *ModelInfo
}

// Run executes the plugin and streams output to the provided writers. stdin and
// resize are the frontend's terminal input (nil for non-interactive callers).
func (c *GRPCClient) Run(ctx context.Context, req *RunStart, stdin io.Reader, stdout, stderr io.Writer, resize <-chan *WindowSize) (int32, error) {
	result, err := c.RunWithModelInfo(ctx, req, stdin, stdout, stderr, resize)
	if err != nil {
		return 1, err
	}
	return result.ExitCode, nil
}

// RunWithModelInfo executes the plugin over the bidirectional Run stream: it
// sends the RunStart, then pumps the frontend's stdin and resize events to the
// controller (which feeds them into the agent's pty), while consuming the
// response stream. The frontend owns the terminal; this is the client half of
// that ownership. gRPC forbids concurrent Send on one stream, so the start +
// stdin + resize pumps share a send mutex.
func (c *GRPCClient) RunWithModelInfo(ctx context.Context, req *RunStart, stdin io.Reader, stdout, stderr io.Writer, resize <-chan *WindowSize) (*RunResult, error) {
	stream, err := c.client.Run(ctx)
	if err != nil {
		return nil, err
	}

	// sendClosed rides the same mutex as Send because gRPC forbids CloseSend
	// concurrently with SendMsg, and once the send direction is half-closed a
	// later Send is not merely rejected but undefined. Refusing it here makes
	// the surviving pump (resize) exit on a legible error of our own rather
	// than on transport behaviour nobody specified.
	var (
		sendMu     sync.Mutex
		sendClosed bool
	)
	send := func(in *RunInput) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		if sendClosed {
			return errRunSendHalfClosed
		}
		return stream.Send(in)
	}
	// halfClose ends the send direction exactly once. The server's Recv loop
	// (server.go's Run) treats the resulting io.EOF as "the frontend is done
	// sending" and closes its own resizeCh and the plugin-side stdin pipe on
	// exactly that signal — which is the ONLY thing that ever unblocks an
	// engine reading its stdin to EOF.
	halfClose := func() {
		sendMu.Lock()
		defer sendMu.Unlock()
		if sendClosed {
			return
		}
		sendClosed = true
		_ = stream.CloseSend()
	}

	if err := send(&RunInput{Input: &RunInput_Start{Start: req}}); err != nil {
		return nil, fmt.Errorf("send run start: %w", err)
	}

	// Neither a keystroke source nor a resize source: nothing else will ever
	// be sent on this stream (oneshot, or an interactive turn whose frontend
	// has no real tty — see interactiveTerminal). Half-close the send
	// direction now instead of leaving it open for the whole run. The
	// server's Recv loop (server.go's Run) treats a Recv error — which
	// CloseSend causes immediately via io.EOF — as "the frontend is done
	// sending" and closes its own resizeCh/stdin pipe on exactly that signal.
	// ptyrunner's pre-Start wait (initialResizeWait) is keyed off that
	// channel closing (the already-correct ok==false fast path), so this
	// turns what was an unconditional 300ms blind wait into an immediate
	// start. CloseSend is a normal bidi-stream lifecycle call (not a new wire
	// message); halfClose serializes it against both pumps, which in this
	// branch do not exist at all.
	if stdin == nil && resize == nil {
		halfClose()
	}

	// Pump keystrokes. At end of run the goroutine is typically parked in
	// stdin.Read; it exits when that read returns (error, or a stray byte the
	// dead stream rejects). For the one-shot `ctxloom run` process the parked
	// read is moot — the process exits. A caller that keeps reading stdin in
	// the same process after Run returns must NOT pass the raw os.Stdin here:
	// the parked read would swallow the next reader's input — it would need a
	// detachable lease over the shared source instead, detached once Run
	// returns, to unblock this goroutine and hand any in-flight bytes to the
	// next reader. No caller in this codebase needs that today (init hands
	// off and exits once its interactive run ends, rather than reading stdin
	// again afterward), but the hazard is real for the next one that does.
	if stdin != nil {
		go func() {
			buf := make([]byte, 4096)
			for {
				n, rerr := stdin.Read(buf)
				if n > 0 {
					if serr := send(&RunInput{Input: &RunInput_Stdin{Stdin: append([]byte(nil), buf[:n]...)}}); serr != nil {
						return
					}
				}
				if rerr != nil {
					// io.EOF is the SOURCE declaring itself exhausted: no
					// further byte will ever arrive from it. That is the same
					// condition the nil-stdin branch above half-closes for,
					// reached later, so it gets the same answer — otherwise
					// the plugin-side stdin pipe stays open and an engine
					// that reads to EOF blocks until the turn's deadline. A
					// finite non-tty reader (a vpio ProcessSpec.Stdin from a
					// test, the container-run driver, an embedder) is the
					// caller shape that hits this.
					//
					// Any OTHER read error is NOT "the caller is done
					// sending" — it is a source that failed, which for a real
					// terminal may be transient — so it keeps the original
					// behaviour of leaving the send direction open and
					// letting the run end on its own terms. A frontend
					// terminal cannot be truncated by the EOF arm either:
					// interactiveTerminal only hands over a keystroke source
					// after putting the tty in RAW mode, where a Ctrl-D is
					// the literal byte 0x04 and a read returns io.EOF only on
					// a genuine hangup — after which no keystroke is coming
					// anyway.
					if rerr == io.EOF {
						halfClose()
					}
					return
				}
			}
		}()
	}

	// Pump terminal resizes.
	if resize != nil {
		go func() {
			for ws := range resize {
				if serr := send(&RunInput{Input: &RunInput_Resize{Resize: ws}}); serr != nil {
					return
				}
			}
		}()
	}

	result := &RunResult{}
	// The exit code is the final message of every completed run (server.go's
	// Run ends with exactly that Send), so its ABSENCE at EOF is a truncated
	// run, not an exit 0 — the zero value of ExitCode cannot be trusted to
	// mean success.
	sawExit := false
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		switch output := resp.Output.(type) {
		case *RunResponse_Stdout:
			// A write the caller's sink refused (closed pipe, full disk) has
			// lost engine output for good; continuing would hand back a clean
			// exit code over a truncated transcript.
			if _, werr := stdout.Write(output.Stdout); werr != nil {
				return nil, fmt.Errorf("write engine stdout: %w", werr)
			}
		case *RunResponse_Stderr:
			if _, werr := stderr.Write(output.Stderr); werr != nil {
				return nil, fmt.Errorf("write engine stderr: %w", werr)
			}
		case *RunResponse_ExitCode:
			result.ExitCode = output.ExitCode
			// ModelInfo is sent with exit_code
			result.ModelInfo = resp.ModelInfo
			sawExit = true
		}
	}
	if !sawExit {
		return nil, fmt.Errorf("run stream ended without an exit code: the engine plugin died mid-run")
	}

	return result, nil
}

// LLMRunner manages the lifecycle of a plugin process. It embeds *GRPCClient
// so every wire operation (Info/Run/RunWithModelInfo/GetSession/ListSessions/
// GetPlans/WatchSession/Chat) is promoted for free; Kill is the one method
// LLMRunner defines itself, because it targets the runner's OS process, not
// the gRPC connection.
type LLMRunner struct {
	conn llmConnection
	*GRPCClient
}

// verbosityToHclogLevel converts verbosity count to hclog level:
// 0 = Error, 1 = Info, 2 = Debug, 3+ = Trace. Each -v widens the ladder by a
// full level; hclog.Warn is deliberately not a rung, and at 0 newPluginLogger
// discards the output entirely regardless of level.
func verbosityToHclogLevel(verbosity int) hclog.Level {
	switch {
	case verbosity >= 3:
		return hclog.Trace
	case verbosity == 2:
		return hclog.Debug
	case verbosity == 1:
		return hclog.Info
	default:
		return hclog.Error
	}
}

// llmConnection is the abstraction over the hashicorp/go-plugin
// machinery that runnerFromConn depends on. Production
// dialLLMConnection wraps a real *plugin.Client; tests inject a
// fake that returns canned errors at each lifecycle step.
type llmConnection interface {
	// Client returns the plugin's gRPC client interface, or an error if
	// the subprocess didn't come up.
	Client() (plugin.ClientProtocol, error)
	// Kill terminates the underlying subprocess. Idempotent.
	Kill()
}

// realLLMConnection adapts *plugin.Client to llmConnection.
type realLLMConnection struct {
	client *plugin.Client
}

func (r *realLLMConnection) Client() (plugin.ClientProtocol, error) { return r.client.Client() }

// Kill terminates the runner. go-plugin's own Client.Kill (called first,
// unchanged) only ever reaches the runner's OWN pid — the graceful path
// (close the RPC connection, let the runner exit on its own) or, failing
// that, a raw cmd.Process.Kill() fallback. Neither reaches a grandchild the
// runner deliberately isolated into its own process group — a hard kill
// never gives the runner a chance to run ITS OWN cleanup for that. killSession is the
// defensive sweep for that gap: the runner was spawned via
// isolateRunner (dialLLMConnection), so its pid doubles as its session id, and
// every descendant that never called setsid(2) itself — including one in a
// separate process group — stays tagged with it. A no-op for a container
// runner (its ID() is a container name, not numeric — containers get their
// own whole-subtree teardown via `docker rm -f`, isolation/runner.go, so
// this fix is host-only by construction, matching where the bug lives).
func (r *realLLMConnection) Kill() {
	pid, ok := runnerSessionPID(r.client)
	r.client.Kill()
	if ok {
		killSession(pid)
	}
}

// runnerSessionPID reads the runner's pid off the still-live *plugin.Client
// (its ID() is the OS pid for a real Cmd-based runner; call BEFORE Kill(),
// which clears the client's runner reference so ID() goes empty afterward).
func runnerSessionPID(c *plugin.Client) (int, bool) {
	pid, err := strconv.Atoi(c.ID())
	return pid, err == nil
}

// dialLLMConnection is the IoC seam tests override to avoid spawning
// real subprocesses. Production points it at the real go-plugin machinery.
var dialLLMConnection = func(cmd string, args []string, env []string, logger hclog.Logger) llmConnection {
	c := exec.Command(cmd, args...)
	env = pluginSpawnEnv(env)
	if len(env) > 0 {
		// Per-spawn runner env (the coordinator reach-back trio, plus the
		// plugin socket dir): stamped on the subprocess env, never the
		// process-global launcher env (racy across concurrent spawns).
		// go-plugin appends its handshake vars to a non-nil cmd.Env, so the
		// base environment must ride along.
		c.Env = append(os.Environ(), env...)
	}
	// Fresh session leader: gives killSession a safe,
	// scoped boundary — see realLLMConnection.Kill's doc comment.
	isolateRunner(c)
	return &realLLMConnection{client: plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: HandshakeConfig,
		Plugins:         PluginMap(),
		Cmd:             c,
		AllowedProtocols: []plugin.Protocol{
			plugin.ProtocolGRPC,
		},
		Logger: logger,
	})}
}

// pluginSpawnEnv returns env plus the go-plugin socket-dir variable, unless
// the launcher's own environment already carries one (go-plugin appends
// os.Environ() after cmd.Env, so an ambient value would win anyway; skipping
// it here just keeps that explicit and avoids a duplicate key).
//
// This is the ENV VAR and not ClientConfig.UnixSocketConfig.TempDir on
// purpose: in go-plugin's Cmd branch that field is never read — only the
// RunnerFunc branch (ContainerClientConfig's path) consumes it. For a
// Cmd-launched plugin the server side reads plugin.EnvUnixSocketDir directly.
func pluginSpawnEnv(env []string) []string {
	if _, ambient := os.LookupEnv(plugin.EnvUnixSocketDir); ambient {
		return env
	}
	dir := pluginSocketDir()
	if dir == "" {
		return env
	}
	return append(env, plugin.EnvUnixSocketDir+"="+dir)
}

// pluginSocketNameMax is the longest socket file name go-plugin produces:
// os.CreateTemp(dir, "plugin") appends a ten-digit random suffix.
const pluginSocketNameMax = len("/plugin0000000000")

// pluginSocketDir picks the directory the serve subprocess binds its
// go-plugin unix socket in, or "" to leave go-plugin's default (os.TempDir()).
//
// That default is what breaks inside an agent cell: the cell's TMPDIR is
// ~100 bytes deep by itself, so $TMPDIR/pluginNNNNNNNNNN exceeds sun_path
// (108 on Linux, 104 on macOS) and every handshake dies with
// "bind: invalid argument" before a line reaches stdout. Preference order
// mirrors runnerSocketPath's host tiers: $XDG_RUNTIME_DIR/ctxloom — short,
// user-private, and already where the runner's own sockets live — then
// os.TempDir() when it fits, then /tmp. A candidate is taken only if its
// worst-case socket path fits the portable headroom.
//
// Sharing one directory across concurrent sessions is collision-safe by
// construction: go-plugin names the socket with os.CreateTemp (O_EXCL,
// random suffix), so no two plugins can be handed the same path.
func pluginSocketDir() string {
	const sunPathHeadroom = 100
	candidates := []string{"", os.TempDir(), "/tmp"}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		candidates[0] = filepath.Join(xdg, "ctxloom")
	}
	for _, dir := range candidates {
		if dir == "" || len(dir)+pluginSocketNameMax > sunPathHeadroom {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			continue
		}
		return dir
	}
	return ""
}

// dialContainerConnection is the container-transport analogue of
// dialLLMConnection: instead of a Cmd it wires a RunnerFunc (the caller's
// container launcher) plus a UnixSocketConfig so the plugin creates its socket in
// socketTempDir — a host directory the runner bind-mounts into the container and
// whose container→host path the runner's AddrTranslator maps. This is the
// canonical go-plugin plugin-in-container transport (RunnerFunc + AddrTranslator);
// see NewContainerClient. It is a package var so tests can override it.
var dialContainerConnection = func(runnerFunc ContainerRunnerFunc, socketTempDir string, logger hclog.Logger) llmConnection {
	return &realLLMConnection{client: plugin.NewClient(ContainerClientConfig(runnerFunc, socketTempDir, logger))}
}

// ContainerClientConfig builds the go-plugin ClientConfig for the
// container transport. It is a named function rather than a literal inside
// dialContainerConnection because SkipHostEnv is a SECURITY property of this
// transport that has to be assertable.
//
// SkipHostEnv keeps this process's own environment out of the metadata Cmd
// go-plugin populates for the RunnerFunc. Without it go-plugin appends
// os.Environ() before its handshake vars, and the container runner's env
// curation (isolation.containerHandshakeEnv) forwards every PLUGIN_-prefixed
// key it is handed — so an ambient host PLUGIN_* variable would cross the
// container boundary and land on the world-readable `run` argv. With it, the
// Cmd's env is exactly go-plugin's own handshake set, which is what the
// curation's prefix match is meant to describe. Nothing is lost: the container
// path never EXECUTES that Cmd (newContainerRunner builds its own
// docker/podman command), it reads only its Env.
func ContainerClientConfig(runnerFunc ContainerRunnerFunc, socketTempDir string, logger hclog.Logger) *plugin.ClientConfig {
	return &plugin.ClientConfig{
		HandshakeConfig:  HandshakeConfig,
		Plugins:          PluginMap(),
		RunnerFunc:       runnerFunc,
		UnixSocketConfig: &plugin.UnixSocketConfig{TempDir: socketTempDir},
		SkipHostEnv:      true,
		AllowedProtocols: []plugin.Protocol{
			plugin.ProtocolGRPC,
		},
		Logger: logger,
	}
}

// newPluginLogger builds the hclog logger the plugin machinery uses. Verbosity 0
// discards output (quiet); >0 forwards plugin logs to stderr at the mapped level.
func newPluginLogger(verbosity int) hclog.Logger {
	output := io.Discard
	if verbosity > 0 {
		output = os.Stderr
	}
	return hclog.New(&hclog.LoggerOptions{
		Name:   "plugin",
		Output: output,
		Level:  verbosityToHclogLevel(verbosity),
	})
}

// runnerFromConn completes plugin startup on an established connection: dial the
// gRPC client, dispense the LLM plugin, wrap it in an *LLMRunner. On any failure
// it kills the connection so a half-started plugin (or container) never leaks.
// Shared by the self-invoking host (NewSelfInvokingClientForLabelEnv) and
// container (NewContainerClient) paths so the dispense/type-assert dance
// lives in one place.
func runnerFromConn(conn llmConnection) (*LLMRunner, error) {
	rpcClient, err := conn.Client()
	if err != nil {
		conn.Kill()
		return nil, err
	}

	raw, err := rpcClient.Dispense(LLMPluginKey)
	if err != nil {
		conn.Kill()
		return nil, err
	}

	grpcClient, ok := raw.(*GRPCClient)
	if !ok {
		conn.Kill()
		return nil, fmt.Errorf("unexpected plugin type: %T", raw)
	}

	// Version handshake (exposable-rental unit 1): a `llm serve`/`llm host`
	// plugin process can persist well past the CLI invocation that spawned it
	// — a long-lived structured-chat connection is held open for a whole
	// conversation, sometimes days — and every call over it keeps running
	// whatever behavior was compiled in at connect time, even as the on-disk
	// binary is rebuilt and reinstalled underneath it. Measured live: two
	// binding thefts and a rotations-strip were traced to `llm serve`
	// daemons still resident from an Aug-10/12 install answering Aug-13
	// calls. Ask the daemon its own build stamp right here, before this
	// runner is handed to any caller, and refuse it outright on a mismatch
	// rather than silently running turns against stale compiled-in code.
	if info, ierr := grpcClient.Info(context.Background()); ierr != nil {
		conn.Kill()
		return nil, fmt.Errorf("llm serve: version handshake failed: %w", ierr)
	} else if info != nil {
		if verr := checkDaemonVersion(info.CtxloomVersion); verr != nil {
			conn.Kill()
			return nil, verr
		}
	}

	return &LLMRunner{
		conn:       conn,
		GRPCClient: grpcClient,
	}, nil
}

// checkDaemonVersion refuses daemonVersion when it does not name this
// process's own ctxloom build (version.Version) — see runnerFromConn's doc for
// the daemon-staleness defect this closes.
//
// There is no unstamped exemption, and that is the whole design. A ctxloom
// that cannot name its own build does not run at all: the stamp is required at
// startup (internal/adapters/cli's stamp gate, on internal/version.ValidStamp), so a
// client reaching this call is always stamped, and a daemon answering without
// one is not "an old build we cannot verify" — it is a process whose identity
// cannot be established, which is precisely what this check exists to refuse.
// A comparison that declines to compare is the failure mode, not the safe
// default: it is how a plugin from an earlier install kept answering.
func checkDaemonVersion(daemonVersion string) error {
	if daemonVersion == version.Version {
		return nil
	}
	return fmt.Errorf("llm serve: %s", versionMismatchDetail(daemonVersion, version.Version))
}

// versionMismatchDetail states a build mismatch WITHOUT asserting a direction
// the comparison did not establish.
//
// The equality test above proves only that two builds differ. The message it
// used to emit went much further: it named the daemon "a plugin process from an
// earlier install" whose behavior "predates this install", and told the reader
// to stop that process. Both halves were false when this actually fired — the
// answering daemon was the NEWER build and the stale side was the caller's own
// long-lived process — so the remedy pointed at a process that was hosting live
// sessions. Following it would have killed working sessions and fixed nothing.
//
// Direction is computable, because a stamp carries a UTC build time, so this
// states it when BOTH sides parse and stays silent about it when either does
// not. The remedy names the OLDER side, which is the one that has to go, rather
// than assuming which role it plays.
func versionMismatchDetail(daemonVersion, clientVersion string) string {
	const restart = "restart it so both sides run the same build, then retry"

	daemonAt, daemonOK := version.BuildTime(daemonVersion)
	clientAt, clientOK := version.BuildTime(clientVersion)

	switch {
	case daemonOK && clientOK && daemonAt.Before(clientAt):
		return fmt.Sprintf(
			"build mismatch — the daemon reports ctxloom %q, this client is %q. The DAEMON is the older build, so a process from an earlier install is still answering: %s",
			daemonVersion, clientVersion, restart)
	case daemonOK && clientOK && clientAt.Before(daemonAt):
		return fmt.Sprintf(
			"build mismatch — the daemon reports ctxloom %q, this client is %q. THIS CLIENT is the older build — a long-lived process keeps the binary image it started with while the file on disk is replaced — so the daemon is current and killing it would not help: %s",
			daemonVersion, clientVersion, restart)
	default:
		return fmt.Sprintf(
			"build mismatch — the daemon reports ctxloom %q, this client is %q. Which one is older cannot be determined from these stamps, so neither is named as the culprit: %s",
			daemonVersion, clientVersion, restart)
	}
}

// NewContainerClient creates a plugin client whose backend server runs INSIDE a
// container, launched by the caller-provided RunnerFunc. It is the container
// analogue of NewSelfInvokingClientForLabel: same *LLMRunner (hence pb.Client),
// but Kill tears down the container via the runner. socketTempDir is a host
// directory under which go-plugin creates the unix-socket dir it bind-mounts into
// the container; the runner's AddrTranslator maps the plugin's announced
// container-namespace socket path back to that host mount. The in-container
// argv — backend name, label, and the container's own ctxloom path — is built
// by the RunnerFunc, so none of it is a parameter here.
func NewContainerClient(verbosity int, runnerFunc ContainerRunnerFunc, socketTempDir string) (*LLMRunner, error) {
	return runnerFromConn(dialContainerConnection(runnerFunc, socketTempDir, newPluginLogger(verbosity)))
}

// NewSelfInvokingClientForLabel is NewSelfInvokingClient carrying the resolved
// config label into the serve subprocess. With two labels of the same backend
// type, serve's type-based lookup is map-ordered — the run would randomly
// apply either label's binary/args/env per process. Callers that resolved a
// specific label (the run path) pass it so serve configures exactly that
// entry; label may be empty when only the type is known.
func NewSelfInvokingClientForLabel(backendName, label string, verbosity int) (*LLMRunner, error) {
	return NewSelfInvokingClientForLabelEnv(backendName, label, verbosity, nil)
}

// NewSelfInvokingClientForLabelEnv is NewSelfInvokingClientForLabel with the
// per-spawn runner env seam: spawnEnv entries are stamped onto the serve
// SUBPROCESS env (the coordinator reach-back trio — the runner-terminated
// MCP path's one credential holder). nil spawnEnv is the plain spawn.
func NewSelfInvokingClientForLabelEnv(backendName, label string, verbosity int, spawnEnv map[string]string) (*LLMRunner, error) {
	// Resolve the running binary upgrade-safely: after an in-place upgrade,
	// bare os.Executable() reports "/path/ctxloom (deleted)" on Linux, which a
	// long-running MCP server (distill/recover tools) would then exec and
	// fail. selfexec strips the suffix and falls back to a PATH lookup.
	executable := selfexec.Path()

	args := []string{"llm", "serve", backendName}
	if label != "" {
		args = append(args, "--label", label)
	}
	env := make([]string, 0, len(spawnEnv))
	for k, v := range spawnEnv {
		env = append(env, k+"="+v)
	}
	sort.Strings(env) // deterministic spawn env
	return runnerFromConn(dialLLMConnection(executable, args, env, newPluginLogger(verbosity)))
}

// Kill terminates the plugin process. Safe on a nil receiver: this is
// belt-and-braces against Go's classic typed-nil-in-interface pitfall — a
// failed spawn's nil *LLMRunner passed through a (Client, error)-typed
// return-conversion boundary (Runtime.Spawn, {Container,Worktree,None}.
// SpawnClient) boxes into a non-nil Client interface value, so a caller's
// `if client != nil` check tests the INTERFACE, which is non-nil even though
// the underlying *LLMRunner is nil, and deferred teardown (e.g. cli/run.go's
// teardownTransport) ends up calling Kill on a nil receiver. Every boundary
// on that chain now returns an explicit nil interface on error instead of
// forwarding the concrete pointer, so this guard should never fire in
// today's callers — it stays as the correct behavior for a nil receiver
// regardless, and as the backstop for any future boundary that reintroduces
// the boxing. The spawn failure's own error is already returned and
// surfaced independently of Kill, so no-op'ing here costs nothing.
func (p *LLMRunner) Kill() {
	if p == nil {
		return
	}
	if p.conn != nil {
		p.conn.Kill()
	}
}
