package backends

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	mockengine "github.com/ctxloom/ctxloom/internal/engines/mock"
)

// Mock implements the Backend interface for testing purposes.
// It echoes back prompts and context without calling any external AI service.
//
// NOTE: This is a test/development backend only - not intended for production use.
//
// It embeds agent.LaunchBackend for the shared Execute tail. fragments and
// managed are what a launch delivered to this backend, which Execute's echo
// and record file report; the runner's delivery hands none through this
// type, so both are empty on a live turn.
type Mock struct {
	agent.LaunchBackend
	fragments []*agent.Fragment
	// managed is the host-assembled setup payload, which Execute's
	// recordMockInput reports. nil is a legitimate value.
	managed *agent.ManagedConfig
}

// MockFailPrefix is the engine kind's FailPrefix (internal/engines/mock):
// one marker for every arm of the mock.
const MockFailPrefix = mockengine.FailPrefix

// MockConfig is the test backend's typed LLM config. Control carries the
// CTXLOOM_MOCK_* knobs (response, exit code, record file) through to Execute
// via the run request's env. It is TEST CONTROL, not credentials, and its key
// says so: `mock_control`, not the retired `env` (config.RetiredLLMEnvKey) —
// no real engine carries an environment map in its config, so the mock is
// the only label body with one, and it must not read as the place a
// credential goes.
type MockConfig struct {
	Model   string            `mapstructure:"model"`
	Control map[string]string `mapstructure:"mock_control"`
}

// BackendType identifies the backend this config drives.
func (MockConfig) BackendType() string { return config.BackendMock }

// MockLossyConfig is the deliberately-lossy double's config. It carries the
// same fields as MockConfig and exists only so the config NAMES ITS OWN
// BACKEND: TestDescriptorTable_ConfigDecodesToItsOwnType requires every
// descriptor's decoded config to report the backend it was registered under,
// and a shared type would have reported "mock" for both — the exact
// two-things-one-name confusion the registry invariant is there to prevent.
type MockLossyConfig struct {
	Model   string            `mapstructure:"model"`
	Control map[string]string `mapstructure:"mock_control"`
}

// MockNoSkillsConfig is the no-skills double's config body.
type MockNoSkillsConfig struct {
	Model   string            `mapstructure:"model"`
	Control map[string]string `mapstructure:"mock_control"`
}

// BackendType identifies the backend this config drives.
func (MockNoSkillsConfig) BackendType() string { return config.BackendMockNoSkills }

// MockControl returns the labeled entry's test-control map, the same way
// MockConfig does.
func (c MockNoSkillsConfig) MockControl() map[string]string { return c.Control }

// BackendType identifies the backend this config drives.
func (MockLossyConfig) BackendType() string { return config.BackendMockLossy }

// MockControl returns the labeled entry's test-control map, the same way
// MockConfig does.
func (c MockLossyConfig) MockControl() map[string]string { return c.Control }

// MockLaunchConfig is the launch-delivered double's config. It carries the same
// fields as MockConfig and exists for the same reason MockLossyConfig does: the
// config must NAME ITS OWN BACKEND, because
// TestDescriptorTable_ConfigDecodesToItsOwnType requires every descriptor's
// decoded config to report the backend it was registered under, and a shared
// type would report "mock" for all three.
type MockLaunchConfig struct {
	Model   string            `mapstructure:"model"`
	Control map[string]string `mapstructure:"mock_control"`
}

// BackendType identifies the backend this config drives.
func (MockLaunchConfig) BackendType() string { return config.BackendMockLaunch }

// MockControl returns the labeled entry's test-control map, the same way
// MockConfig does.
func (c MockLaunchConfig) MockControl() map[string]string { return c.Control }

// MockControl returns the labeled entry's test-control map. Shared code (see
// operations.MockControlFor) reaches it through an interface assertion rather
// than a concrete-type switch: internal/adapters/operations must not branch on a
// backend's identity (ADR-0026), and the four mock doubles each carry their
// own config type, so one structural accessor serves them all.
func (c MockConfig) MockControl() map[string]string { return c.Control }

// NewMock creates a new Mock backend.
//
// The InitLaunch wiring is deliberately the plainest one in the tree: mock's
// own Declaration (mock_surfaces.go). Its context rides MOCK_CONTEXT.md as
// its own native well-known file — mock declares no hook-carried context, so
// no SessionStart injection hook is ever installed for it. History is the
// same NilSessionHistory the backend has always reported — mock keeps no
// transcripts.
// NewMockLossy builds the deliberately-lossy sibling of the mock backend. It
// is mock in every respect but its registered NAME and the hook kind its
// descriptor declares unsupported — see config.BackendMockLossy for why a
// second double beats making the first one imperfect.
func NewMockLossy() *Mock { return newMockBackend(config.BackendMockLossy) }

