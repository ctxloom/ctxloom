package operations

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/shared/agent"
)

// stubClient is a minimal pb.Client for testing RunOneshot without a real
// backend: Run records the request and emits canned stdout.
type stubClient struct {
	out    string
	echo   bool // when true, write the request prompt back as output
	gotReq *pb.RunStart
}

func (s *stubClient) Run(_ context.Context, req *pb.RunStart, _ io.Reader, stdout, _ io.Writer, _ <-chan *pb.WindowSize) (int32, error) {
	s.gotReq = req
	if s.echo && req.Prompt != nil {
		_, _ = io.WriteString(stdout, req.Prompt.Content)
	} else {
		_, _ = io.WriteString(stdout, s.out)
	}
	return 0, nil
}
func (s *stubClient) Info(context.Context) (*pb.LLMInfo, error) { return &pb.LLMInfo{}, nil }
func (s *stubClient) RunWithModelInfo(context.Context, *pb.RunStart, io.Reader, io.Writer, io.Writer, <-chan *pb.WindowSize) (*pb.RunResult, error) {
	return &pb.RunResult{}, nil
}
func (s *stubClient) GetSession(context.Context, string) (*agent.Session, error) { return nil, nil }
func (s *stubClient) ListSessions(context.Context) ([]agent.SessionMeta, error)  { return nil, nil }
func (s *stubClient) GetPlans(context.Context, string) ([]agent.PlanFile, error) { return nil, nil }
func (s *stubClient) Kill()                                                      {}

func oneshotTestConfig() *config.Config {
	return &config.Config{
		AppPaths: []string{testBaseDir},
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"claude-fast": {Type: "claude-code"},
				"gemini-code": {Type: "gemini"},
			},
			Defaults: config.RoleDefaults{Primary: "claude-fast"},
		},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"rev": {
				LLM:       "gemini-code",
				Fragments: []config.FragmentRef{{Name: "dev#fragments/go-patterns"}},
			},
		}},
	}
}

func TestRunOneshot_ProfileLLMAndContextFlow(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig()

	stub := &stubClient{out: "  REVIEW FINDINGS  \n"}
	var gotBackend string
	factory := func(backendName string, _ int) (pb.Client, error) {
		gotBackend = backendName
		return stub, nil
	}

	res, err := RunOneshot(context.Background(), cfg, RunOneshotRequest{
		Profile: "rev",
		Task:    "review this diff",
		Loader:  loader,
		Factory: factory,
	})
	require.NoError(t, err)

	// Profile's llm (gemini-code) resolved to the gemini backend.
	assert.Equal(t, "gemini-code", res.Label)
	assert.Equal(t, "gemini", res.Backend)
	assert.Equal(t, "gemini", gotBackend)

	// Output captured and trimmed.
	assert.Equal(t, "REVIEW FINDINGS", res.Output)

	// Task became the prompt; assembled profile context rode along as a fragment.
	require.NotNil(t, stub.gotReq.Prompt)
	assert.Equal(t, "review this diff", stub.gotReq.Prompt.Content)
	require.NotEmpty(t, stub.gotReq.Fragments)
	assert.Contains(t, stub.gotReq.Fragments[0].Content, "Go Patterns")
	assert.Equal(t, pb.ExecutionMode_ONESHOT, stub.gotReq.Options.Mode)
}

func TestResolveBackend(t *testing.T) {
	cfg := &config.Config{LM: config.LMConfig{Configs: map[string]config.LLMConfig{
		"gemini-code": {Type: "gemini", Body: map[string]any{"model": "gemini-2.5-pro"}},
	}}}

	t.Run("configured label resolves to its type and model", func(t *testing.T) {
		backend, model := ResolveBackend(cfg, "gemini-code")
		assert.Equal(t, "gemini", backend)
		assert.Equal(t, "gemini-2.5-pro", model)
	})

	t.Run("unknown non-backend label degrades to the default", func(t *testing.T) {
		backend, model := ResolveBackend(cfg, "no-such-label")
		assert.Equal(t, config.DefaultLLM, backend)
		assert.Empty(t, model)
	})
}

func TestRunOneshot_OverrideWinsOverProfileLLM(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig()

	var gotBackend string
	factory := func(backendName string, _ int) (pb.Client, error) {
		gotBackend = backendName
		return &stubClient{out: "ok"}, nil
	}

	res, err := RunOneshot(context.Background(), cfg, RunOneshotRequest{
		Profile: "rev", // declares gemini-code
		Task:    "x",
		LLM:     "claude-fast", // override wins
		Loader:  loader,
		Factory: factory,
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-fast", res.Label)
	assert.Equal(t, "claude-code", res.Backend)
	assert.Equal(t, "claude-code", gotBackend)
}
