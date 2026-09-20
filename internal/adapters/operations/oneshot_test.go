package operations

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// stubClient is a canned pb.Client: it captures the request it was run with
// and answers with out (or echoes the prompt, or emits the lead fragments).
type stubClient struct {
	out  string
	echo bool // when true, write the request prompt back as output
	// exitCode and stderr are what a failing engine reports.
	exitCode int32
	stderr   string
	// emitFragments writes the run's assembled context (its lead fragments)
	// back as output, so a composed profile-context is observable in the
	// answer. Wins over echo/out.
	emitFragments bool
	gotReq        *pb.RunStart
}

func (s *stubClient) Run(_ context.Context, req *pb.RunStart, _ io.Reader, stdout, stderr io.Writer, _ <-chan *pb.WindowSize) (int32, error) {
	s.gotReq = req
	if s.exitCode != 0 {
		_, _ = io.WriteString(stderr, s.stderr)
		return s.exitCode, nil
	}
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
func (s *stubClient) RunWithModelInfo(ctx context.Context, req *pb.RunStart, stdin io.Reader, stdout, stderr io.Writer, resize <-chan *pb.WindowSize) (*pb.RunResult, error) {
	code, err := s.Run(ctx, req, stdin, stdout, stderr, resize)
	return &pb.RunResult{ExitCode: code}, err
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
				// bypass: these are the profile/context/output-flow tests,
				// not permission-resolution tests (the launch package pins
				// the floor).
				"claude-fast": {Type: "claude-code", Permissions: "bypass"},
				"agy-code":    {Type: "mock", Permissions: "bypass"},
			},
			Defaults: config.RoleDefaults{Primary: "claude-fast"},
		},
	})
}

// testLaunchDeps composes the resolver's ports over cfg with stateless
// doubles: an in-memory session store, the isolation seam stubbed to a host
// workspace, the pipeline seam for the assembler. HOME is isolated by the
// callers' fixture setup.
func testLaunchDeps(t *testing.T, cfg *config.Config, pipe *bundles.Pipeline, stub pb.Client) launch.Deps {
	t.Helper()
	stubPrepareIsolation(t, nil, func() pb.Client { return stub })
	return launch.Deps{
		Snapshot:  &config.Snapshot{Config: cfg},
		Engines:   backends.Engines(),
		Assembler: &assembler{pipe: pipe},
		Cells:     Cells{cfg: cfg},
		Endpoints: endpointMinter{},
		Sessions:  sessions.NewMemStore(),
		Host:      launch.HostFacts{Home: t.TempDir(), CtxloomHome: t.TempDir(), Binary: "ctxloom"},
	}
}

// testOneShot resolves a one-shot session over cfg and drives its turns on
// the stub client.
func testOneShot(t *testing.T, cfg *config.Config, pipe *bundles.Pipeline, stub pb.Client, src launch.Source) (*OneShot, error) {
	t.Helper()
	deps := testLaunchDeps(t, cfg, pipe, stub)
	if src.WorkDir == "" {
		src.WorkDir = t.TempDir()
	}
	o, err := StartOneShot(context.Background(), deps, sessions.Seed{ProjectDir: src.WorkDir}, src, 0)
	if err != nil {
		return nil, err
	}
	o.Factory = func(string, string, int) (pb.Client, error) { return stub, nil }
	t.Cleanup(o.End)
	return o, nil
}

// TestOneShot_ProfileLLMAndContextFlow: a profile set resolves through the
// one resolver — the profile's llm picks the engine, its context leads the
// turn, the prompt is the turn — and the answer is captured, trimmed.
func TestOneShot_ProfileLLMAndContextFlow(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig(t)
	stub := &stubClient{out: "  REVIEW FINDINGS  \n"}

	o, err := testOneShot(t, cfg, opPipe(cfg, loader), stub, launch.Source{Profiles: []string{"rev"}})
	require.NoError(t, err)
	assert.Equal(t, "agy-code", o.Launch.Label.Label, "the profile's llm label")
	assert.Equal(t, "mock", string(o.Launch.Engine))

	out, err := o.Turn(context.Background(), "review this diff")
	require.NoError(t, err)
	assert.Equal(t, "REVIEW FINDINGS", out)

	require.NotNil(t, stub.gotReq.Prompt)
	assert.Equal(t, "review this diff", stub.gotReq.Prompt.Content)
	require.NotEmpty(t, stub.gotReq.Fragments)
	assert.Contains(t, stub.gotReq.Fragments[0].Content, "Go Patterns")
	assert.Equal(t, pb.ExecutionMode_ONESHOT, stub.gotReq.Options.Mode)
	assert.Equal(t, pb.LaunchForm_LAUNCH_FORM_DELIVER, stub.gotReq.Options.LaunchForm,
		"a one-shot owns a harp and delivers its own surfaces")
	assert.Equal(t, o.Launch.Identity.Harp, stub.gotReq.Options.Env[sessions.EnvHarp], "the turn carries the session's identity")
}

