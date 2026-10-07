package cli

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// The product's own publish path, end to end, against a REAL bare repository:
// `bundle push` with no injected PublishManager, so the remote's file:// URL
// goes through remote.NewPublisher to the generic-git publisher and the git
// binary. Every other push test in this package swaps the Publisher for a
// recorder, which proves what push ASKED for and nothing about what a remote
// ends up holding. The assertions here read the bare repository itself.

// bareGit runs git against the bare repository and returns its trimmed output.
func bareGit(t *testing.T, bare string, args ...string) string {
	t.Helper()
	return taskstest.Git(t, bare, nil, args...)
}

func TestPushBundle_GenericGitRemote_LandsTheTreeInTheBareRepository(t *testing.T) {
	// The publish commit is made by the user's git, which owns identity: the
	// environment supplies one, and no developer config leaks in.
	t.Setenv("GIT_AUTHOR_NAME", "ctxloom test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@ctxloom.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "ctxloom test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@ctxloom.invalid")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	root := t.TempDir()
	bare := filepath.Join(root, "bundles.git")
	seed := filepath.Join(root, "seed")
	initPullGit(t, root, "init", "--bare", "-b", "main", bare)
	initPullGit(t, root, "init", "-b", "main", seed)
	require.NoError(t, os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644))
	initPullGit(t, seed, "add", "-A")
	initPullGit(t, seed, "-c", "user.name=seed", "-c", "user.email=seed@ctxloom.invalid", "commit", "-m", "seed")
	initPullGit(t, seed, "push", bare, "main")
	seedSHA := bareGit(t, bare, "rev-parse", "main")

	appDir := t.TempDir()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "remotes.yaml"),
		[]byte("default: team\nschema_version: 1\nremotes:\n  team:\n    url: file://"+bare+"\n"), 0o644))
	manifest := writeDirFormBundle(t, cfg, "dir-form")
	manifestBytes, err := os.ReadFile(manifest)
	require.NoError(t, err)

	cmd, out := testCmd()
	require.NoError(t, pushBundleCfg(cmd, cfg, nil, "dir-form", "", false, ""))

	target := remote.PublishPath(remote.ItemTypeBundle, "dir-form")
	assert.Equal(t, strings.TrimSpace(string(manifestBytes)),
		bareGit(t, bare, "show", "main:"+path.Join(target, "bundle.yaml")),
		"the envelope landed in the remote's branch, verbatim")
	assert.Equal(t, "# greet\n\nSay hello.",
		bareGit(t, bare, "show", "main:"+path.Join(target, "fragments", "greet.md")),
		"the whole tree landed, not just its envelope")
	assert.Equal(t, seedSHA, bareGit(t, bare, "rev-parse", "main^"),
		"the publish is one commit on top of the remote's history")
	assert.Contains(t, out.String(), target, "the reported destination is the one written")
}
