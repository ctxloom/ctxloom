package spawn

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// TestProdSpawner_Resolve_MissingAgent_NamesCreate: agent_run on a name no
// agent has is refused with ErrNoAgent and a fix naming a command the CLI
// actually has, carried as a remedy so the model sees it as the fix line.
func TestProdSpawner_Resolve_MissingAgent_NamesCreate(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeSpawnerConfig(t, appDir, "schema_version: 7\nagents:\n  dev:\n    llm: claude-code\n")
	s := newSpawner(termRep(), spawnerApp(t, appDir), filepath.Dir(appDir), nil)

	_, err := s.Resolve(context.Background(), "worker")
	require.ErrorIs(t, err, launch.ErrNoAgent)
	fix, ok := clifmt.RemedyOf(err)
	require.True(t, ok, "the refusal carries its fix: %v", err)
	assert.Equal(t, createAgentFix, fix)
}
