package grpc

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
)

// =============================================================================
// Shared Configuration Tests
//
// These tests verify that the plugin handshake and mapping configuration
// is set up correctly for plugin communication.
// =============================================================================

// TestHandshakeConfig_HasRequiredFields verifies that the handshake
// configuration contains all required fields for go-plugin compatibility.
// An incorrect handshake prevents plugins from connecting.
func TestHandshakeConfig_HasRequiredFields(t *testing.T) {
	assert.Equal(t, uint(1), HandshakeConfig.ProtocolVersion)
	assert.Equal(t, "CTXLOOM_PLUGIN", HandshakeConfig.MagicCookieKey)
	assert.Equal(t, "ai-backend-v1", HandshakeConfig.MagicCookieValue)
}

// TestPluginMap_ContainsAIPlugin verifies that the plugin map contains
// the expected AI plugin entry. This is required for plugin dispensing.
func TestPluginMap_ContainsAIPlugin(t *testing.T) {
	assert.Contains(t, PluginMap, PluginName)
	assert.Equal(t, "ai_plugin", PluginName)
}

// =============================================================================
// verbosityToHclogLevel Tests
//
// These tests verify the mapping between ctxloom verbosity levels and hclog
// logging levels. Correct mapping ensures appropriate log output.
// =============================================================================

