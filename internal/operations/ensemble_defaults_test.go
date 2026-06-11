package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// TestMapProfiles_EmptyMemberProfilesResolveDefaultsRaceFree is a -race
// regression: members with an empty profile make AssembleContext call
// EnsureDefaultProfiles, which (before the fix) mutated cfg's default-profile
// state from every member goroutine concurrently. MapProfiles must resolve the
// inherited defaults once on the calling goroutine so the member goroutines'
// calls are read-only.
func TestMapProfiles_EmptyMemberProfilesResolveDefaultsRaceFree(t *testing.T) {
	// Home config DEFINES defaults — the write path of EnsureDefaultProfiles —
	// while the project cfg has none, so every empty-profile member would
	// otherwise race to record the inheritance.
	writeHomeConfig(t, []string{"home/dev"})

	cfg := &config.Config{
		AppPaths: []string{t.TempDir()},
		LM: config.LMConfig{
			Configs:  map[string]config.LLMConfig{"claude-fast": {Type: "claude-code"}},
			Defaults: config.RoleDefaults{Primary: "claude-fast"},
		},
	}
	factory := func(backendName, _ string, _ int) (pb.Client, error) {
		return &stubClient{out: backendName}, nil
	}

	parts := MapProfiles(context.Background(), cfg, MapProfilesRequest{
		Profiles:    []string{"", "", "", ""},
		Task:        "review",
		Concurrency: 4,
		Factory:     factory,
	})

	// The default profile ("home/dev") doesn't resolve to real content, which
	// is fine — configured defaults degrade per fault tolerance. What matters
	// is that all members ran (no race, no abort).
	assert.Len(t, parts, 4)
	for _, p := range parts {
		assert.False(t, p.Failed(), "empty-profile member must run with inherited defaults: %v", p.Err)
	}
	assert.Equal(t, []string{"home/dev"}, cfg.GetDefaultProfiles(),
		"defaults resolved once up front on the main goroutine")
}
