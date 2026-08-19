package operations

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// stubClient is a minimal pb.Client for testing RunOneshot without a real
// backend: Run records the request and emits canned stdout.
type stubClient struct {
	out  string
	echo bool // when true, write the request prompt back as output
	// emitFragments writes the run's assembled context (its lead fragments) back
	// as output, so a fan member's composed profile-context is observable in its
	// Part.Output. Wins over echo/out.
	emitFragments bool
	gotReq        *pb.RunStart
}

func (s *stubClient) Run(_ context.Context, req *pb.RunStart, _ io.Reader, stdout, _ io.Writer, _ <-chan *pb.WindowSize) (int32, error) {
	s.gotReq = req
	switch {
	case s.emitFragments:
		var parts []string
		for _, f := range req.Fragments {
			parts = append(parts, f.Content)
		}
		_, _ = io.WriteString(stdout, strings.Join(parts, "\n"))
	case s.echo && req.Prompt != nil:
		_, _ = io.WriteString(stdout, req.Prompt.Content)
	default:
		_, _ = io.WriteString(stdout, s.out)
	}
	return 0, nil
}
func (s *stubClient) Info(context.Context) (*pb.LLMInfo, error) { return &pb.LLMInfo{}, nil }
func (s *stubClient) RunWithModelInfo(context.Context, *pb.RunStart, io.Reader, io.Writer, io.Writer, <-chan *pb.WindowSize) (*pb.RunResult, error) {
	return &pb.RunResult{}, nil
}
func (s *stubClient) GetSession(context.Context, string) (*agent.Session, error) { return nil, nil }
func (s *stubClient) WatchSession(context.Context, string) (<-chan *pb.WatchEvent, <-chan error, error) {
	return nil, nil, nil
}
func (s *stubClient) Chat(context.Context, agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
	return nil, nil, nil, nil
}
func (s *stubClient) ListSessions(context.Context) ([]agent.SessionMeta, error)  { return nil, nil }
func (s *stubClient) GetPlans(context.Context, string) ([]agent.PlanFile, error) { return nil, nil }
func (s *stubClient) Kill()                                                      {}

func oneshotTestConfig() *config.Config {
	return config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				// bypass: these are the generic profile/context/output-flow
				// tests, not permission-resolution tests — headless-safe so
				// effectiveMemberPermission's refusal doesn't collide with
				// unrelated coverage (see TestRunOneshot_ResolvesHeadlessPosture
				// for the dedicated permission-resolution cases).
				"claude-fast": {Type: "claude-code", Permissions: "bypass"},
				"agy-code":    {Type: "antigravity", Permissions: "bypass"},
			},
			Defaults: config.RoleDefaults{Primary: "claude-fast"},
		},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"rev": {
				LLM:       "agy-code",
				Fragments: []config.FragmentRef{{Name: "dev#fragments/go-patterns"}},
			},
		}},
	})
}

func TestRunOneshot_ProfileLLMAndContextFlow(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig()

	stub := &stubClient{out: "  REVIEW FINDINGS  \n"}
	var gotBackend string
	factory := func(backendName, _ string, _ int) (pb.Client, error) {
		gotBackend = backendName
		return stub, nil
	}

	res, err := RunOneshot(context.Background(), cfg, RunOneshotRequest{
		Profile:  "rev",
		Task:     "review this diff",
		Pipeline: opPipe(cfg, loader),
		Factory:  factory,
	})
	require.NoError(t, err)

	// Profile's llm (agy-code) resolved to the antigravity backend.
	assert.Equal(t, "agy-code", res.Label)
	assert.Equal(t, "antigravity", res.Backend)
	assert.Equal(t, "antigravity", gotBackend)

	// Output captured and trimmed.
	assert.Equal(t, "REVIEW FINDINGS", res.Output)

	// Task became the prompt; assembled profile context rode along as a fragment.
	require.NotNil(t, stub.gotReq.Prompt)
	assert.Equal(t, "review this diff", stub.gotReq.Prompt.Content)
	require.NotEmpty(t, stub.gotReq.Fragments)
	assert.Contains(t, stub.gotReq.Fragments[0].Content, "Go Patterns")
	assert.Equal(t, pb.ExecutionMode_ONESHOT, stub.gotReq.Options.Mode)
	// dire-petal: this is exactly the SkipSetup:true + non-empty Fragments
	// combination the "none"-isolation oneshot member path sends —
	// asserted here so a future change that flips this default silently
	// can't slip past without also exercising the grpc-server delivery fix
	// (internal/lm/grpc.server_test.go's
	// TestGRPCServer_Run_SkipSetupDeliversFragmentsViaPrompt) that combination
	// depends on.
	assert.True(t, stub.gotReq.Options.SkipSetup,
		"a none-isolation member's RunStart must be SkipSetup — this is the exact request shape dire-petal's fragments-under-SkipSetup fix targets")
}