// TestVerbosityToHclogLevel_AllLevels verifies that each verbosity level
// maps to the expected hclog level. This controls plugin logging output.
func TestVerbosityToHclogLevel_AllLevels(t *testing.T) {
	tests := []struct {
		name      string
		verbosity int
		want      hclog.Level
	}{
		{
			name:      "verbosity 0 maps to Error (quiet mode)",
			verbosity: 0,
			want:      hclog.Error,
		},
		{
			name:      "verbosity 1 maps to Info",
			verbosity: 1,
			want:      hclog.Info,
		},
		{
			name:      "verbosity 2 maps to Debug",
			verbosity: 2,
			want:      hclog.Debug,
		},
		{
			name:      "verbosity 3 maps to Trace",
			verbosity: 3,
			want:      hclog.Trace,
		},
		{
			name:      "verbosity 4+ also maps to Trace (max level)",
			verbosity: 10,
			want:      hclog.Trace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := verbosityToHclogLevel(tt.verbosity)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestVerbosityToHclogLevel_NegativeVerbosity verifies that negative
// verbosity values are treated as 0 (quiet mode). This handles edge
// cases from misconfiguration.
func TestVerbosityToHclogLevel_NegativeVerbosity(t *testing.T) {
	got := verbosityToHclogLevel(-1)
	assert.Equal(t, hclog.Error, got)
}

// =============================================================================
// Fragment Conversion Tests
//
// These tests verify correct conversion between protobuf and backend
// fragment types. Data integrity during conversion is critical.
// =============================================================================

// TestConvertFragment_NilInput verifies that nil fragment input returns
// nil output without panicking. This handles optional fragment fields.
func TestConvertFragment_NilInput(t *testing.T) {
	result := convertFragment(nil)
	assert.Nil(t, result)
}

// TestConvertFragment_FullyPopulated verifies that all fragment fields
// are correctly copied during conversion. No data should be lost.
func TestConvertFragment_FullyPopulated(t *testing.T) {
	proto := &Fragment{
		Name:        "test-fragment",
		Version:     "1.0.0",
		Tags:        []string{"golang", "testing"},
		Content:     "# Test Content\n\nSome documentation here.",
		IsDistilled: true,
		DistilledBy: "gpt-4",
	}

	result := convertFragment(proto)

	assert.NotNil(t, result)
	assert.Equal(t, "test-fragment", result.Name)
	assert.Equal(t, "1.0.0", result.Version)
	assert.Equal(t, []string{"golang", "testing"}, result.Tags)
	assert.Equal(t, "# Test Content\n\nSome documentation here.", result.Content)
	assert.True(t, result.IsDistilled)
	assert.Equal(t, "gpt-4", result.DistilledBy)
}

// TestConvertFragment_EmptyFields verifies that empty strings and nil
// slices are handled correctly without corruption.
func TestConvertFragment_EmptyFields(t *testing.T) {
	proto := &Fragment{
		Name:    "",
		Version: "",
		Tags:    nil,
		Content: "",
	}

	result := convertFragment(proto)

	assert.NotNil(t, result)
	assert.Empty(t, result.Name)
	assert.Empty(t, result.Version)
	assert.Nil(t, result.Tags)
	assert.Empty(t, result.Content)
}

// TestConvertFragments_NilSlice verifies that nil slice input returns
// nil output. This handles cases where no fragments are provided.
func TestConvertFragments_NilSlice(t *testing.T) {
	result := convertFragments(nil)
	assert.Nil(t, result)
}

// TestConvertFragments_EmptySlice verifies that empty slice returns
// empty slice, not nil. This preserves the distinction between
// "no fragments" and "explicitly empty list".
func TestConvertFragments_EmptySlice(t *testing.T) {
	result := convertFragments([]*Fragment{})
	assert.NotNil(t, result)
	assert.Empty(t, result)
}

// TestConvertFragments_MultipleFragments verifies that multiple fragments
// are all converted correctly with correct ordering preserved.
func TestConvertFragments_MultipleFragments(t *testing.T) {
	protos := []*Fragment{
		{Name: "first", Content: "content 1"},
		{Name: "second", Content: "content 2"},
		{Name: "third", Content: "content 3"},
	}

	results := convertFragments(protos)

	assert.Len(t, results, 3)
	assert.Equal(t, "first", results[0].Name)
	assert.Equal(t, "second", results[1].Name)
	assert.Equal(t, "third", results[2].Name)
}

// TestConvertFragments_WithNilElements verifies that nil elements within
// the slice are converted to nil backend fragments, preserving position.
func TestConvertFragments_WithNilElements(t *testing.T) {
	protos := []*Fragment{
		{Name: "first"},
		nil,
		{Name: "third"},
	}

	results := convertFragments(protos)

	assert.Len(t, results, 3)
	assert.Equal(t, "first", results[0].Name)
	assert.Nil(t, results[1])
	assert.Equal(t, "third", results[2].Name)
}

// =============================================================================
// ModelInfo Conversion Tests
//
// These tests verify correct conversion of model metadata from backend
// types to protobuf types for transmission over gRPC.
// =============================================================================

// TestConvertModelInfoToProto_NilInput verifies that nil model info
// returns nil without panicking. Model info is optional in responses.
func TestConvertModelInfoToProto_NilInput(t *testing.T) {
	result := convertModelInfoToProto(nil)
	assert.Nil(t, result)
}

// TestConvertModelInfoToProto_FullyPopulated verifies that all model
// metadata fields are correctly converted for transmission.
func TestConvertModelInfoToProto_FullyPopulated(t *testing.T) {
	backend := &backends.ModelInfo{
		ModelName:    "claude-3-opus",
		ModelVersion: "20240229",
		Provider:     "anthropic",
	}

	result := convertModelInfoToProto(backend)

	assert.NotNil(t, result)
	assert.Equal(t, "claude-3-opus", result.ModelName)
	assert.Equal(t, "20240229", result.ModelVersion)
	assert.Equal(t, "anthropic", result.Provider)
}

// TestConvertModelInfoToProto_EmptyFields verifies that empty strings
// are preserved during conversion, not converted to nil or defaults.
func TestConvertModelInfoToProto_EmptyFields(t *testing.T) {
	backend := &backends.ModelInfo{
		ModelName:    "",
		ModelVersion: "",
		Provider:     "",
	}

	result := convertModelInfoToProto(backend)

	assert.NotNil(t, result)
	assert.Empty(t, result.ModelName)
	assert.Empty(t, result.ModelVersion)
	assert.Empty(t, result.Provider)
}

// =============================================================================
// AIPluginGRPC Tests
//
// These tests verify the plugin wrapper creates correct server/client types.
// =============================================================================

// TestAIPluginGRPC_GRPCClient verifies that GRPCClient returns a properly
// constructed client wrapper. This is called by go-plugin during connection.
func TestAIPluginGRPC_GRPCClient(t *testing.T) {
	plugin := &AIPluginGRPC{}

	// GRPCClient requires a connection, but we can verify it doesn't panic
	// with nil broker (broker is only used for advanced scenarios)
	// Passing nil conn creates a client wrapper around a nil connection
	result, err := plugin.GRPCClient(context.TODO(), nil, nil)

	// The function creates a client wrapper even with nil conn
	// The actual RPC calls would fail, but creation succeeds
	assert.NotNil(t, result)
	assert.NoError(t, err)

	// Verify it's the correct type
	grpcClient, ok := result.(*GRPCClient)
	assert.True(t, ok, "result should be *GRPCClient")
	assert.NotNil(t, grpcClient)
}

// =============================================================================
// RunResult Tests
//
// Tests for the RunResult struct used to hold execution results.
// =============================================================================

// TestRunResult_ZeroValue verifies the default state of RunResult.
// A zero value should represent a successful execution with no model info.
func TestRunResult_ZeroValue(t *testing.T) {
	result := RunResult{}

	assert.Equal(t, int32(0), result.ExitCode)
	assert.Nil(t, result.ModelInfo)
}

// TestRunResult_WithModelInfo verifies that RunResult correctly holds
// both exit code and model information from execution.
func TestRunResult_WithModelInfo(t *testing.T) {
	result := RunResult{
		ExitCode: 42,
		ModelInfo: &ModelInfo{
			ModelName: "test-model",
			Provider:  "test-provider",
		},
	}

	assert.Equal(t, int32(42), result.ExitCode)
	assert.Equal(t, "test-model", result.ModelInfo.ModelName)
}

// =============================================================================
// MockClient Tests
//
// These tests verify the mock client implementation for dependency injection.
// =============================================================================

// TestMockClient_DefaultBehavior verifies that MockClient returns sensible
// defaults when no custom functions are configured.
func TestMockClient_DefaultBehavior(t *testing.T) {
	mock := NewMockClient()

	// Info returns default plugin info
	info, err := mock.Info(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, "mock", info.Name)
	assert.Equal(t, "1.0.0", info.Version)

	// Run returns success
	exitCode, err := mock.Run(context.Background(), &RunRequest{}, nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, int32(0), exitCode)

	// RunWithModelInfo returns success
	result, err := mock.RunWithModelInfo(context.Background(), &RunRequest{}, nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, int32(0), result.ExitCode)

	// Kill doesn't panic
	mock.Kill()

	// Verify call tracking
	assert.Equal(t, 1, mock.InfoCalls)
	assert.Equal(t, 1, mock.RunCalls)
	assert.Equal(t, 1, mock.RunWithModelInfoCalls)
	assert.Equal(t, 1, mock.KillCalls)
}

// TestMockClient_CustomInfo verifies that custom Info behavior can be injected.
func TestMockClient_CustomInfo(t *testing.T) {
	mock := &MockClient{
		InfoFunc: func(ctx context.Context) (*PluginInfo, error) {
			return &PluginInfo{Name: "custom-plugin", Version: "2.0.0"}, nil
		},
	}

	info, err := mock.Info(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, "custom-plugin", info.Name)
	assert.Equal(t, "2.0.0", info.Version)
}

// TestMockClient_CustomRun verifies that custom Run behavior can be injected.
func TestMockClient_CustomRun(t *testing.T) {
	mock := &MockClient{
		RunFunc: func(ctx context.Context, req *RunRequest, stdout, stderr io.Writer) (int32, error) {
			if stdout != nil {
				_, _ = stdout.Write([]byte("hello from mock"))
			}
			return 42, nil
		},
	}

	var buf bytes.Buffer
	exitCode, err := mock.Run(context.Background(), &RunRequest{}, &buf, nil)

	assert.NoError(t, err)
	assert.Equal(t, int32(42), exitCode)
	assert.Equal(t, "hello from mock", buf.String())
}

// TestMockClient_CustomRunWithModelInfo verifies custom RunWithModelInfo behavior.
func TestMockClient_CustomRunWithModelInfo(t *testing.T) {
	mock := &MockClient{
		RunWithModelInfoFunc: func(ctx context.Context, req *RunRequest, stdout, stderr io.Writer) (*RunResult, error) {
			return &RunResult{
				ExitCode: 0,
				ModelInfo: &ModelInfo{
					ModelName: "claude-3-haiku",
					Provider:  "anthropic",
				},
			}, nil
		},
	}

	result, err := mock.RunWithModelInfo(context.Background(), &RunRequest{}, nil, nil)

	assert.NoError(t, err)
	assert.Equal(t, "claude-3-haiku", result.ModelInfo.ModelName)
	assert.Equal(t, "anthropic", result.ModelInfo.Provider)
}

// TestMockClient_CustomKill verifies custom Kill behavior can be injected.
func TestMockClient_CustomKill(t *testing.T) {
	killCalled := false
	mock := &MockClient{
		KillFunc: func() {
			killCalled = true
		},
	}

	mock.Kill()

	assert.True(t, killCalled)
	assert.Equal(t, 1, mock.KillCalls)
}

// TestMockClientFactory_ReturnsProvidedMock verifies that MockClientFactory
// always returns the same mock instance regardless of parameters.
func TestMockClientFactory_ReturnsProvidedMock(t *testing.T) {
	mock := NewMockClient()
	factory := MockClientFactory(mock)

	// Call factory multiple times with different params
	client1, err1 := factory("backend-a", 0)
	client2, err2 := factory("backend-b", 5)

	assert.NoError(t, err1)
	assert.NoError(t, err2)
	assert.Same(t, mock, client1)
	assert.Same(t, mock, client2)
}

// TestClient_InterfaceCompliance verifies that PluginClient implements Client.
func TestClient_InterfaceCompliance(t *testing.T) {
	// This is a compile-time check that PluginClient implements Client
	var _ Client = (*PluginClient)(nil)

	// And MockClient
	var _ Client = (*MockClient)(nil)
}

// TestDefaultClientFactory_ReturnsNonNilFactory verifies that DefaultClientFactory
// returns a usable factory function.
func TestDefaultClientFactory_ReturnsNonNilFactory(t *testing.T) {
	factory := DefaultClientFactory()
	assert.NotNil(t, factory)
	// Note: Actually calling the factory would try to start a real plugin,
	// which is slow and requires integration testing. We just verify the
	// factory is non-nil.
}
