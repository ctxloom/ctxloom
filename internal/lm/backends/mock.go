package backends

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/containerprobe"
)

// Mock implements the Backend interface for testing purposes.
// It echoes back prompts and context without calling any external AI service.
//
// NOTE: This is a test/development backend only - not intended for production use.
//
// It embeds agent.LaunchBackend (not the bare agent.BaseBackend) for ONE
// reason: so a live turn's Setup runs the SAME surfaces × typed-cells delivery
// every real launch backend runs (agent.LaunchBackend.setupViaCells), rather
// than a mock-only bypass. Before that, Mock.Setup stashed its payload and
// returned nil, so BuildSurfaces("mock", …) was never called on the launch
// path and a live `ctxloom run --backend mock` materialized nothing — which
// made every hermetic delivery assertion either live-engine-dependent or
// vacuous (docs/design/engine-delivery-seam.design.md).
//
// Execute's echo and record file are UNCHANGED by that: Setup still stashes
// fragments/managed before delegating, and delivery is additive to the echo,
// never a replacement for it.
//
// Environment variables for test control — they reach Execute through the run
// request's env, which a label's `mock_control:` map feeds (MockConfig):
//   - CTXLOOM_MOCK_RESPONSE: Custom response text to output. SET TO EMPTY is a
//     request for an empty reply, and is distinct from leaving it unset
//   - CTXLOOM_MOCK_EXIT_CODE: Exit code to return (default: 0)
//   - CTXLOOM_MOCK_RECORD_FILE: File to write received input to for verification
//   - CTXLOOM_MOCK_FAIL_PREFIX: "1" prefixes the response with MockFailPrefix,
//     leaving the rest of the response INTACT (see MockFailPrefix)
type Mock struct {
	agent.LaunchBackend
	fragments []*agent.Fragment
	// managed is the host-assembled setup payload from the last Setup call —
	// stashed so Execute's recordMockInput can prove fields like DenyTools/
	// Skills actually survived the wire (the launch-flow regression guard).
	// nil is a legitimate value (the minimal/distill form sends none).
	managed *agent.ManagedConfig
}

// MockFailPrefix marks a response as the failure outcome WITHOUT discarding the
// evidence of what the engine actually observed. It is deliberately additive: a
// failure that replaced the response with a constant would render identically
// whether or not ctxloom delivered anything, and the mock's class gate
// (internal/mockengine/arch_test.go) forbids exactly that — "a limb that renders
// identically either way is not evidence". Prefixing instead of replacing is
// what lets a NEGATIVE scenario assert positively: the run can only produce
// "FAIL" followed by the observed context if the engine was actually reached and
// the value actually flowed, where asserting the ABSENCE of something is
// satisfied just as well by an engine that never launched.
//
// internal/mockengine references this constant rather than re-typing it, so the
// two mock halves cannot drift to different markers.
const MockFailPrefix = "FAIL"

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
// The doubles differ ONLY in that name and in what their descriptors declare,
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

