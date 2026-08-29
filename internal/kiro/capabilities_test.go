package kiro

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
)

func TestWriteCommandFiles_SkillMD(t *testing.T) {
	fs := afero.NewMemMapFs()
	cmds := []agent.CommandExport{
		{Name: "review", Content: "Do a review.", Enabled: true, Description: "PR review helper"},
	}
	require.NoError(t, WriteCommandFiles("/proj", cmds, agent.WithCommandFS(fs)))

	data, err := afero.ReadFile(fs, "/proj/.kiro/skills/review/SKILL.md")
	require.NoError(t, err)
	s := string(data)
	assert.Contains(t, s, `name: "review"`)
	assert.Contains(t, s, `description: "PR review helper"`)
	assert.Contains(t, s, "Do a review.")
	// front-matter precedes the body
	assert.True(t, len(s) > 0 && s[:4] == "---\n")

	// manifest records the written skill for clean reconciliation
	exists, _ := afero.Exists(fs, filepath.Join("/proj/.kiro/skills", ledger.Name))
	assert.True(t, exists)
}

func TestWriteCommandFiles_DescriptionFallsBackToName(t *testing.T) {
	fs := afero.NewMemMapFs()
	cmds := []agent.CommandExport{{Name: "deploy", Content: "ship it", Enabled: true}}
	require.NoError(t, WriteCommandFiles("/proj", cmds, agent.WithCommandFS(fs)))

	data, err := afero.ReadFile(fs, "/proj/.kiro/skills/deploy/SKILL.md")
	require.NoError(t, err)
	assert.Contains(t, string(data), `description: "deploy"`)
}

func TestRenderSkillFile_GroupedNamePreservedAsSubdir(t *testing.T) {
	rel, content, err := renderSkillFile(agent.CommandExport{Name: "go/review", Content: "x", Description: "d"})
	require.NoError(t, err)
	assert.Equal(t, "go/review/SKILL.md", rel)
	// the front-matter identifier is slash-flattened
	assert.Contains(t, string(content), `name: "go-review"`)
}