// NewMockLaunch builds the launch-delivered double. See config.BackendMockLaunch.
func NewMockLaunch() *Mock { return newMockBackend(config.BackendMockLaunch) }

// NewMockNoSkills builds the double with no skills surface. See
// config.BackendMockNoSkills.
func NewMockNoSkills() *Mock { return newMockBackend(config.BackendMockNoSkills) }

// newMockBackend builds a mock-family backend under the given registry name.
// The doubles differ ONLY in that name and in what their records declare,
// so they share one constructor rather than a body each that could drift into
// behaving differently.
//
// Every one of them gets the COMPLETE surface set here, mock-launch included,
// and that is not an oversight. This is the CELLS/LAUNCH path: a
// launch-delivered engine receives all five surfaces into its per-session home
// at launch — that is what "delivered at launch" means. Only the static
// MATERIALIZE path is narrowed, and that narrowing lives on the descriptor
// (newSurfaces -> NewMockLaunchSurfaces), not here. Wiring the narrow set on
// both paths would make the double an engine that simply LOSES four surfaces,
// which is the different fact mock-lossy already covers.
func newMockBackend(name string) *Mock {
	b := &Mock{}
	b.BaseBackend = agent.NewBaseBackend(name, "1.0.0")
	b.InitLaunch(
		agent.NewBaseLifecycle(name),
		agent.NewBaseContextProvider(),
		&NilSessionHistory{},
		mockDeclaration(name),
	)
	return b
}

func NewMock() *Mock { return newMockBackend(config.BackendMock) }

// Execute runs the mock backend with the given request.
// It echoes back information about the request for testing purposes.
func (b *Mock) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
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
	if getEnvFromMap(req.Env, "CTXLOOM_MOCK_ECHO_STDIN") == "1" && req.Stdin != nil {
		return b.executeInteractiveEcho(ctx, req, stdout, modelInfo)
	}

	// Assemble context from fragments
	contextStr := agent.AssembleContext(b.fragments)
	promptContent := agent.GetPromptContent(req.Prompt)

	if err := recordMockInput(getEnvFromMap(req.Env, "CTXLOOM_MOCK_RECORD_FILE"), req, b.managed, contextStr, promptContent, len(b.fragments)); err != nil {
		// A record-file write failure used to only warn to stderr and
		// return nothing, so Execute reported success with no record
		// file — a hermetic test asserting against it would then silently
		// read a STALE file from a previous run instead of failing loudly.
		return &agent.ExecuteResult{ExitCode: 1, ModelInfo: modelInfo}, err
	}

	customResponse, hasCustomResponse := lookupEnvFromMap(req.Env, "CTXLOOM_MOCK_RESPONSE")
	failPrefix := getEnvFromMap(req.Env, "CTXLOOM_MOCK_FAIL_PREFIX") == "1"
	response := buildMockResponse(customResponse, hasCustomResponse, contextStr, promptContent, req.Mode, len(b.fragments), failPrefix)

	if _, err := stdout.Write([]byte(response)); err != nil {
		return &agent.ExecuteResult{ExitCode: 1, ModelInfo: modelInfo}, fmt.Errorf("failed to write response: %w", err)
	}

	return &agent.ExecuteResult{ExitCode: mockExitCode(req), ModelInfo: modelInfo}, nil
}

// ConfigHomeEnvKeys returns the config-home env vars a run can thread into
// RunOptions.Env — each registered engine's own home-relocation var(s),
// DERIVED from the kinds' Home declarations so the roster cannot miss an
// engine that declared one. mock records whichever of these are set so a
// hermetic test can prove what config-home env the engine received.
func ConfigHomeEnvKeys() []string {
	var keys []string
	seen := map[string]bool{}
	for _, name := range List() {
		for _, v := range records[name].kind.Home().Vars {
			if !seen[v.Name] {
				seen[v.Name] = true
				keys = append(keys, v.Name)
			}
		}
	}
	return keys
}

