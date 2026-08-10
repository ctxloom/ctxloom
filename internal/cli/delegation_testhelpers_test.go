package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// delegationFixture stages a hermetic project (one bundle fragment, one
// profile composing it, mock-typed engine labels) and the given agents, with
// HOME scrubbed so session accounting and trust stores stay in the sandbox.
//
// GAP 1 (prodSpawner.Resolve re-reads agent definitions FROM DISK per call —
// spawner.go's resolveCfg) means the returned in-memory cfg.GetConfiguredAgents() is no
// longer the only thing a spawn sees: a REAL config.yaml describing the SAME
// bindings must exist at app too, or the disk reload observes an empty
// agent set the in-memory cfg promised. writeDelegationConfigYAML keeps the
// two in lockstep.
func delegationFixture(t *testing.T, subs map[string]agents.Agent) (*config.Config, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	app := filepath.Join(root, ".ctxloom")
	writeDelegationFile(t, filepath.Join(app, "content", "bundles", "kit1.yaml"),
		"version: \"1.0.0\"\nfragments:\n  f1:\n    content: \"FRAG-ONE\"\n")
	writeDelegationFile(t, filepath.Join(app, "profiles", "p1.yaml"),
		"bundles:\n  - ctxloom:local@bundles/kit1\n")
	writeDelegationConfigYAML(t, app, subs)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{app},
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"fast": {Type: "mock", Body: map[string]any{"model": "m-fast"}},
			},
			Defaults: config.RoleDefaults{Primary: "fast"},
		},
		Agents: subs,
	})
	return cfg, root
}

// writeDelegationConfigYAML persists the fixture's LLM registry + agents to
// a real config.yaml, mirroring the in-memory cfg built alongside it (see
// delegationFixture's GAP 1 note).
func writeDelegationConfigYAML(t *testing.T, app string, subs map[string]agents.Agent) {
	t.Helper()
	doc := struct {
		Version int `yaml:"version"`
		LLM     struct {
			Configs map[string]struct {
				Type  string `yaml:"type"`
				Model string `yaml:"model"`
			} `yaml:"configs"`
			Defaults struct {
				Primary string `yaml:"primary"`
			} `yaml:"defaults"`
		} `yaml:"llm"`
		Agents map[string]agents.Agent `yaml:"agents,omitempty"`
	}{Version: config.CurrentConfigVersion}
	doc.LLM.Configs = map[string]struct {
		Type  string `yaml:"type"`
		Model string `yaml:"model"`
	}{"fast": {Type: "mock", Model: "m-fast"}}
	doc.LLM.Defaults.Primary = "fast"
	doc.Agents = subs
	raw, err := yaml.Marshal(doc)
	require.NoError(t, err)
	writeDelegationFile(t, filepath.Join(app, "config.yaml"), string(raw))
}

func writeDelegationFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func headlessAgent(profiles ...string) agents.Agent {
	return agents.Agent{LLM: "fast", Profiles: profiles, Permissions: "bypass"}
}

// fakeChatEngine is a minimal pb.Client for the CLI-level coordinator tests:
// it records each turn's text and completes it, so the child "launches" and
// stays alive without a real engine. The coord conformance suite covers the
// delegation semantics; here we only need the production spawn path to reach a
// live child.
//
// Wave C4 mock/test migration note: this fixture uses config.LLMConfig{Type:
// "mock"} (backend "mock"), which is StructuredChat but NOT on the
// coordinator's ViaStartRun allowlist (coord/spawner.go's
// viaStartRunBackends) — so it deliberately rides the delegated-child
// go-plugin Chat dial (operations/delegate.go's Start, non-oneshot branch)
// rather than StartRun. That is NOT redundant with the coord package's own
// fakeSpawner-based conformance suite (delegation SEMANTICS: turn ordering,
// D3, resume, roster, approvals — see conformance_test.go and friends): this
// fixture exists only to reach a REAL *coord.Coordinator (production
// prodSpawner, not coord's unexported/package-internal fakeSpawner) so the
// CLI tool-handler PLUMBING can be pinned end-to-end. Faking that over
// StartRun instead would require simulating a whole runner process dialing
// home over RunnerChannel/RunChannel (coord's own fakeSpawner.StartEngine
// does exactly this, in-package) — a materially bigger fixture for a test
// that does not care which transport carried the turn. Left as-is,
// consciously: it is the surviving legacy-dial consumer this wave's
// reachability proof accounts for, not a migration gap.
type fakeChatEngine struct {
	mu    sync.Mutex
	texts []string
	// req is the ChatRequest this engine's Chat call was given — captured so
	// a test can assert on what the spawn actually plumbed through
	// (permissions, workdir, MCP servers) instead of only observing that a
	// harp came back non-empty. Before this, the request was thrown away
	// entirely: only the turn TEXT (below) was ever inspectable.
	req     agent.ChatRequest
	gotChat bool
}

