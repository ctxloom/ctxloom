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

// resetACPRunFlags restores every acp-client package flag var (and the
// Factory test seam) to its zero value, undone via t.Cleanup — these are
// cobra-bound package vars (see acpRunCmd's init()), so a test that sets
// one must not leak it into the next.
func resetACPRunFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		acpRunLLM = ""
		acpRunProfile = ""
		acpRunWorkDir = ""
		acpRunVerbosity = 0
		acpRunFactory = nil
	})
}

// acpRunStubClient is a minimal pb.Client for testing runACPRun without
// spawning a real ACP subprocess — mirrors internal/operations/oneshot_test.go's
// stubClient (same seam, one door).
type acpRunStubClient struct {
	out    string
	gotReq *pb.RunStart
}

func (s *acpRunStubClient) Run(_ context.Context, req *pb.RunStart, _ io.Reader, stdout, _ io.Writer, _ <-chan *pb.WindowSize) (int32, error) {
	s.gotReq = req
	_, _ = io.WriteString(stdout, s.out)
	return 0, nil
}
func (s *acpRunStubClient) Info(context.Context) (*pb.LLMInfo, error) { return &pb.LLMInfo{}, nil }
func (s *acpRunStubClient) RunWithModelInfo(context.Context, *pb.RunStart, io.Reader, io.Writer, io.Writer, <-chan *pb.WindowSize) (*pb.RunResult, error) {
	return &pb.RunResult{}, nil
}
func (s *acpRunStubClient) GetSession(context.Context, string) (*agent.Session, error) {
	return nil, nil
}
func (s *acpRunStubClient) WatchSession(context.Context, string) (<-chan *pb.WatchEvent, <-chan error, error) {
	return nil, nil, nil
}
func (s *acpRunStubClient) Chat(context.Context, agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
	return nil, nil, nil, nil
}
func (s *acpRunStubClient) ListSessions(context.Context) ([]agent.SessionMeta, error) {
	return nil, nil
}
func (s *acpRunStubClient) GetPlans(context.Context, string) ([]agent.PlanFile, error) {
	return nil, nil
}
func (s *acpRunStubClient) Kill() {}

func acpRunTestConfig() *config.Config {
	return config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"acp-kiro":    {Type: "acp"},
				"claude-fast": {Type: "claude-code"},
			},
			Defaults: config.RoleDefaults{Primary: "claude-fast"},
		},
	})
}

// TestRunACPClient_RequiresLLMFlag proves the real RunE — not a stand-in —
// refuses to run (and never touches config or the plugin door) when --llm is
// unset: there is no "the" ACP-client label to default to.
func TestRunACPClient_RequiresLLMFlag(t *testing.T) {
	resetACPRunFlags(t)
	cmd, _ := formatCmd(formatText)
	err := runACPRun(cmd, []string{"hello"})
	require.ErrorIs(t, err, errACPRunLLMRequired)
}

// TestRunACPClientWithConfig_RejectsNonACPBackend proves --llm is validated
// against the resolved backend type: a label configured for a different
// engine (e.g. claude-code) is refused with a named fix, never silently
// spawned as if it were ACP.
func TestRunACPClientWithConfig_RejectsNonACPBackend(t *testing.T) {
	resetACPRunFlags(t)
	acpRunLLM = "claude-fast"

	cmd, _ := formatCmd(formatText)
	err := runACPRunWithConfig(cmd, acpRunTestConfig(), "hello")
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
	resetACPRunFlags(t)
	acpRunLLM = "acp-kiro"

	stub := &acpRunStubClient{out: "  hello from kiro  \n"}
	var gotBackend string
	acpRunFactory = func(backendName, _ string, _ int) (pb.Client, error) {
		gotBackend = backendName
		return stub, nil
	}

	cmd, buf := formatCmd(formatText)
	require.NoError(t, runACPRunWithConfig(cmd, acpRunTestConfig(), "ping"))

	assert.Equal(t, "acp", gotBackend)
	require.NotNil(t, stub.gotReq.Prompt)
	assert.Equal(t, "ping", stub.gotReq.Prompt.Content)
	assert.Contains(t, buf.String(), "hello from kiro")
}
