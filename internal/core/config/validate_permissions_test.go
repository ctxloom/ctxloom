package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

func boolp(b bool) *bool { return &b }

// Load refuses what no launch could honour: an engine block for an engine
// nobody composed, a block or a label key its engine refuses, a neutral
// value outside its vocabulary, and a may_delegate naming no agent.
func TestConfig_Validate_Permissions(t *testing.T) {
	reg := registryOf("alpha", "beta")
	ok := config.NewFixture(config.Fixture{
		LM: config.LMConfig{Configs: map[string]config.LLMConfig{"l": {Type: "alpha", Permissions: agents.LabelPermissions{Engine: map[string]any{"mode": "on"}, NeutralPermissions: agents.NeutralPermissions{Sandbox: "full"}}}}},
		Agents: map[string]agents.Agent{
			"a":      {LLM: "l", MayDelegate: []string{"finder"}, Permissions: agents.Permissions{NeutralPermissions: agents.NeutralPermissions{Approver: "reviewer", ApprovalTimeout: "20m", Network: boolp(false)}, Engines: map[string]map[string]any{"alpha": {"mode": "on"}, "beta": {}}}},
			"finder": {LLM: "l"},
		},
		Permissions: agents.NeutralPermissions{Approver: "none", Sandbox: "workspace-write"},
	})
	require.NoError(t, ok.Validate(reg))

	for name, tc := range map[string]struct {
		f    config.Fixture
		want []string
	}{
		"unknown engine block": {config.Fixture{Agents: map[string]agents.Agent{"a": {Permissions: agents.Permissions{Engines: map[string]map[string]any{"gamma": {}}}}}}, []string{"agents.a.permissions.gamma", "alpha, beta"}},
		"engine refuses block": {config.Fixture{Agents: map[string]agents.Agent{"a": {Permissions: agents.Permissions{Engines: map[string]map[string]any{"beta": {"bad": 1}}}}}}, []string{"agents.a.permissions.beta", "bad key"}},
		"engine refuses label": {config.Fixture{LM: config.LMConfig{Configs: map[string]config.LLMConfig{"l": {Type: "beta", Permissions: agents.LabelPermissions{Engine: map[string]any{"bad": 1}}}}}}, []string{"llm.configs.l.permissions", "bad key"}},
		"approver":             {config.Fixture{Agents: map[string]agents.Agent{"a": {Permissions: agents.Permissions{NeutralPermissions: agents.NeutralPermissions{Approver: "robot"}}}}}, []string{"agents.a.permissions", `"robot"`, "human|none|reviewer"}},
		"sandbox":              {config.Fixture{Permissions: agents.NeutralPermissions{Sandbox: "wide-open"}}, []string{"permissions", `"wide-open"`, "read-only|workspace-write|full"}},
		"timeout":              {config.Fixture{LM: config.LMConfig{Configs: map[string]config.LLMConfig{"l": {Type: "alpha", Permissions: agents.LabelPermissions{NeutralPermissions: agents.NeutralPermissions{ApprovalTimeout: "2h"}}}}}}, []string{"llm.configs.l.permissions", `"2h"`, "60m"}},
		"may_delegate unknown": {config.Fixture{Agents: map[string]agents.Agent{"a": {MayDelegate: []string{"nobody"}}, "b": {}}}, []string{"agents.a.may_delegate", `"nobody"`, "a, b"}},
	} {
		t.Run(name, func(t *testing.T) {
			err := config.NewFixture(tc.f).Validate(reg)
			require.Error(t, err)
			for _, w := range tc.want {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
}
