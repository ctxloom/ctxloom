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

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// delegationFixture stages a hermetic project (one bundle fragment, one
// profile composing it, mock-typed engine labels) and the given agents, with
// HOME scrubbed so session accounting and trust stores stay in the sandbox.
func delegationFixture(t *testing.T, subs map[string]agents.Agent) (*config.Config, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	app := filepath.Join(root, ".ctxloom")
	writeDelegationFile(t, filepath.Join(app, "cache", "bundles", "kit1.yaml"),
		"version: \"1.0.0\"\nfragments:\n  f1:\n    content: \"FRAG-ONE\"\n")
	writeDelegationFile(t, filepath.Join(app, "profiles", "p1.yaml"),
		"bundles:\n  - ctxloom:local@bundles/kit1\n")
	cfg := &config.Config{
		AppPaths: []string{app},
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"fast": {Type: "mock", Body: map[string]any{"model": "m-fast"}},
			},
			Defaults: config.RoleDefaults{Primary: "fast"},
		},
		Agents: subs,
	}
	return cfg, root
}

func writeDelegationFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func headlessAgent(profiles ...string) agents.Agent {
	return agents.Agent{Engine: "fast", Profiles: profiles, Permissions: "bypass"}
}

// fakeChatEngine is a minimal pb.Client for the CLI-level coordinator tests:
// it records each turn's text and completes it, so the child "launches" and
// stays alive without a real engine. The coord conformance suite covers the
// delegation semantics; here we only need the production spawn path to reach a
// live child.
type fakeChatEngine struct {
	mu    sync.Mutex
	texts []string
}

func (f *fakeChatEngine) Chat(ctx context.Context, req agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
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

// fakeChatFactory returns a pb.ClientFactory minting fakeChatEngines (the
// coord.Options.Factory seam: a non-nil factory skips isolation entirely).
func fakeChatFactory() pb.ClientFactory {
	return func(string, string, int) (pb.Client, error) { return &fakeChatEngine{}, nil }
}
