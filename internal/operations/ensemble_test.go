package operations

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

func mapTestConfig() *config.Config {
	return &config.Config{
		AppPaths: []string{testBaseDir},
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"claude-fast": {Type: "claude-code"},
				"agy-code":    {Type: "antigravity"},
			},
			Defaults: config.RoleDefaults{Primary: "claude-fast"},
		},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"a": {LLM: "claude-fast", Fragments: []config.FragmentRef{{Name: "dev#fragments/go-patterns"}}},
			"b": {LLM: "agy-code", Fragments: []config.FragmentRef{{Name: "dev#fragments/go-patterns"}}},
		}},
	}
}

// MapProfiles preserves input order, labels each part with its resolved backend,
// and turns a member failure into an error Part without aborting the rest.
func TestMapProfiles_OrderLabelingAndFaultTolerance(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := mapTestConfig()

	// Factory echoes the backend name as output, so each part's output proves
	// which backend its member resolved to.
	factory := func(backendName, _ string, _ int) (pb.Client, error) {
		return &stubClient{out: backendName}, nil
	}

	parts := MapProfiles(context.Background(), cfg, MapProfilesRequest{
		Profiles: []string{"a", "nonexistent", "b"},
		Task:     "review",
		Loader:   loader,
		Factory:  factory,
	})

	if assert.Len(t, parts, 3) {
		// Order preserved.
		assert.Equal(t, "a", parts[0].Profile)
		assert.Equal(t, "nonexistent", parts[1].Profile)
		assert.Equal(t, "b", parts[2].Profile)

		// Member a → claude-code backend; labeled and not failed.
		assert.False(t, parts[0].Failed())
		assert.Equal(t, "claude-code", parts[0].Backend)
		assert.Equal(t, "claude-code", parts[0].Output)

		// Unknown profile fails gracefully (no panic, others still ran).
		assert.True(t, parts[1].Failed())
		assert.Contains(t, parts[1].Err, "nonexistent")
		assert.Empty(t, parts[1].Output)

		// Member b → antigravity backend.
		assert.False(t, parts[2].Failed())
		assert.Equal(t, "antigravity", parts[2].Backend)
	}
}

func TestFormatParts_LabeledStreamWithErrors(t *testing.T) {
	out := FormatParts([]Part{
		{Profile: "code-review/security", Label: "claude-fast", Output: "no issues"},
		{Profile: "code-review/perf", Err: "boom"},
	})
	assert.Contains(t, out, "===== part: code-review/security (llm: claude-fast) =====")
	assert.Contains(t, out, "no issues")
	assert.Contains(t, out, "===== part: code-review/perf =====")
	assert.Contains(t, out, "[error: boom]")
	// Output rows end in a newline so blocks don't run together.
	assert.True(t, strings.HasSuffix(out, "\n"))
}

func TestMapProfiles_Empty(t *testing.T) {
	parts := MapProfiles(context.Background(), mapTestConfig(), MapProfilesRequest{})
	assert.Empty(t, parts)
}

// Weave fans members in parallel, appends injected parts, and synthesizes them
// all through the synthesis profile (on its own llm). The echo stub returns each
// run's prompt, so the synthesizer's "report" is the framed synthesis input,
// letting us assert the member + injected parts flowed in.
func TestWeave_MembersInjectedAndSynthesis(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := mapTestConfig()
	cfg.Profiles.Definitions["synth"] = config.Profile{LLM: "agy-code"}

	factory := func(string, string, int) (pb.Client, error) { return &stubClient{echo: true}, nil }

	res, err := Weave(context.Background(), cfg, WeaveRequest{
		Members:       []string{"a", "b"},
		Synthesize:    "synth",
		Task:          "review the diff",
		InjectedParts: []Part{{Profile: "legacy", Output: "old finding"}},
		Loader:        loader,
		Factory:       factory,
	})
	require.NoError(t, err)

	// Members ran (echoed the task) + the injected part is appended, in order.
	require.Len(t, res.Parts, 3)
	assert.Equal(t, "a", res.Parts[0].Profile)
	assert.Equal(t, "review the diff", res.Parts[0].Output)
	assert.Equal(t, "legacy", res.Parts[2].Profile)
	assert.Equal(t, "old finding", res.Parts[2].Output)

	// Synthesizer ran on its own llm (antigravity) and produced the report.
	require.NotNil(t, res.Synthesizer)
	assert.Equal(t, "synth", res.Synthesizer.Profile)
	assert.Equal(t, "antigravity", res.Synthesizer.Backend)

	// Report (echoed synthesis input) carries the framing + every labeled part.
	assert.Contains(t, res.Report, "## Specialist outputs")
	assert.Contains(t, res.Report, "===== part: a")
	assert.Contains(t, res.Report, "===== part: legacy")
	assert.Contains(t, res.Report, "old finding")
}

func TestWeave_NoSynthesizeEmitsPartsOnly(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := mapTestConfig()
	factory := func(string, string, int) (pb.Client, error) { return &stubClient{out: "x"}, nil }

	res, err := Weave(context.Background(), cfg, WeaveRequest{
		Members:      []string{"a"},
		Task:         "t",
		NoSynthesize: true,
		Loader:       loader,
		Factory:      factory,
	})
	require.NoError(t, err)
	assert.Empty(t, res.Report)
	assert.Nil(t, res.Synthesizer)
	require.Len(t, res.Parts, 1)
}