// recordMockInput writes the assembled request to recordFile when one is set
// (via CTXLOOM_MOCK_RECORD_FILE), returning the write error (if any) so the
// caller can fail loudly instead of reporting success with no record file.
//
// Records BOTH the process's actual cwd (os.Getwd) and req.WorkDir, plus
// whichever config-home env vars are set, so a hermetic test can prove WHERE
// the engine actually ran and WHAT isolation env it received.
//
// These are two DIFFERENT signals and only one of them moves with the
// isolation workspace axis. On the Host runtime, isolation.Prepare's Worktree
// policy never os.Chdir's the plugin subprocess itself (SpawnClient spawns
// `ctxloom llm serve <backend>` with no Cmd.Dir — see
// internal/adapters/isolation/none.go / worktree.go's SpawnClient and
// internal/lm/grpc/client.go's dialLLMConnection) — real engines honor
// isolation by having THEIR OWN Execute spawn a grandchild process with
// Cmd.Dir = req.WorkDir (agent.ExecuteRequest.WorkDir's own doc: "the passed
// workspace always reaches the child instead of defaulting to the plugin's
// inherited '.'"). Mock never spawns a grandchild, so os.Getwd() here is
// always the plugin subprocess's OWN inherited cwd — identical across every
// workspace axis (confirmed live: a --workspace worktree run's mock record
// showed the SAME cwd as --workspace none). req.WorkDir is the actually
// resolved isolation workspace and is what a hermetic test must read to
// observe the workspace boundary; cwd is kept alongside it for diagnostics.
func recordMockInput(recordFile string, req *agent.ExecuteRequest, managed *agent.ManagedConfig, contextStr, promptContent string, fragmentCount int) error {
	rec := mockengine.Record{
		Mode:          int32(req.Mode),
		WorkDir:       req.WorkDir,
		Env:           req.Env,
		Context:       contextStr,
		Prompt:        promptContent,
		FragmentCount: fragmentCount,
		HomeEnvKeys:   ConfigHomeEnvKeys(),
	}
	if managed != nil {
		rec.DenyTools = managed.DenyTools
		for _, sk := range managed.Skills {
			rec.Skills = append(rec.Skills, sk.Name)
		}
	}
	return mockengine.WriteRecord(recordFile, rec)
}

// mockExitCode returns the exit code from CTXLOOM_MOCK_EXIT_CODE, or 0.
func mockExitCode(req *agent.ExecuteRequest) int32 {
	exitCodeStr := getEnvFromMap(req.Env, "CTXLOOM_MOCK_EXIT_CODE")
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
	return mockengine.Response(customResponse, hasCustomResponse, contextStr, promptContent, int32(mode), fragmentCount, failPrefix)
}

// executeInteractiveEcho reflects each typed line and the latest terminal
// resize it has observed — the interactive engine behavior the docker-exec
// turn's integration test round-trips through the full pty chain. It loops
// (echoing `mock echo: <line>` plus, once a resize has been seen, `mock
// winsize: RxC`) until a "quit" line or EOF: a tty rarely EOFs, so the sentinel
// is what lets a test drive a resize BETWEEN two lines (giving the daemon's
// SIGWINCH time to propagate) and then end the turn deterministically.
func (b *Mock) executeInteractiveEcho(ctx context.Context, req *agent.ExecuteRequest, stdout io.Writer, modelInfo *agent.ModelInfo) (*agent.ExecuteResult, error) {
	var lastWS agent.WindowSize
	var sawWS bool
	// drainResize picks up every resize delivered so far without blocking — a
	// resize sent BETWEEN two lines (the integration test's pattern) is on the
	// channel by the time the next line's iteration drains it.
	drainResize := func() {
		if req.Resize == nil {
			return
		}
		for {
			select {
			case ws, ok := <-req.Resize:
				if !ok {
					req.Resize = nil
					return
				}
				lastWS, sawWS = ws, true
			default:
				return
			}
		}
	}

	// ReadString blocks until a line arrives, so a cancelled ctx
	// checked only AFTER it returns can never interrupt a stdin that never
	// produces a line (a pty rarely EOFs — see the doc above). Reading on a
	// goroutine and selecting on ctx.Done() lets THIS function return
	// promptly on cancellation; the goroutine itself may still leak until the
	// peer writes or closes, which is the pre-existing, documented tradeoff
	// (the "quit"/EOF sentinel), not something fixable from this side alone.
	type readResult struct {
		line string
		err  error
	}
	lines := make(chan readResult)
	go func() {
		r := bufio.NewReader(req.Stdin)
		for {
			line, err := r.ReadString('\n')
			lines <- readResult{line: line, err: err}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return &agent.ExecuteResult{ExitCode: mockExitCode(req), ModelInfo: modelInfo}, nil
		case res := <-lines:
			line := strings.TrimRight(res.line, "\r\n")
			if line == "quit" {
				return &agent.ExecuteResult{ExitCode: mockExitCode(req), ModelInfo: modelInfo}, nil
			}
			if line != "" {
				drainResize()
				_, _ = fmt.Fprintf(stdout, "mock echo: %s\n", line)
				if sawWS {
					_, _ = fmt.Fprintf(stdout, "mock winsize: %dx%d\n", lastWS.Rows, lastWS.Cols)
				}
			}
			if res.err != nil {
				return &agent.ExecuteResult{ExitCode: mockExitCode(req), ModelInfo: modelInfo}, nil
			}
		}
	}
}

// getEnvFromMap and lookupEnvFromMap are the engine kind's knob readers
// (internal/engines/mock.Env, LookupEnv): the lookup ORDER lives there alone.
func getEnvFromMap(env map[string]string, key string) string {
	return mockengine.Env(env, key)
}

func lookupEnvFromMap(env map[string]string, key string) (string, bool) {
	return mockengine.LookupEnv(env, key)
}
