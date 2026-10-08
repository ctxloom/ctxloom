package cli

import (
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// claudeKind is the registered claude engine: the hook verbs' tests drive
// its codec with the payloads claude really writes.
func claudeKind(t *testing.T) engine.Engine {
	t.Helper()
	kind, ok := engines.Registry().Lookup(claude.EngineName)
	require.True(t, ok)
	return kind
}

// claudeCodec is claude's hook codec (claudeKind).
func claudeCodec(t *testing.T) engine.HookCodec { return claudeKind(t).Hooks() }

// claudeAnswer is the stdout envelope claude reads from a hook, as these
// tests read it back.
type claudeAnswer struct {
	Decision           string `json:"decision"`
	Reason             string `json:"reason"`
	SystemMessage      string `json:"systemMessage"`
	HookSpecificOutput *struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// decodeClaudeAnswer parses one hook answer.
func decodeClaudeAnswer(t *testing.T, raw []byte) claudeAnswer {
	t.Helper()
	var a claudeAnswer
	require.NoError(t, json.Unmarshal(raw, &a), "answer %q", raw)
	return a
}

// firedBy names engine as the firing engine on cmd, as a delivered hook's
// --engine does.
func firedBy(t *testing.T, cmd *cobra.Command, engineName string) *cobra.Command {
	t.Helper()
	cmd.Flags().String(hookEngineFlagName, "", "")
	require.NoError(t, cmd.Flags().Set(hookEngineFlagName, engineName))
	return cmd
}
