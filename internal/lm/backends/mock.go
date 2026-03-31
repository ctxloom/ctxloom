package backends

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
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
	BaseBackend
	fragments []*Fragment
}

// NewMock creates a new Mock backend.
func NewMock() *Mock {
	return &Mock{
		BaseBackend: NewBaseBackend("mock", "1.0.0"),
	}
}

// Lifecycle returns nil - Mock doesn't support lifecycle hooks.
func (b *Mock) Lifecycle() LifecycleHandler { return nil }

// Skills returns nil - Mock doesn't support skills.
func (b *Mock) Skills() SkillRegistry { return nil }

// Context returns nil - Mock doesn't need a context provider.
func (b *Mock) Context() ContextProvider { return nil }

// MCP returns nil - Mock doesn't support MCP servers.
func (b *Mock) MCP() MCPManager { return nil }

// History returns nil - Mock doesn't support session history.
func (b *Mock) History() SessionHistory { return &NilSessionHistory{} }


// Setup prepares the backend for execution.
func (b *Mock) Setup(ctx context.Context, req *SetupRequest) error {
	b.SetWorkDir(req.WorkDir)
	b.fragments = req.Fragments
	return nil
}

// Execute runs the mock backend with the given request.
// It echoes back information about the request for testing purposes.
func (b *Mock) Execute(ctx context.Context, req *ExecuteRequest, stdout, stderr io.Writer) (*ExecuteResult, error) {
	// Build model info
	modelInfo := &ModelInfo{
		ModelName: "mock-model",
		Provider:  "mock",
	}

	// Check for record file in environment
	recordFile := getEnvFromMap(req.Env, "CTXLOOM_MOCK_RECORD_FILE")

	// Assemble context from fragments
	contextStr := AssembleContext(b.fragments)
	promptContent := GetPromptContent(req.Prompt)

	// Record input if requested
	if recordFile != "" {
		var input strings.Builder
		input.WriteString("=== Arguments ===\n")
		_, _ = fmt.Fprintf(&input, "mode=%d\n", req.Mode)
		_, _ = fmt.Fprintf(&input, "fragments=%d\n", len(b.fragments))
		input.WriteString("=== Context ===\n")
		input.WriteString(contextStr)
		input.WriteString("\n=== Prompt ===\n")
		input.WriteString(promptContent)
		input.WriteString("\n")

		if err := os.WriteFile(recordFile, []byte(input.String()), 0644); err != nil {
			_, _ = fmt.Fprintf(stderr, "warning: failed to write record file: %v\n", err)
		}
	}

	// Check for custom response
	customResponse := getEnvFromMap(req.Env, "CTXLOOM_MOCK_RESPONSE")

	// Check for custom exit code
	exitCode := int32(0)
	exitCodeStr := getEnvFromMap(req.Env, "CTXLOOM_MOCK_EXIT_CODE")
	if exitCodeStr != "" {
		if code, err := strconv.Atoi(exitCodeStr); err == nil {
			exitCode = int32(code)
		}
	}

	// Generate response
	var response strings.Builder

	if customResponse != "" {
		// Use custom response if provided
		response.WriteString(customResponse)
	} else {
		// Default echo behavior
		_, _ = fmt.Fprintf(&response, "[mock] mode=%d\n", req.Mode)
		_, _ = fmt.Fprintf(&response, "[mock] fragments=%d\n", len(b.fragments))

		if contextStr != "" {
			_, _ = fmt.Fprintf(&response, "[mock] context_length=%d\n", len(contextStr))
		}

		if promptContent != "" {
			_, _ = fmt.Fprintf(&response, "[mock] prompt=%s\n", promptContent)
		}

		// For distillation testing, return a compressed version
		if strings.Contains(contextStr, "distill") || strings.Contains(contextStr, "compress") {
			response.WriteString("[mock] distilled=Compressed content for testing\n")
		}
	}

	// Write response to stdout
	_, err := stdout.Write([]byte(response.String()))
	if err != nil {
		return &ExecuteResult{ExitCode: 1, ModelInfo: modelInfo}, fmt.Errorf("failed to write response: %w", err)
	}

	return &ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, nil
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
