package mock

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"maps"
	"strconv"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// Backend is the mock kind's agent.Backend: the in-process double that
// echoes prompts and context and calls no model. Its Execute is the
// interactive path and the legacy one-shot's; the structured drive
// is the kind's own driver (turn.go). fragments and managed are what a
// launch delivered to this backend, which Execute's echo and record file
// report; the runner's delivery hands none through this type, so both are
// empty on a live turn.
type Backend struct {
	agent.LaunchBackend
	fragments []*agent.Fragment
	// managed is the host-assembled setup payload, which Execute's
	// recordInput reports. nil is a legitimate value.
	managed *agent.ManagedConfig
	// homeEnvKeys are the kind's home-relocation variables, echoed in the
	// record when set.
	homeEnvKeys []string
	// kind is the engine kind this backend runs: the interactive echo
	// composes its exec through it, as the runner does, to read what the
	// session was delivered (its hooks file, its wake socket).
	kind Mock
}

// Config is the mock family's typed LLM config, decoded from a labeled
// entry's body (Mock.NewConfig). Control carries the CTXLOOM_MOCK_* knobs
// (response, exit code, record file) through to Execute via the run
// request's env. It is TEST CONTROL, not credentials, and its key says so:
// `mock_control`, not the retired `env` (config.RetiredLLMEnvKey) — no
// real engine carries an environment map in its config, so the mock is
// the only label body with one, and it must not read as the place a
// credential goes. kind is the double the config drives: a decoded config
// names its OWN backend, so a shared struct never reports "mock" for a
// double.
type Config struct {
	Model   string            `mapstructure:"model"`
	Control map[string]string `mapstructure:"mock_control"`
	kind    engine.Name
}

// BackendType identifies the backend this config drives.
func (c Config) BackendType() string { return string(c.kind) }

// MockControl returns the labeled entry's test-control map. Shared code
// (operations.MockControlFor) reaches it through an interface assertion
// rather than a concrete-type switch: the adapters must not branch on a
// backend's identity.
func (c Config) MockControl() map[string]string { return c.Control }

// NewConfig is agent.Hosted's: the zero config naming this double.
func (m Mock) NewConfig() agent.BackendConfig { return &Config{kind: m.Name} }

// Backend is agent.Hosted's: a fresh backend for this double. The doubles
// differ ONLY in name and in what their kind declares, so they share one
// constructor rather than a body each that could drift into behaving
// differently. History is NilSessionHistory — mock keeps no transcripts.
func (m Mock) Backend(agent.Launcher) agent.Backend {
	b := &Backend{kind: m}
	b.BaseBackend = agent.NewBaseBackend(string(m.Name), "1.0.0")
	b.InitLaunch(
		agent.NewBaseLifecycle(string(m.Name)),
		agent.NewBaseContextProvider(),
		&NilSessionHistory{},
		m.Declaration(),
	)
	for _, v := range m.home.Vars {
		b.homeEnvKeys = append(b.homeEnvKeys, v.Name)
	}
	return b
}

// Execute runs the mock backend with the given request.
// It echoes back information about the request for testing purposes.
func (b *Backend) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	// Build model info
	modelInfo := &agent.ModelInfo{
		ModelName: "mock-model",
		Provider:  "mock",
	}

	// CTXLOOM_MOCK_ECHO_STDIN drives the INTERACTIVE echo path used by the
	// docker-exec turn's integration proof (Phase 2a): a real interactive
	// engine reads keystrokes and reflects them, and reacts to terminal
	// resizes — the default echo (prompt/context only) reads neither, so it
	// cannot prove the host-pty → exec → turn → engine → back chain. This mode
	// reflects one typed line and the resize it saw, then exits.
	if Env(req.Env, "CTXLOOM_MOCK_ECHO_STDIN") == "1" && req.Stdin != nil {
		return b.executeInteractiveEcho(ctx, req, stdout, stderr, modelInfo)
	}

	// Assemble context from fragments
	contextStr := agent.AssembleContext(b.fragments)
	promptContent := agent.GetPromptContent(req.Prompt)

	if err := b.recordInput(Env(req.Env, "CTXLOOM_MOCK_RECORD_FILE"), req, contextStr, promptContent); err != nil {
		// A record-file write failure used to only warn to stderr and
		// return nothing, so Execute reported success with no record
		// file — a hermetic test asserting against it would then silently
		// read a STALE file from a previous run instead of failing loudly.
		return &agent.ExecuteResult{ExitCode: 1, ModelInfo: modelInfo}, err
	}

	customResponse, hasCustomResponse := LookupEnv(req.Env, "CTXLOOM_MOCK_RESPONSE")
	failPrefix := Env(req.Env, "CTXLOOM_MOCK_FAIL_PREFIX") == "1"
	response := buildMockResponse(customResponse, hasCustomResponse, contextStr, promptContent, req.Mode, len(b.fragments), failPrefix)

	if _, err := stdout.Write([]byte(response)); err != nil {
		return &agent.ExecuteResult{ExitCode: 1, ModelInfo: modelInfo}, fmt.Errorf("failed to write response: %w", err)
	}

	return &agent.ExecuteResult{ExitCode: mockExitCode(req), ModelInfo: modelInfo}, nil
}