// TestRunOneshot_ResolvesHeadlessPosture pins fix C as it now reads: a
// headless oneshot/fan member honors a declared read-only plan on a backend
// that enforces it, but REFUSES a would-block or unenforceable posture
// instead of floor it up to bypass — a member cannot hang (there is no human
// to answer the engine's prompt), and silently elevating to bypass is worse
// than refusing. Floor-to-bypass was the ORIGINAL fix C; unroasted-spinning
// replaced the floor with an error once it was recognised as the same
// silent-elevation shape as the ACP one-shot arm bug.
func TestRunOneshot_ResolvesHeadlessPosture(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"claude-plan": {Type: "claude-code", Permissions: "plan"},
				"claude-none": {Type: "claude-code"},
				"agy-plan":    {Type: "antigravity", Permissions: "plan"},
			},
			Defaults: config.RoleDefaults{Primary: "claude-none"},
		},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"keep-plan":     {LLM: "claude-plan"},
			"floor-default": {LLM: "claude-none"},
			"collapse-agy":  {LLM: "agy-plan"},
		}},
	})
	t.Run("enforcing backend keeps declared plan", func(t *testing.T) {
		stub := &stubClient{out: "ok"}
		factory := func(string, string, int) (pb.Client, error) { return stub, nil }
		_, err := RunOneshot(context.Background(), cfg, RunOneshotRequest{
			Profile: "keep-plan", Task: "t", Pipeline: opPipe(cfg, loader), Factory: factory,
		})
		require.NoError(t, err)
		require.NotNil(t, stub.gotReq.Options)
		assert.Equal(t, agent.PermissionPlan.String(), stub.gotReq.Options.PermissionMode)
	})

	cases := []struct {
		name    string
		profile string
	}{
		{"no posture is refused, not floored to bypass", "floor-default"},
		{"unenforceable plan collapses then is refused, not floored to bypass", "collapse-agy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubClient{out: "ok"}
			factory := func(string, string, int) (pb.Client, error) { return stub, nil }
			_, err := RunOneshot(context.Background(), cfg, RunOneshotRequest{
				Profile: tc.profile, Task: "t", Pipeline: opPipe(cfg, loader), Factory: factory,
			})
			require.Error(t, err, "a would-block/unenforceable posture must refuse, not silently run at bypass")
			assert.Contains(t, err.Error(), `"default"`, "the error names the resolved (collapsed) posture")
			assert.Nil(t, stub.gotReq, "the engine must never have run")
		})
	}
}

func TestResolveBackend(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{LM: config.LMConfig{Configs: map[string]config.LLMConfig{
		"agy-code": {Type: "antigravity", Body: map[string]any{"model": "gemini-3-pro"}},
	}}})

	t.Run("configured label resolves to its type and model", func(t *testing.T) {
		backend, model := ResolveBackend(cfg, "agy-code")
		assert.Equal(t, "antigravity", backend)
		assert.Equal(t, "gemini-3-pro", model)
	})

	t.Run("unknown non-backend label degrades to the default", func(t *testing.T) {
		backend, model := ResolveBackend(cfg, "no-such-label")
		assert.Equal(t, config.DefaultLLM, backend)
		assert.Empty(t, model)
	})

	// A backend NAME is what the launch path keys tables by — including
	// internal/lm/isolation's credential-seed and instance-config tables, which
	// resolve engines by name and hold only canonical spellings. An alias that
	// left here as typed would seed no credentials and report nothing.
	t.Run("an ad-hoc alias is promoted to the canonical backend name", func(t *testing.T) {
		for _, spelling := range []string{"claude", "CLAUDE", "claudecode", "Claude-Code"} {
			backend, model := ResolveBackend(cfg, spelling)
			assert.Equal(t, "claude-code", backend, "ResolveBackend(%q)", spelling)
			assert.Empty(t, model)
		}
	})

	t.Run("a hand-written entry whose type is an alias resolves canonical", func(t *testing.T) {
		aliased := config.NewFixture(config.Fixture{LM: config.LMConfig{Configs: map[string]config.LLMConfig{
			"hand-edited": {Type: "claude", Body: map[string]any{"model": "opus"}},
		}}})
		backend, model := ResolveBackend(aliased, "hand-edited")
		assert.Equal(t, "claude-code", backend)
		assert.Equal(t, "opus", model)
	})
}

func TestRunOneshot_OverrideWinsOverProfileLLM(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig()

	var gotBackend string
	factory := func(backendName, _ string, _ int) (pb.Client, error) {
		gotBackend = backendName
		return &stubClient{out: "ok"}, nil
	}

	res, err := RunOneshot(context.Background(), cfg, RunOneshotRequest{
		Profile:  "rev", // declares agy-code
		Task:     "x",
		LLM:      "claude-fast", // override wins
		Pipeline: opPipe(cfg, loader),
		Factory:  factory,
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-fast", res.Label)
	assert.Equal(t, "claude-code", res.Backend)
	assert.Equal(t, "claude-code", gotBackend)
}
