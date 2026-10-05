package git

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// injectedAuthor is the identity a calling hook or alias tries to impose on
// ctxloom's own git through inherited config overrides.
const injectedAuthor = "Injected Override"

// CLAIM: config overrides inherited from whatever ran ctxloom — `git -c`, a
// hook, an alias — do not reach the git processes ctxloom spawns. git exports
// both forms of `-c` to its children: GIT_CONFIG_PARAMETERS, and the
// GIT_CONFIG_COUNT / GIT_CONFIG_KEY_<n> / GIT_CONFIG_VALUE_<n> family. Either
// one silently rewrites the configuration of every git ctxloom runs, outranking
// the repository's own config.
func TestExecGit_CommitAll_IgnoresInheritedConfigOverrides(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	for name, env := range map[string][][2]string{
		"count-key-value": {
			{"GIT_CONFIG_COUNT", "1"},
			{"GIT_CONFIG_KEY_0", "user.name"},
			{"GIT_CONFIG_VALUE_0", injectedAuthor},
		},
		"parameters": {
			{"GIT_CONFIG_PARAMETERS", "'user.name'='" + injectedAuthor + "'"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := initRepo(t)
			require.NoError(t, writeFile(filepath.Join(repo, "README.md"), "changed"))
			for _, kv := range env {
				t.Setenv(kv[0], kv[1])
			}

			_, _, err := NewExec().CommitAll(context.Background(), repo, "commit under an inherited override")
			require.NoError(t, err)

			author := taskstest.Git(t, repo, nil, "log", "-1", "--format=%an")
			assert.Equal(t, initRepoIdentity, author,
				"ctxloom's git took its identity from an inherited config override, not the repository")
		})
	}
}