// recordInput writes the assembled request to recordFile when one is set
// (via CTXLOOM_MOCK_RECORD_FILE), returning the write error (if any) so the
// caller can fail loudly instead of reporting success with no record file.
//
// Records BOTH the process's actual cwd (os.Getwd) and req.WorkDir, plus
// whichever of the kind's home-relocation vars are set, so a hermetic test
// can prove WHERE the engine ran and WHAT isolation env it received. The
// two are different signals: this backend never spawns a grandchild, so
// os.Getwd is the hosting process's own cwd on every workspace axis, while
// req.WorkDir is the resolved isolation workspace — the one a test reads to
// observe the workspace boundary; cwd rides beside it for diagnostics.
func (b *Backend) recordInput(recordFile string, req *agent.ExecuteRequest, contextStr, promptContent string) error {
	rec := Record{
		Mode:          int32(req.Mode),
		WorkDir:       req.WorkDir,
		Env:           req.Env,
		Context:       contextStr,
		Prompt:        promptContent,
		FragmentCount: len(b.fragments),
		HomeEnvKeys:   b.homeEnvKeys,
	}
	if b.managed != nil {
		rec.DenyTools = b.managed.DenyTools
		for _, sk := range b.managed.Skills {
			rec.Skills = append(rec.Skills, sk.Name)
		}
	}
	return WriteRecord(recordFile, rec)
}

// mockExitCode returns the exit code from CTXLOOM_MOCK_EXIT_CODE, or 0.
func mockExitCode(req *agent.ExecuteRequest) int32 {
	exitCodeStr := Env(req.Env, "CTXLOOM_MOCK_EXIT_CODE")
	if exitCodeStr == "" {
		return 0
	}
	if code, err := strconv.Atoi(exitCodeStr); err == nil {
		return int32(code)
	}
	return 0
}

// buildMockResponse is the engine kind's Response (internal/engines/mock).
func buildMockResponse(customResponse string, hasCustomResponse bool, contextStr, promptContent string, mode agent.ExecutionMode, fragmentCount int, failPrefix bool) string {
	return Response(customResponse, hasCustomResponse, contextStr, promptContent, int32(mode), fragmentCount, failPrefix)
}

