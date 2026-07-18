package cli

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// resetACPClientFlags restores every acp-client package flag var (and the
// Factory test seam) to its zero value, undone via t.Cleanup — these are
// cobra-bound package vars (see acpClientCmd's init()), so a test that sets
// one must not leak it into the next.
func resetACPClientFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		acpClientLLM = ""
		acpClientProfile = ""
		acpClientWorkDir = ""
		acpClientVerbosity = 0
		acpClientFactory = nil
	})
}

// acpClientStubClient is a minimal pb.Client for testing runACPClient without
// spawning a real ACP subprocess — mirrors internal/operations/oneshot_test.go's
// stubClient (same seam, one door).
type acpClientStubClient struct {
	out    string
	gotReq *pb.RunStart
}

func (s *acpClientStubClient) Run(_ context.Context, req *pb.RunStart, _ io.Reader, stdout, _ io.Writer, _ <-chan *pb.WindowSize) (int32, error) {
	s.gotReq = req
	_, _ = io.WriteString(stdout, s.out)
	return 0, nil
}
func (s *acpClientStubClient) Info(context.Context) (*pb.LLMInfo, error) { return &pb.LLMInfo{}, nil }
func (s *acpClientStubClient) RunWithModelInfo(context.Context, *pb.RunStart, io.Reader, io.Writer, io.Writer, <-chan *pb.WindowSize) (*pb.RunResult, error) {
	return &pb.RunResult{}, nil
}
func (s *acpClientStubClient) GetSession(context.Context, string) (*agent.Session, error) {
	return nil, nil
}
func (s *acpClientStubClient) WatchSession(context.Context, string) (<-chan *pb.WatchEvent, <-chan error, error) {
	return nil, nil, nil
}
func (s *acpClientStubClient) Chat(context.Context, agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
	return nil, nil, nil, nil
}
func (s *acpClientStubClient) ListSessions(context.Context) ([]agent.SessionMeta, error) {
	return nil, nil
}
func (s *acpClientStubClient) GetPlans(context.Context, string) ([]agent.PlanFile, error) {
	return nil, nil
}
func (s *acpClientStubClient) Kill() {}

func acpClientTestConfig() *config.Config {
	return &config.Config{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"acp-kiro":    {Type: "acp"},
				"claude-fast": {Type: "claude-code"},
			},
			Defaults: config.RoleDefaults{Primary: "claude-fast"},
		},
	}
}

// TestRunACPClient_RequiresLLMFlag proves the real RunE — not a stand-in —
// refuses to run (and never touches config or the plugin door) when --llm is
// unset: there is no "the" ACP-client label to default to.
func TestRunACPClient_RequiresLLMFlag(t *testing.T) {
	resetACPClientFlags(t)
	cmd, _ := formatCmd(formatText)
	err := runACPClient(cmd, []string{"hello"})
	require.ErrorIs(t, err, errACPClientLLMRequired)
}

// TestRunACPClientWithConfig_RejectsNonACPBackend proves --llm is validated
// against the resolved backend type: a label configured for a different
// engine (e.g. claude-code) is refused with a named fix, never silently
// spawned as if it were ACP.
func TestRunACPClientWithConfig_RejectsNonACPBackend(t *testing.T) {
	resetACPClientFlags(t)
	acpClientLLM = "claude-fast"

	cmd, _ := formatCmd(formatText)
	err := runACPClientWithConfig(cmd, acpClientTestConfig(), "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"claude-code"`)
	assert.Contains(t, err.Error(), "ctxloom llm list")
}

// TestRunACPClientWithConfig_DrivesConfiguredACPLabel is the end-to-end proof
// (the stub-Factory seam, same shape as operations.TestRunOneshot_*): an
// acp-type label resolves, the prompt rides RunStart.Prompt, and the stub's
// captured output reaches stdout — all through operations.RunOneshot, never a
// CLI-local internal/acp construction.
func TestRunACPClientWithConfig_DrivesConfiguredACPLabel(t *testing.T) {
	resetACPClientFlags(t)
	acpClientLLM = "acp-kiro"

	stub := &acpClientStubClient{out: "  hello from kiro  \n"}
	var gotBackend string
	acpClientFactory = func(backendName, _ string, _ int) (pb.Client, error) {
		gotBackend = backendName
		return stub, nil
	}

	cmd, buf := formatCmd(formatText)
	require.NoError(t, runACPClientWithConfig(cmd, acpClientTestConfig(), "ping"))

	assert.Equal(t, "acp", gotBackend)
	require.NotNil(t, stub.gotReq.Prompt)
	assert.Equal(t, "ping", stub.gotReq.Prompt.Content)
	assert.Contains(t, buf.String(), "hello from kiro")
}