func (f *fakeChatEngine) Chat(ctx context.Context, req agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
	f.mu.Lock()
	f.req = req
	f.gotChat = true
	f.mu.Unlock()

	in := make(chan agent.ChatMessage)
	events := make(chan agent.ChatEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(errs)
		defer close(events)
		for msg := range in {
			if msg.Text == "" {
				continue
			}
			f.mu.Lock()
			f.texts = append(f.texts, msg.Text)
			f.mu.Unlock()
			select {
			case events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "ok"}}:
			case <-ctx.Done():
				return
			}
			select {
			case events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return in, events, errs, nil
}

// Request returns the ChatRequest captured by this engine's Chat call, and
// whether Chat was ever invoked (false means nothing spawned onto this
// engine, so the zero-value ChatRequest below would otherwise look
// indistinguishable from a genuinely empty one).
func (f *fakeChatEngine) Request() (agent.ChatRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.req, f.gotChat
}

// Texts returns the message texts this engine has received so far (a copy,
// safe to read concurrently with the Chat goroutine still appending).
func (f *fakeChatEngine) Texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

func (f *fakeChatEngine) Info(context.Context) (*pb.LLMInfo, error) { return &pb.LLMInfo{}, nil }
func (f *fakeChatEngine) Run(context.Context, *pb.RunStart, io.Reader, io.Writer, io.Writer, <-chan *pb.WindowSize) (int32, error) {
	return 0, nil
}
func (f *fakeChatEngine) RunWithModelInfo(context.Context, *pb.RunStart, io.Reader, io.Writer, io.Writer, <-chan *pb.WindowSize) (*pb.RunResult, error) {
	return &pb.RunResult{}, nil
}
func (f *fakeChatEngine) GetSession(context.Context, string) (*agent.Session, error) {
	return nil, fmt.Errorf("no session")
}
func (f *fakeChatEngine) WatchSession(context.Context, string) (<-chan *pb.WatchEvent, <-chan error, error) {
	return nil, nil, nil
}
func (f *fakeChatEngine) ListSessions(context.Context) ([]agent.SessionMeta, error) { return nil, nil }
func (f *fakeChatEngine) GetPlans(context.Context, string) ([]agent.PlanFile, error) {
	return nil, nil
}
func (f *fakeChatEngine) Kill() {}

// fakeChatEngineSpawns mints fakeChatEngines (the coord.Options.Factory seam:
// a non-nil factory skips isolation entirely), keeping every one it creates,
// in spawn order, so a test can reach into a specific child's captured
// ChatRequest after agent_run has spawned it. The factory signature
// (backendName, label string, verbosity int) never carries the agent name or
// harp, so callers correlate by call order: prodSpawner.Launch calls the
// factory synchronously within agent_run's handler, so the Nth factory call
// is the child of the Nth agent_run this test issues.
type fakeChatEngineSpawns struct {
	mu      sync.Mutex
	engines []*fakeChatEngine
}

// factory returns the pb.ClientFactory to pass as coord.Options.Factory.
func (s *fakeChatEngineSpawns) factory() pb.ClientFactory {
	return func(string, string, int) (pb.Client, error) {
		e := &fakeChatEngine{}
		s.mu.Lock()
		s.engines = append(s.engines, e)
		s.mu.Unlock()
		return e, nil
	}
}

// nth returns the (0-indexed) nth engine spawned, or nil if fewer than n+1
// spawns have happened yet.
func (s *fakeChatEngineSpawns) nth(n int) *fakeChatEngine {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 0 || n >= len(s.engines) {
		return nil
	}
	return s.engines[n]
}
