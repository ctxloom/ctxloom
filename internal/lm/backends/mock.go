package backends

import (
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

// recordMockInput writes the assembled request to recordFile when one is set
// (via CTXLOOM_MOCK_RECORD_FILE), warning to stderr on failure.
func recordMockInput(recordFile string, req *agent.ExecuteRequest, contextStr, promptContent string, fragmentCount int, stderr io.Writer) {
	if recordFile == "" {
		return
	}
	var input strings.Builder
	input.WriteString("=== Arguments ===\n")
	_, _ = fmt.Fprintf(&input, "mode=%d\n", req.Mode)
	_, _ = fmt.Fprintf(&input, "fragments=%d\n", fragmentCount)
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
	}
	if promptContent != "" {
		_, _ = fmt.Fprintf(&response, "[mock] prompt=%s\n", promptContent)
	}
	if strings.Contains(contextStr, "distill") || strings.Contains(contextStr, "compress") {
		response.WriteString("[mock] distilled=Compressed content for testing\n")
	}
	return response.String()
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
