package backends

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Mock implements the Backend interface for testing purposes.
// It echoes back prompts and context without calling any external AI service.
//
// NOTE: This is a test/development backend only - not intended for production use.
//
// Environment variables for test control:
//   - CTXLOOM_MOCK_RESPONSE: Custom response text to output
//   - CTXLOOM_MOCK_EXIT_CODE: Exit code to return (default: 0)
//   - CTXLOOM_MOCK_RECORD_FILE: File to write received input to for verification
type Mock struct {
	agent.BaseBackend
	fragments []*agent.Fragment
}

// MockConfig is the test backend's typed LLM config. Env carries the
// CTXLOOM_MOCK_* knobs (response, exit code, record file) through to Execute via
// the run request, mirroring the other backends' env passthrough.
type MockConfig struct {
	Model string            `mapstructure:"model"`
	Env   map[string]string `mapstructure:"env"`
}

// BackendType identifies the backend this config drives.
func (MockConfig) BackendType() string { return "mock" }

// NewMock creates a new Mock backend.
func NewMock() *Mock {
	return &Mock{
		BaseBackend: agent.NewBaseBackend("mock", "1.0.0"),
	}
}

// History returns nil - Mock doesn't support session history.
func (b *Mock) History() agent.SessionHistory { return &NilSessionHistory{} }

// Setup prepares the backend for execution.
func (b *Mock) Setup(ctx context.Context, req *agent.SetupRequest) error {
	b.SetWorkDir(req.WorkDir)
	b.fragments = req.Fragments
	return nil
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

	recordMockInput(getEnvFromMap(req.Env, "CTXLOOM_MOCK_RECORD_FILE"), req, contextStr, promptContent, len(b.fragments), stderr)

	customResponse := getEnvFromMap(req.Env, "CTXLOOM_MOCK_RESPONSE")
	response := buildMockResponse(customResponse, contextStr, promptContent, req.Mode, len(b.fragments))

	if _, err := stdout.Write([]byte(response)); err != nil {
		return &agent.ExecuteResult{ExitCode: 1, ModelInfo: modelInfo}, fmt.Errorf("failed to write response: %w", err)
	}

	return &agent.ExecuteResult{ExitCode: mockExitCode(req), ModelInfo: modelInfo}, nil
}

// configHomeEnvKeys are the per-agent config-home env vars isolation.EnvWorkspace
// threads into RunOptions.Env (see internal/lm/isolation.EnvWorkspace) — each
// engine's own global-config isolation knob.
var configHomeEnvKeys = []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "KIRO_HOME"}

// recordMockInput writes the assembled request to recordFile when one is set
// (via CTXLOOM_MOCK_RECORD_FILE), warning to stderr on failure.
//
// Records BOTH the process's actual cwd (os.Getwd) and req.WorkDir, plus
// whichever config-home env vars are set, so a hermetic test can prove WHERE
// the engine actually ran and WHAT isolation env it received.
//
// These are two DIFFERENT signals and only one of them moves with the
// isolation workspace axis. On the Host runtime, isolation.Prepare's Worktree
// policy never os.Chdir's the plugin subprocess itself (SpawnClient spawns
// `ctxloom llm serve <backend>` with no Cmd.Dir — see
// internal/lm/isolation/none.go / worktree.go's SpawnClient and
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
func recordMockInput(recordFile string, req *agent.ExecuteRequest, contextStr, promptContent string, fragmentCount int, stderr io.Writer) {
	if recordFile == "" {
		return
	}
	var input strings.Builder
	input.WriteString("=== Arguments ===\n")
	_, _ = fmt.Fprintf(&input, "mode=%d\n", req.Mode)
	_, _ = fmt.Fprintf(&input, "fragments=%d\n", fragmentCount)
	if cwd, err := os.Getwd(); err == nil {
		_, _ = fmt.Fprintf(&input, "cwd=%s\n", cwd)
	} else {
		_, _ = fmt.Fprintf(&input, "cwd=<error: %v>\n", err)
	}
	_, _ = fmt.Fprintf(&input, "workdir=%s\n", req.WorkDir)
	input.WriteString("=== Env ===\n")
	for _, key := range configHomeEnvKeys {
		if v := getEnvFromMap(req.Env, key); v != "" {
			_, _ = fmt.Fprintf(&input, "%s=%s\n", key, v)
		}
	}
	input.WriteString("=== Context ===\n")
	input.WriteString(contextStr)
	input.WriteString("\n=== Prompt ===\n")
	input.WriteString(promptContent)
	input.WriteString("\n")

	if err := os.WriteFile(recordFile, []byte(input.String()), 0644); err != nil {
		_, _ = fmt.Fprintf(stderr, "warning: failed to write record file: %v\n", err)
	}
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
func buildMockResponse(customResponse, contextStr, promptContent string, mode agent.ExecutionMode, fragmentCount int) string {
	if customResponse != "" {
		return customResponse
	}

	var response strings.Builder
	_, _ = fmt.Fprintf(&response, "[mock] mode=%d\n", mode)
	_, _ = fmt.Fprintf(&response, "[mock] fragments=%d\n", fragmentCount)

	if contextStr != "" {
		_, _ = fmt.Fprintf(&response, "[mock] context_length=%d\n", len(contextStr))
		// The doc comment above ("echoes back prompts and context") promised
		// the CONTENT, not just its length — before this line, a hermetic
		// caller could prove a fragment was ASSEMBLED (the length changed) but
		// never that its actual guidance reached the child's own output. J17
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

	r := bufio.NewReader(req.Stdin)
	for {
		line, err := r.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line == "quit" {
			break
		}
		if line != "" {
			drainResize()
			_, _ = fmt.Fprintf(stdout, "mock echo: %s\n", line)
			if sawWS {
				_, _ = fmt.Fprintf(stdout, "mock winsize: %dx%d\n", lastWS.Rows, lastWS.Cols)
			}
		}
		if err != nil || ctx.Err() != nil {
			break
		}
	}

	return &agent.ExecuteResult{ExitCode: mockExitCode(req), ModelInfo: modelInfo}, nil
}

// Cleanup releases resources after execution.
func (b *Mock) Cleanup(ctx context.Context) error { return nil }

// getEnvFromMap retrieves an environment variable from a map or os.Environ.
// Handles case-insensitive lookup since config parser may lowercase keys.
func getEnvFromMap(env map[string]string, key string) string {
	if env != nil {
		// Try exact case first
		if v, ok := env[key]; ok {
			return v
		}
		// Try lowercase (config parser may lowercase keys)
		if v, ok := env[strings.ToLower(key)]; ok {
			return v
		}
	}
	// Fall back to os environment
	return os.Getenv(key)
}