// TestOneShot_TurnsShareOneSession: every turn rides the same harp and the
// same endpoint; the prompt is what changes.
func TestOneShot_TurnsShareOneSession(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig(t)
	stub := &stubClient{echo: true}

	o, err := testOneShot(t, cfg, opPipe(cfg, loader), stub, launch.Source{Profiles: []string{"rev"}})
	require.NoError(t, err)
	first, err := o.Turn(context.Background(), "one")
	require.NoError(t, err)
	firstHarp := stub.gotReq.Options.Env[sessions.EnvHarp]
	second, err := o.Turn(context.Background(), "two")
	require.NoError(t, err)
	assert.Equal(t, "one", first)
	assert.Equal(t, "two", second)
	assert.Equal(t, firstHarp, stub.gotReq.Options.Env[sessions.EnvHarp], "one session, many turns")
	assert.NotEmpty(t, o.Launch.MCP.URL, "the session endpoint was minted once for every turn")
}

// TestOneShot_FailedTurnNamesTheExitCodeAndStderr: an engine that exits
// non-zero fails the turn with the code and whatever it said on stderr —
// the "LLM exited with code N" a distill's caller reports (the content
// distiller leaves the item raw over it, naming this) and never a
// successful empty answer.
func TestOneShot_FailedTurnNamesTheExitCodeAndStderr(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig(t)
	stub := &stubClient{exitCode: 1, stderr: "quota exhausted"}
	o, err := testOneShot(t, cfg, opPipe(cfg, loader), stub, launch.Source{Profiles: []string{"rev"}})
	require.NoError(t, err)
	_, err = o.Turn(context.Background(), "distill this")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LLM exited with code 1")
	assert.Contains(t, err.Error(), "quota exhausted")
}

// TestOneShot_EndedSessionRefusesATurn: End releases the session; a turn
// after it is refused, never silently driven on a dead harp.
func TestOneShot_EndedSessionRefusesATurn(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig(t)
	stub := &stubClient{out: "x"}
	o, err := testOneShot(t, cfg, opPipe(cfg, loader), stub, launch.Source{Profiles: []string{"rev"}})
	require.NoError(t, err)
	o.End()
	_, err = o.Turn(context.Background(), "again")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ended")
}

func TestResolveBackend(t *testing.T) {
	cfg := gatedFixture(config.Fixture{LM: config.LMConfig{Configs: map[string]config.LLMConfig{
		"agy-code": {Type: "mock", Body: map[string]any{"model": "gemini-3-pro"}},
	}}})
	// The degrade target is the engine shipped by default, bound by
	// validating against the composed registry — never a literal in config.
	require.NoError(t, cfg.Validate(backends.Engines()))

	t.Run("configured label resolves to its type and model", func(t *testing.T) {
		backend, model := ResolveBackend(cfg, "agy-code")
		assert.Equal(t, "mock", backend)
		assert.Equal(t, "gemini-3-pro", model)
	})

	t.Run("unknown non-backend label degrades to the default", func(t *testing.T) {
		backend, model := ResolveBackend(cfg, "no-such-label")
		assert.Equal(t, "claude-code", backend)
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
			assert.Equal(t, "claude-code", backend, "ResolveBackend(%q) degrades like any unknown label", spelling)
			assert.Empty(t, model)
		}
	})

	// A configured entry's type is validated on write (SetLLM); a hand-written
	// one that names no registered backend leaves here AS WRITTEN, so the
	// launch path refuses it as an unknown backend rather than this boundary
	// rounding it to a real one.
	t.Run("a hand-written entry's type is not rewritten", func(t *testing.T) {
		handWritten := gatedFixture(config.Fixture{LM: config.LMConfig{Configs: map[string]config.LLMConfig{
			"hand-edited": {Type: "claude", Body: map[string]any{"model": "opus"}},
		}}})
		backend, model := ResolveBackend(handWritten, "hand-edited")
		assert.Equal(t, "claude", backend)
		assert.Equal(t, "opus", model)
	})
}
