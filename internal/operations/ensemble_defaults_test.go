package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// TestMapProfiles_EmptyMemberProfilesComposeDefaultAgent verifies that map
// members with an empty profile compose the DEFAULT AGENT's profiles (the
// replacement for the retired home-inherited defaults) and that a concurrent
// fan does so race-free: DefaultAgentProfiles is a pure read, so no member
// goroutine mutates shared cfg state (this was a -race regression when the old
// EnsureDefaultProfiles wrote inherited defaults from every goroutine).
func TestMapProfiles_EmptyMemberProfilesComposeDefaultAgent(t *testing.T) {
	cfg := &config.Config{
		AppPaths:     []string{t.TempDir()},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"home/dev"}}},
		LM: config.LMConfig{
			Configs:  map[string]config.LLMConfig{"claude-fast": {Type: "claude-code"}},
			Defaults: config.RoleDefaults{Primary: "claude-fast"},
		},
	}
	factory := func(backendName, _ string, _ int) (pb.Client, error) {
		return &stubClient{out: backendName}, nil
	}

	parts := MapProfiles(context.Background(), cfg, MapProfilesRequest{
		Members:     []string{"", "", "", ""},
		Task:        "review",
		Concurrency: 4,
		Factory:     factory,
	})

	// The default profile ("home/dev") doesn't resolve to real content, which is
	// fine — a configured default degrades per fault tolerance. What matters is
	// that all members ran (no race, no abort) composing the default agent's set.
	assert.Len(t, parts, 4)
	for _, p := range parts {
		assert.False(t, p.Failed(), "empty-profile member must compose the default agent: %v", p.Err)
	}
	assert.Equal(t, []string{"home/dev"}, cfg.DefaultAgentProfiles())
}