// executeInteractiveEcho reflects each typed line and the latest terminal
// resize it has observed — the interactive engine behavior the docker-exec
// turn's integration test round-trips through the full pty chain, and the
// session owner acceptance drives. It loops (echoing `mock echo: <line>`
// plus, once a resize has been seen, `mock winsize: RxC`) until a "quit" line
// or EOF: a tty rarely EOFs, so the sentinel is what lets a test drive a
// resize BETWEEN two lines (giving the daemon's SIGWINCH time to propagate)
// and then end the turn deterministically.
//
// Every non-blank line is a prompt submitted, so the session's delivered
// turn_start hooks fire with it before the echo — the hook the session
// owner's mail rides. A line posted to the session's wake socket (the
// exec's EnvWakeSocket) is taken exactly as a typed line: the mock's own
// wake.
func (b *Backend) executeInteractiveEcho(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer, modelInfo *agent.ModelInfo) (*agent.ExecuteResult, error) {
	done := func() (*agent.ExecuteResult, error) {
		return &agent.ExecuteResult{ExitCode: mockExitCode(req), ModelInfo: modelInfo}, nil
	}
	ex, err := b.interactiveExec(req)
	if err != nil {
		return &agent.ExecuteResult{ExitCode: 1, ModelInfo: modelInfo}, err
	}
	hooks, err := deliveredHooks(ex)
	if err != nil {
		return &agent.ExecuteResult{ExitCode: 1, ModelInfo: modelInfo}, err
	}
	hookEnv := maps.Clone(req.Env)
	if hookEnv == nil {
		hookEnv = map[string]string{}
	}
	maps.Copy(hookEnv, ex.Env)

	// ReadString blocks until a line arrives, so a cancelled ctx checked
	// only AFTER it returns can never interrupt a stdin that never produces
	// a line (a pty rarely EOFs — see the doc above). Reading on a goroutine
	// and selecting on ctx.Done() lets THIS function return promptly on
	// cancellation; the goroutine itself may still leak until the peer
	// writes or closes, which is the documented tradeoff (the "quit"/EOF
	// sentinel), not something fixable from this side alone.
	lines := make(chan echoLine)
	stop, err := ListenWakes(ex.Env[EnvWakeSocket], lines, func(line string) echoLine { return echoLine{line: line} })
	if err != nil {
		return &agent.ExecuteResult{ExitCode: 1, ModelInfo: modelInfo}, err
	}
	defer stop()
	go func() {
		r := bufio.NewReader(req.Stdin)
		for {
			line, err := r.ReadString('\n')
			lines <- echoLine{line: line, err: err}
			if err != nil {
				return
			}
		}
	}()

	echo := interactiveEcho{req: req, stdout: stdout, stderr: stderr, hooks: hooks, env: hookEnv}
	for {
		select {
		case <-ctx.Done():
			return done()
		case res := <-lines:
			if echo.take(ctx, res) {
				return done()
			}
		}
	}
}

// interactiveExec is the exec this session's runner composed — the same
// kind over the same session and presentations — or the zero exec when the
// request names no session (a bare echo with nothing delivered).
func (b *Backend) interactiveExec(req *agent.ExecuteRequest) (engine.Exec, error) {
	if req.Session == nil {
		return engine.Exec{}, nil
	}
	inst, err := b.kind.Instance(*req.Session)
	if err != nil {
		return engine.Exec{}, err
	}
	return inst.Exec(req.Presented)
}

// echoLine is one line the terminal or the wake socket delivered, or the
// error that ended the terminal.
type echoLine struct {
	line string
	err  error
}

// interactiveEcho is one interactive session's line handling.
type interactiveEcho struct {
	req            *agent.ExecuteRequest
	stdout, stderr io.Writer
	hooks          wire.UnifiedHooks
	env            map[string]string
	lastWS         agent.WindowSize
	sawWS          bool
}

// take handles one line: quit ends the session; a non-blank line fires the
// turn_start hooks (a failing hook is reported, and the session goes on —
// a TUI does not die because a hook did) and is echoed. It reports whether
// the session ended.
func (e *interactiveEcho) take(ctx context.Context, res echoLine) bool {
	line := strings.TrimRight(res.line, "\r\n")
	if line == "quit" {
		return true
	}
	if strings.TrimSpace(line) != "" {
		if err := FireHooks(ctx, e.hooks, "turn_start", "", line, e.req.WorkDir, e.env); err != nil {
			_, _ = fmt.Fprintf(e.stderr, "mock: %v\n", err)
		}
		e.drainResize()
		_, _ = fmt.Fprintf(e.stdout, "mock echo: %s\n", line)
		if e.sawWS {
			_, _ = fmt.Fprintf(e.stdout, "mock winsize: %dx%d\n", e.lastWS.Rows, e.lastWS.Cols)
		}
	}
	return res.err != nil
}

// drainResize picks up every resize delivered so far without blocking — a
// resize sent BETWEEN two lines (the integration test's pattern) is on the
// channel by the time the next line's turn drains it.
func (e *interactiveEcho) drainResize() {
	for e.req.Resize != nil {
		select {
		case ws, ok := <-e.req.Resize:
			if !ok {
				e.req.Resize = nil
				return
			}
			e.lastWS, e.sawWS = ws, true
		default:
			return
		}
	}
}
