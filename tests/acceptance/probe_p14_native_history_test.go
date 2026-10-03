package acceptance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestP14_ContainerWrites(t *testing.T) {
	assert.NoError(t, p14JudgeContainerWrites("h", []string{"claude/.claude.json", "claude/projects/-ws/abc.jsonl"}))
	assert.ErrorIs(t, p14JudgeContainerWrites("h", []string{"claude/.claude.json", "claude/projects/-ws/memory/x.md"}), errP14NoHistory,
		"a projects/ entry that is not a conversation .jsonl is not history")
	assert.ErrorIs(t, p14JudgeContainerWrites("h", []string{"claude/history.jsonl"}), errP14NoHistory,
		"a .jsonl outside projects/ is not conversation history")
}

// p14Layout plants the cell's layout: <root>/home/claude (the config home)
// whose projects is the relative link into <root>/native/claude/projects.
func p14Layout(t *testing.T) (cfg, target string) {
	t.Helper()
	root := t.TempDir()
	cfg = filepath.Join(root, "home", "claude")
	target = filepath.Join(root, "native", "claude", "projects")
	require.NoError(t, os.MkdirAll(cfg, 0o700))
	require.NoError(t, os.MkdirAll(target, 0o700))
	require.NoError(t, os.Symlink(p14NativeLink, filepath.Join(cfg, "projects")))
	return cfg, target
}

func TestP14_SymlinkFollowed(t *testing.T) {
	cfg, target := p14Layout(t)
	require.ErrorIs(t, p14JudgeSymlink(cfg), errP14NoHistory, "nothing written through the link yet")
	require.NoError(t, os.MkdirAll(filepath.Join(target, "-ws"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(target, "-ws", "s.jsonl"), []byte("{}\n"), 0o600))
	assert.NoError(t, p14JudgeSymlink(cfg))
}

func TestP14_SymlinkReplacedIsRed(t *testing.T) {
	cfg, _ := p14Layout(t)
	link := filepath.Join(cfg, "projects")
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.MkdirAll(filepath.Join(link, "-ws"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(link, "-ws", "s.jsonl"), []byte("{}\n"), 0o600))
	assert.ErrorIs(t, p14JudgeSymlink(cfg), errP14LinkReplaced)
}

func TestP14_SymlinkRetargetedIsRed(t *testing.T) {
	cfg, _ := p14Layout(t)
	link := filepath.Join(cfg, "projects")
	require.NoError(t, os.Remove(link))
	elsewhere := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(elsewhere, "s.jsonl"), []byte("{}\n"), 0o600))
	require.NoError(t, os.Symlink(elsewhere, link))
	assert.Error(t, p14JudgeSymlink(cfg))
}