// Setup stashes the payload Execute's echo and record file are built from, then
// runs the shared launch Setup so the context surface is actually delivered.
//
// The stash comes FIRST and unconditionally: recordMockInput and
// buildMockResponse read b.fragments/b.managed, and a great many hermetic
// scenarios assert on those bytes. A delivery failure is warned by the caller
// (grpc.runTurnSetup) and the turn proceeds to Execute, so the echo must
// already hold its payload by then — returning early on the delegate's error
// would silently empty the echo, which is precisely the silent no-op this
// backend exists to catch in others.
func (b *Mock) Setup(ctx context.Context, req *agent.SetupRequest) error {
	b.fragments = req.Fragments
	b.managed = req.Managed
	return b.LaunchBackend.Setup(ctx, req)
}

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
// DERIVED from the descriptors' Home declarations so the roster cannot miss
// an engine that declared one. mock records whichever of these are set so a
// hermetic test can prove what config-home env the engine received.
func ConfigHomeEnvKeys() []string {
	var keys []string
	seen := map[string]bool{}
	for _, name := range List() {
		home, ok := descriptors[name].Home.Get()
		if !ok {
			continue
		}
		for _, v := range home.Vars {
			if !seen[v.EnvVar] {
				seen[v.EnvVar] = true
				keys = append(keys, v.EnvVar)
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
	return writeMockRecord(recordFile, mockRecordFields{
		Mode:          int32(req.Mode),
		WorkDir:       req.WorkDir,
		Env:           req.Env,
		Context:       contextStr,
		Prompt:        promptContent,
		FragmentCount: fragmentCount,
	}, managed)
}

// mockRecordFields is what a record is written FROM, named independently of
// which request type produced it. Execute is handed an agent.ExecuteRequest and
// Chat an agent.ChatRequest; both must be able to leave the same evidence,
// because a scenario asserting WHERE an engine ran must not first have to know
// which transport arm the run happened to take.
type mockRecordFields struct {
	Mode          int32
	WorkDir       string
	Env           map[string]string
	Context       string
	Prompt        string
	FragmentCount int
}

// writeMockRecord renders one record. See recordMockInput for what the fields
// mean and why cwd and workdir are both present.
func writeMockRecord(recordFile string, in mockRecordFields, managed *agent.ManagedConfig) error {
	if recordFile == "" {
		return nil
	}
	var input strings.Builder
	input.WriteString("=== Arguments ===\n")
	_, _ = fmt.Fprintf(&input, "mode=%d\n", in.Mode)
	_, _ = fmt.Fprintf(&input, "fragments=%d\n", in.FragmentCount)
	if cwd, err := os.Getwd(); err == nil {
		_, _ = fmt.Fprintf(&input, "cwd=%s\n", cwd)
	} else {
		_, _ = fmt.Fprintf(&input, "cwd=<error: %v>\n", err)
	}
	_, _ = fmt.Fprintf(&input, "workdir=%s\n", in.WorkDir)
	// WHERE THE ENGINE RAN, in two independent signals, because neither is
	// sufficient alone. container_markers is a heuristic that reads TRUE on
	// both sides when the test harness itself runs inside a devcontainer — a
	// scenario trusting it alone would then pass without any container being
	// launched. hostname is what breaks that tie: a container gets its own UTS
	// namespace regardless of how paths are mapped, so it never matches the
	// launching process's hostname, nested or not. cwd and workdir cannot
	// serve here: whether they agree between host and container is a
	// property of the runtime's pathMapper seam (internal/adapters/isolation/
	// runtime.go) — identical-path is only that seam's default
	// configuration, not a guaranteed contract — so a signal built on their
	// equality would break under a non-identity mapper.
	if host, err := os.Hostname(); err == nil {
		_, _ = fmt.Fprintf(&input, "hostname=%s\n", host)
	} else {
		_, _ = fmt.Fprintf(&input, "hostname=<error: %v>\n", err)
	}
	_, _ = fmt.Fprintf(&input, "container_markers=%s\n", strings.Join(containerprobe.Markers(), ","))
	input.WriteString("=== Env ===\n")
	for _, key := range ConfigHomeEnvKeys() {
		if v := getEnvFromMap(in.Env, key); v != "" {
			_, _ = fmt.Fprintf(&input, "%s=%s\n", key, v)
		}
	}
	// Managed setup payload — proves the launch-flow wire actually carried
	// these fields (deny_tools/skills were silently dropped here; see
	// TestArch_ProtoConverters_MirrorEveryStructField in internal/lm/grpc).
	// Recorded even when empty, so a scenario asserting ABSENCE is possible too.
	input.WriteString("=== DenyTools ===\n")
	if managed != nil {
		for _, t := range managed.DenyTools {
			_, _ = fmt.Fprintf(&input, "%s\n", t)
		}
	}
	input.WriteString("=== Skills ===\n")
	if managed != nil {
		for _, s := range managed.Skills {
			_, _ = fmt.Fprintf(&input, "%s\n", s.Name)
		}
	}
	input.WriteString("=== Context ===\n")
	input.WriteString(in.Context)
	input.WriteString("\n=== Prompt ===\n")
	input.WriteString(in.Prompt)
	input.WriteString("\n")

	if err := os.WriteFile(recordFile, []byte(input.String()), 0644); err != nil {
		return fmt.Errorf("failed to write mock record file %q: %w", recordFile, err)
	}
	return nil
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

// buildMockResponse returns the custom response when provided, else the default
// echo of mode/fragments/context/prompt (plus a distilled marker for distill or
// compress contexts).
//
// failPrefix prepends MockFailPrefix to WHATEVER response is produced, custom or
// echo. It is orthogonal to CTXLOOM_MOCK_RESPONSE on purpose: that knob REPLACES
// the response, so a test using it to signal failure can only assert a literal it
// wrote itself, which proves nothing about what reached the child.
func buildMockResponse(customResponse string, hasCustomResponse bool, contextStr, promptContent string, mode agent.ExecutionMode, fragmentCount int, failPrefix bool) string {
	var response strings.Builder
	if failPrefix {
		response.WriteString(MockFailPrefix + "\n")
	}
	// SET, not non-empty: an override of "" is a deliberate request for an empty
	// reply, and treating it as absent would silently answer with the echo
	// instead — the caller asking to prove a zero-byte reply gets a
	// several-hundred-byte one and the test passes for the wrong reason.
	if hasCustomResponse {
		response.WriteString(customResponse)
		return response.String()
	}

	_, _ = fmt.Fprintf(&response, "[mock] mode=%d\n", mode)
	_, _ = fmt.Fprintf(&response, "[mock] fragments=%d\n", fragmentCount)

	if contextStr != "" {
		_, _ = fmt.Fprintf(&response, "[mock] context_length=%d\n", len(contextStr))
		// The doc comment above ("echoes back prompts and context") promised
		// the CONTENT, not just its length — before this line, a hermetic
		// caller could prove a fragment was ASSEMBLED (the length changed) but
		// never that its actual guidance reached the child's own output. J002300
		// (cross-engine delegation) needs exactly that: two children with
		// different composed profiles must each emit evidence, in their OWN
		// stdout, of guidance present in their OWN context and absent from a
		// sibling's — a length number cannot carry that, verbatim text can.
		_, _ = fmt.Fprintf(&response, "[mock] context=%s\n", contextStr)
	}
	if promptContent != "" {
		_, _ = fmt.Fprintf(&response, "[mock] prompt=%s\n", promptContent)
	}
	if strings.Contains(contextStr, "distill") || strings.Contains(contextStr, "compress") {
		response.WriteString("[mock] distilled=Compressed content for testing\n")
	}
	return response.String()
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

// getEnvFromMap retrieves an environment variable from a map or os.Environ.
// Handles case-insensitive lookup since config parser may lowercase keys.
//
// It DISCARDS the found/not-found bit, so it may only be used for knobs where
// an empty value and an unset one mean the same thing. Where they differ, use
// lookupEnvFromMap.
func getEnvFromMap(env map[string]string, key string) string {
	v, _ := lookupEnvFromMap(env, key)
	return v
}

// lookupEnvFromMap is getEnvFromMap's TWO-VALUE form: it reports whether the
// knob was SET, which an empty string cannot.
//
// CTXLOOM_MOCK_RESPONSE="" is a request for an EMPTY engine reply, and a
// zero-byte reply is the exact shape this project's characteristic bug
// produces — exit 0, a success message, nothing written. A mock that cannot be
// asked for one cannot be used to prove ctxloom surfaces it rather than
// papering over it, so "set to empty" and "unset" have to be distinguishable.
// internal/mockengine's Dispatch takes a two-value reader for the same reason.
//
// The lookup ORDER lives here alone (exact key, then the lowercase the config
// parser may produce, then the process environment) rather than being written
// twice and drifting.
func lookupEnvFromMap(env map[string]string, key string) (string, bool) {
	if env != nil {
		if v, ok := env[key]; ok {
			return v, true
		}
		if v, ok := env[strings.ToLower(key)]; ok {
			return v, true
		}
	}
	return os.LookupEnv(key)
}
