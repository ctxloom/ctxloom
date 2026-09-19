package operations

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/spf13/afero"
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

func oneshotTestConfig(t *testing.T) *config.Config {
	return cfgWithDirProfiles(t, afero.NewMemMapFs(), testBaseDir, map[string]config.Profile{
		"rev": {
			LLM:       "agy-code",
			Fragments: []config.FragmentRef{{Name: "dev#fragments/go-patterns"}},
		},
	}, config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				// bypass: these are the generic profile/context/output-flow
				// tests, not permission-resolution tests — headless-safe so
				// effectiveMemberPermission's refusal doesn't collide with
				// unrelated coverage (see TestRunOneshot_ResolvesHeadlessPosture
				// for the dedicated permission-resolution cases).
				"claude-fast": {Type: "claude-code", Permissions: "bypass"},
				"agy-code":    {Type: "mock", Permissions: "bypass"},
			},
			Defaults: config.RoleDefaults{Primary: "claude-fast"},
		},
	})
}

func TestRunOneshot_ProfileLLMAndContextFlow(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig(t)

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
	assert.Equal(t, "mock", res.Backend)
	assert.Equal(t, "mock", gotBackend)

	// Output captured and trimmed.
	assert.Equal(t, "REVIEW FINDINGS", res.Output)

	// Task became the prompt; assembled profile context rode along as a fragment.
	require.NotNil(t, stub.gotReq.Prompt)
	assert.Equal(t, "review this diff", stub.gotReq.Prompt.Content)
	require.NotEmpty(t, stub.gotReq.Fragments)
	assert.Contains(t, stub.gotReq.Fragments[0].Content, "Go Patterns")
	assert.Equal(t, pb.ExecutionMode_ONESHOT, stub.gotReq.Options.Mode)
	// A none-isolation member shares the project cwd, so it declares the form
	// that NAMES the session's surfaces and writes none of its own. This used to
	// be SkipSetup:true — a bypass of the whole delivery machinery, which is why
	// the member's composed context had to travel by a second route and was
	// silently discarded when that route was not wired up (dire-petal). The form
	// is selected from the cell, so asserting it here is asserting that the
	// selection actually happened.
	assert.Equal(t, pb.LaunchForm_LAUNCH_FORM_PRESENT, stub.gotReq.Options.LaunchForm,
		"a shared-cell member must PRESENT the session's surfaces, never write per-member config into the one shared cwd")
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
	cfg := cfgWithDirProfiles(t, afero.NewMemMapFs(), testBaseDir, map[string]config.Profile{
		"keep-plan":     {LLM: "claude-plan"},
		"floor-default": {LLM: "claude-none"},
		"collapse-agy":  {LLM: "agy-plan"},
	}, config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"claude-plan": {Type: "claude-code", Permissions: "plan"},
				"claude-none": {Type: "claude-code"},
				"agy-plan":    {Type: "mock", Permissions: "plan"},
			},
			Defaults: config.RoleDefaults{Primary: "claude-none"},
		},
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
		"agy-code": {Type: "mock", Body: map[string]any{"model": "gemini-3-pro"}},
	}}})

	t.Run("configured label resolves to its type and model", func(t *testing.T) {
		backend, model := ResolveBackend(cfg, "agy-code")
		assert.Equal(t, "mock", backend)
		assert.Equal(t, "gemini-3-pro", model)
	})

	t.Run("unknown non-backend label degrades to the default", func(t *testing.T) {
		backend, model := ResolveBackend(cfg, "no-such-label")
		assert.Equal(t, config.DefaultLLM, backend)
		assert.Empty(t, model)
	})

	t.Run("the ad-hoc arm admits only a registered backend name", func(t *testing.T) {
		backend, model := ResolveBackend(cfg, "claude-code")
		assert.Equal(t, "claude-code", backend)
		assert.Empty(t, model)
	})

	// There is no alias table, so a retired short spelling is not a backend
	// name. As an ad-hoc label it is simply an unknown label, and takes the
	// same degrade path "no-such-label" does above — it is NOT resolved to
	// the engine it used to abbreviate.
	t.Run("a retired short spelling is an unknown label, not a backend", func(t *testing.T) {
		for _, spelling := range []string{"claude", "CLAUDE", "claudecode", "Claude-Code"} {
			backend, model := ResolveBackend(cfg, spelling)
			assert.Equal(t, config.DefaultLLM, backend, "ResolveBackend(%q) degrades like any unknown label", spelling)
			assert.Empty(t, model)
		}
	})

	// A configured entry's type is validated on write (SetLLM); a hand-written
	// one that names no registered backend leaves here AS WRITTEN, so the
	// launch path refuses it as an unknown backend rather than this boundary
	// rounding it to a real one.
	t.Run("a hand-written entry's type is not rewritten", func(t *testing.T) {
		handWritten := config.NewFixture(config.Fixture{LM: config.LMConfig{Configs: map[string]config.LLMConfig{
			"hand-edited": {Type: "claude", Body: map[string]any{"model": "opus"}},
		}}})
		backend, model := ResolveBackend(handWritten, "hand-edited")
		assert.Equal(t, "claude", backend)
		assert.Equal(t, "opus", model)
	})
}

func TestRunOneshot_OverrideWinsOverProfileLLM(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig(t)

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
