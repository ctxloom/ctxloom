package backends

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	pb "github.com/benjaminabbitt/scm/internal/lm/grpc"
)

// Mock implements the Backend interface for testing purposes.
// It echoes back prompts and context without calling any external AI service.
//
// NOTE: This is a test/development backend only - not intended for production use.
//
// Environment variables for test control:
//   - SCM_MOCK_RESPONSE: Custom response text to output
//   - SCM_MOCK_EXIT_CODE: Exit code to return (default: 0)
//   - SCM_MOCK_RECORD_FILE: File to write received input to for verification
type Mock struct {
	BinaryPath string
	Args       []string
	Env        map[string]string
}

// NewMock creates a new Mock backend.
func NewMock() *Mock {
	return &Mock{
		Args: []string{},
		Env:  make(map[string]string),
	}
}

// Name returns the backend identifier.
func (b *Mock) Name() string {
	return "mock"
}

// Version returns the backend version.
func (b *Mock) Version() string {
	return "1.0.0"
}

// SupportedModes returns the execution modes this backend supports.
func (b *Mock) SupportedModes() []pb.ExecutionMode {
	return []pb.ExecutionMode{pb.ExecutionMode_INTERACTIVE, pb.ExecutionMode_ONESHOT}
}

// Run executes the mock backend with the given request.
// It echoes back information about the request for testing purposes.
func (b *Mock) Run(ctx context.Context, req *pb.RunRequest, stdout, stderr io.Writer) (int32, *pb.ModelInfo, error) {
	opts := req.GetOptions()
	if opts == nil {
		opts = &pb.RunOptions{}
	}

	// Build model info
	modelInfo := &pb.ModelInfo{
		ModelName: "mock-model",
		Provider:  "mock",
	}

	// Check for record file in environment (from options or os env)
	// Note: config parser lowercases keys, so check both forms
	recordFile := getEnvFromOpts(opts, "SCM_MOCK_RECORD_FILE")

	// Assemble context from fragments
	context := AssembleContext(req.Fragments)
	promptContent := ""
	if req.Prompt != nil {
		promptContent = req.Prompt.Content
	}

	// Record input if requested
	if recordFile != "" {
		var input strings.Builder
		input.WriteString("=== Arguments ===\n")
		input.WriteString(fmt.Sprintf("mode=%s\n", opts.Mode.String()))
		input.WriteString(fmt.Sprintf("fragments=%d\n", len(req.Fragments)))
		input.WriteString("=== Context ===\n")
		input.WriteString(context)
		input.WriteString("\n=== Prompt ===\n")
		input.WriteString(promptContent)
		input.WriteString("\n")

		if err := os.WriteFile(recordFile, []byte(input.String()), 0644); err != nil {
			fmt.Fprintf(stderr, "warning: failed to write record file: %v\n", err)
		}
	}

	// Check for custom response
	customResponse := getEnvFromOpts(opts, "SCM_MOCK_RESPONSE")

	// Check for custom exit code
	exitCode := int32(0)
	exitCodeStr := getEnvFromOpts(opts, "SCM_MOCK_EXIT_CODE")
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
		response.WriteString(fmt.Sprintf("[mock] mode=%s\n", opts.Mode.String()))
		response.WriteString(fmt.Sprintf("[mock] fragments=%d\n", len(req.Fragments)))

		if context != "" {
			response.WriteString(fmt.Sprintf("[mock] context_length=%d\n", len(context)))
		}

		if promptContent != "" {
			response.WriteString(fmt.Sprintf("[mock] prompt=%s\n", promptContent))
		}

		// For distillation testing, return a compressed version
		if strings.Contains(context, "distill") || strings.Contains(context, "compress") {
			response.WriteString("[mock] distilled=Compressed content for testing\n")
		}
	}

	// Write response to stdout
	_, err := stdout.Write([]byte(response.String()))
	if err != nil {
		return 1, modelInfo, fmt.Errorf("failed to write response: %w", err)
	}

	return exitCode, modelInfo, nil
}

// getEnvFromOpts retrieves an environment variable from options or os.Environ.
// Handles case-insensitive lookup since config parser may lowercase keys.
func getEnvFromOpts(opts *pb.RunOptions, key string) string {
	if opts.Env != nil {
		// Try exact case first
		if v, ok := opts.Env[key]; ok {
			return v
		}
		// Try lowercase (config parser may lowercase keys)
		if v, ok := opts.Env[strings.ToLower(key)]; ok {
			return v
		}
	}
	// Fall back to os environment
	return os.Getenv(key)
}
