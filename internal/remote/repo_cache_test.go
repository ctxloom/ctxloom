package remote

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestRepo creates a bare git repo with some files for testing.
func createTestRepo(t *testing.T, dir string) string {
	t.Helper()

	repoDir := filepath.Join(dir, "test-repo")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create .ctxloom/content/bundles/test.yaml
	bundleDir := filepath.Join(repoDir, ".ctxloom", "content", "bundles")
	require.NoError(t, os.MkdirAll(bundleDir, 0755))

	content := []byte("version: v1\ndescription: test bundle\n")
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "test.yaml"), content, 0644))

	_, err = wt.Add(".ctxloom/content/bundles/test.yaml")
	require.NoError(t, err)

	_, err = wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@test.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	return repoDir
}

// fakeGitBinary drops an executable named "git" in a fresh temp dir that,
// instead of touching any repository, dumps its own environment (one VAR=value
// per line, matching `env`(1)) to envFile and exits 0. Returns the directory to
// prepend to PATH so exec.CommandContext's "git" resolves to it.
func fakeGitBinary(t *testing.T, envFile string) string {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\nenv > \"" + envFile + "\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0755))
	return binDir
}

// TestRunGit_NonInteractiveEnv pins the fix for the credential-prompt hang: a
// git subprocess must never be able to block on interactive auth. It proves
// runGit's child process env always carries GIT_TERMINAL_PROMPT=0 and empty
// GIT_ASKPASS/SSH_ASKPASS — even when the parent environment already set an
// askpass (the exact shape of the live hang: an ambient GUI askpass answering a
// failed clone with a blocking password dialog) and even when extraEnv (the
// github auth-header injection) is empty, which is the path that previously
// left cmd.Env nil and let the child inherit the parent verbatim.
func TestRunGit_NonInteractiveEnv(t *testing.T) {
	assertNonInteractive := func(t *testing.T, envFile string) {
		t.Helper()
		data, err := os.ReadFile(envFile)
		require.NoError(t, err)
		env := string(data)
		assert.Regexp(t, `(?m)^GIT_TERMINAL_PROMPT=0$`, env,
			"git must be told not to prompt a human at a terminal")
		assert.Regexp(t, `(?m)^GIT_ASKPASS=$`, env,
			"a configured askpass must be neutralized, not inherited")
		assert.Regexp(t, `(?m)^SSH_ASKPASS=$`, env,
			"a configured SSH askpass must be neutralized, not inherited")
	}

	t.Run("extraEnv empty (cmd.Env previously left nil)", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "env.txt")
		binDir := fakeGitBinary(t, envFile)
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		// Simulate the live hang's ambient setup: a GUI askpash already configured
		// in the parent environment.
		t.Setenv("GIT_ASKPASS", "/usr/bin/some-gui-credential-dialog")
		t.Setenv("SSH_ASKPASS", "/usr/bin/ssh-askpass")

		err := runGit(context.Background(), "", "clone", nil,
			"clone", "--", "https://example.invalid/owner/repo", filepath.Join(t.TempDir(), "dir"))
		require.NoError(t, err)
		assertNonInteractive(t, envFile)
	})

	t.Run("extraEnv non-empty (github auth-header injection)", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "env.txt")
		binDir := fakeGitBinary(t, envFile)
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("GIT_ASKPASS", "/usr/bin/some-gui-credential-dialog")

		err := runGit(context.Background(), "", "fetch", []string{"GIT_CONFIG_COUNT=1"},
			"fetch", "--all")
		require.NoError(t, err)
		assertNonInteractive(t, envFile)
	})
}

func TestRepoCache_repoDirForURL_RejectsTraversal(t *testing.T) {
	base := t.TempDir()
	cache := NewRepoCache(base, AuthConfig{})

	// A crafted URL with ".." segments must not escape the cache dir, since
	// the result is later RemoveAll'd and cloned into.
	traversal := []string{
		"https://evil.com/../../../../etc/passwd",
		"https://host/a/../../../../../tmp/pwned",
		"ssh://git@host/../../outside",
	}
	for _, u := range traversal {
		dir, derr := cache.RepoDirForURL(u)
		require.NoError(t, derr, "RepoDirForURL(%q) should resolve (traversal segments are dropped, not refused outright)", u)
		rel, err := filepath.Rel(base, dir)
		require.NoError(t, err, "RepoDirForURL(%q) = %q", u, dir)
		assert.False(t, rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)),
			"RepoDirForURL(%q) = %q escaped base %q (rel=%q)", u, dir, base, rel)
	}
}

func TestRepoCache_EnsureRepo_Clone(t *testing.T) {
	tmpDir := t.TempDir()
	sourceRepo := createTestRepo(t, tmpDir)

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	repoDir, err := cache.EnsureRepo(context.Background(), "file://"+sourceRepo, ForgeGitHub)
	require.NoError(t, err)
	assert.DirExists(t, repoDir)
}

func TestRepoCache_EnsureRepo_AlreadyCloned(t *testing.T) {
	tmpDir := t.TempDir()
	sourceRepo := createTestRepo(t, tmpDir)

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	// First clone
	repoDir1, err := cache.EnsureRepo(context.Background(), "file://"+sourceRepo, ForgeGitHub)
	require.NoError(t, err)

	// Second call should return immediately (already cloned, no fetch)
	repoDir2, err := cache.EnsureRepo(context.Background(), "file://"+sourceRepo, ForgeGitHub)
	require.NoError(t, err)
	assert.Equal(t, repoDir1, repoDir2)
}

func TestRepoCache_EnsureRepo_CorruptClone(t *testing.T) {
	tmpDir := t.TempDir()
	sourceRepo := createTestRepo(t, tmpDir)

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	// Create a corrupt directory where the clone would go
	cloneURL := "file://" + sourceRepo
	expectedDir, derr := cache.RepoDirForURL(cloneURL)
	require.NoError(t, derr)
	require.NoError(t, os.MkdirAll(expectedDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(expectedDir, "garbage"), []byte("not a repo"), 0644))

	// Should delete and re-clone
	repoDir, err := cache.EnsureRepo(context.Background(), cloneURL, ForgeGitHub)
	require.NoError(t, err)
	assert.DirExists(t, repoDir)
}

func TestRepoCache_UpdateRepo(t *testing.T) {
	tmpDir := t.TempDir()
	sourceRepo := createTestRepo(t, tmpDir)

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	// First ensure (clone)
	repoDir, err := cache.EnsureRepo(context.Background(), "file://"+sourceRepo, ForgeGitHub)
	require.NoError(t, err)

	// Update should fetch
	repoDir2, err := cache.UpdateRepo(context.Background(), "file://"+sourceRepo, ForgeGitHub)
	require.NoError(t, err)
	assert.Equal(t, repoDir, repoDir2)
}

// TestRepoCache_UpdateRepo_FetchFailure_ReturnsEmptyPath pins that
// UpdateRepo must return ("", err) on a fetch failure, matching ensureClone's
// contract, not (repoDir, err) — a caller that (mistakenly) used the returned
// path on error would otherwise silently keep working against a stale clone.
func TestRepoCache_UpdateRepo_FetchFailure_ReturnsEmptyPath(t *testing.T) {
	tmpDir := t.TempDir()
	sourceRepo := createTestRepo(t, tmpDir)
	repoURL := "file://" + sourceRepo

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	// Clone succeeds while the source exists.
	_, err := cache.EnsureRepo(context.Background(), repoURL, ForgeGitHub)
	require.NoError(t, err)

	// Remove the source so the next fetch fails — the clone is already
	// present (isGitRepo is true), so UpdateRepo takes the fetch path, not
	// the clone path.
	require.NoError(t, os.RemoveAll(sourceRepo))

	repoDir, err := cache.UpdateRepo(context.Background(), repoURL, ForgeGitHub)
	require.Error(t, err, "fetch against a removed source must fail")
	assert.Empty(t, repoDir, "a failed fetch must return an empty path, matching ensureClone's (\"\", err) contract")
}

func TestRepoCache_UpdateRepo_NotYetCloned(t *testing.T) {
	tmpDir := t.TempDir()
	sourceRepo := createTestRepo(t, tmpDir)

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	// Update on a repo that isn't cloned yet should clone it
	repoDir, err := cache.UpdateRepo(context.Background(), "file://"+sourceRepo, ForgeGitHub)
	require.NoError(t, err)
	assert.DirExists(t, repoDir)
}

// TestRepoCache_EnsureRef_ResolvesOlderRef verifies that a full clone makes an
// older ref (a tag behind HEAD) locally resolvable without any extra fetch —
// the clone carries complete history and all tags.
func TestRepoCache_EnsureRef_ResolvesOlderRef(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")

	repo, err := git.PlainInit(sourceDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "a.txt"), []byte("a\n"), 0644))
	_, err = wt.Add("a.txt")
	require.NoError(t, err)
	first, err := wt.Commit("first", &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()},
	})
	require.NoError(t, err)

	_, err = repo.CreateTag("v0.1.0", first, nil)
	require.NoError(t, err)

	// Second commit so HEAD moves past the tag.
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "b.txt"), []byte("b\n"), 0644))
	_, err = wt.Add("b.txt")
	require.NoError(t, err)
	_, err = wt.Commit("second", &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()},
	})
	require.NoError(t, err)

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	// EnsureRef with empty ref clones the full history.
	repoDir, err := cache.EnsureRef(context.Background(), "file://"+sourceDir, ForgeGitHub, "")
	require.NoError(t, err)
	assert.DirExists(t, repoDir)

	// The tag points at the older commit; the full clone already carries it, so
	// it is locally resolvable.
	repoDir2, err := cache.EnsureRef(context.Background(), "file://"+sourceDir, ForgeGitHub, "v0.1.0")
	require.NoError(t, err)
	assert.Equal(t, repoDir, repoDir2)

	clone, err := git.PlainOpen(repoDir2)
	require.NoError(t, err)
	_, err = clone.ResolveRevision(plumbing.Revision("refs/tags/v0.1.0"))
	require.NoError(t, err, "tag v0.1.0 must be locally resolvable in the full clone")
}

func TestRepoCache_repoDirForURL(t *testing.T) {
	cache := NewRepoCache("/tmp/cache", AuthConfig{})

	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "https github",
			url:  "https://github.com/owner/repo",
			want: "/tmp/cache/github.com/owner/repo",
		},
		{
			name: "https gitlab",
			url:  "https://gitlab.com/owner/repo",
			want: "/tmp/cache/gitlab.com/owner/repo",
		},
		{
			name: "with .git suffix",
			url:  "https://github.com/owner/repo.git",
			want: "/tmp/cache/github.com/owner/repo",
		},
		{
			// The cache key must lowercase the host, matching
			// cloneHost (used for the auth header) — otherwise a mixed-case
			// host produces a SECOND clone directory for a repo already
			// cached under its lowercase form.
			name: "mixed-case host is lowercased",
			url:  "https://GitHub.com/owner/repo",
			want: "/tmp/cache/github.com/owner/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cache.RepoDirForURL(tt.url)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRepoCache_authEnv(t *testing.T) {
	t.Run("github with token injects extraheader via env, not argv", func(t *testing.T) {
		cache := NewRepoCache("", AuthConfig{GitHub: "test-token"})
		env := cache.authEnv("https://github.com/owner/repo", ForgeGitHub)
		require.Len(t, env, 3)
		assert.Equal(t, "GIT_CONFIG_COUNT=1", env[0])
		assert.Equal(t, "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", env[1])
		want := base64.StdEncoding.EncodeToString([]byte("x-access-token:test-token"))
		assert.Equal(t, "GIT_CONFIG_VALUE_0=AUTHORIZATION: basic "+want, env[2])
	})

	t.Run("github without token injects nothing", func(t *testing.T) {
		cache := NewRepoCache("", AuthConfig{})
		assert.Nil(t, cache.authEnv("https://github.com/owner/repo", ForgeGitHub))
	})

	t.Run("generic git uses ambient auth, no injection", func(t *testing.T) {
		cache := NewRepoCache("", AuthConfig{GitHub: "test-token"})
		assert.Nil(t, cache.authEnv("https://gitlab.com/owner/repo", ForgeGitGeneric))
	})
}

func TestNormalizeCloneURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://github.com/owner/repo", "https://github.com/owner/repo"},
		// The suffix is KEPT: it is preserved everywhere else, and a clone
		// argument that disagrees with the identity about what repository a
		// string names is the split this file's shared parse exists to end.
		// `git clone https://host/owner/repo.git` is the spelling forges hand
		// out, so keeping it costs nothing.
		{"https://github.com/owner/repo.git", "https://github.com/owner/repo.git"},
		{"owner/repo", "https://github.com/owner/repo"},
		// The shorthand check used to run BEFORE the .git suffix
		// was trimmed, so "owner/repo.git" (which contains a ".") never
		// qualified as shorthand and was returned bare instead of expanded —
		// `git clone -- owner/repo <dir>` then treats it as a local path.
		{"owner/repo.git", "https://github.com/owner/repo.git"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeCloneURL(tt.input))
		})
	}
}

// TestRepoCache_ConcurrentEnsureSameDir pins the per-directory clone lock:
// concurrent EnsureRepo/EnsureRef/UpdateRepo calls for the same repo URL —
// including through INDEPENDENT RepoCache instances, as the bundle- and
// profile-side prewarm goroutines do — must serialize inside the cache. The
// unsynchronized RemoveAll+clone race corrupted the directory ("directory not
// empty") whenever two goroutines raced a cold cache.
func TestRepoCache_ConcurrentEnsureSameDir(t *testing.T) {
	tmpDir := t.TempDir()
	sourceRepo := createTestRepo(t, tmpDir)
	cloneURL := "file://" + sourceRepo
	cacheDir := filepath.Join(tmpDir, "cache")

	const workers = 8
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			// Independent instances per goroutine: caller discipline must not be
			// required for safety.
			cache := NewRepoCache(cacheDir, AuthConfig{})
			var err error
			switch i % 3 {
			case 0:
				_, err = cache.EnsureRepo(context.Background(), cloneURL, ForgeGitHub)
			case 1:
				_, err = cache.EnsureRef(context.Background(), cloneURL, ForgeGitHub, "")
			default:
				_, err = cache.UpdateRepo(context.Background(), cloneURL, ForgeGitHub)
			}
			errs <- err
		}()
	}
	for i := 0; i < workers; i++ {
		require.NoError(t, <-errs, "concurrent clone-cache access must serialize, not corrupt the directory")
	}

	// The resulting clone is usable.
	cache := NewRepoCache(cacheDir, AuthConfig{})
	repoDir, err := cache.EnsureRepo(context.Background(), cloneURL, ForgeGitHub)
	require.NoError(t, err)
	_, err = git.PlainOpen(repoDir)
	require.NoError(t, err)
}

// failingGitBinary drops an executable "git" that, on `clone -- <url> <dir>`,
// leaves an unremovable file behind in <dir> (chmod 000 on its parent) and
// then fails — simulating a clone that dies mid-transfer, leaving debris the
// post-failure cleanup then also cannot remove. Any other subcommand no-ops
// successfully.
func failingGitBinary(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "clone" ]; then
    dir="$4"
    mkdir -p "$dir/locked"
    : > "$dir/locked/partial"
    chmod 000 "$dir/locked"
    exit 1
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0755))
	return binDir
}

// TestRepoCache_EnsureRepo_CloneFailure_CleanupErrorNotSwallowed pins that
// when a clone fails AND the subsequent cleanup (os.RemoveAll of the
// partial clone dir) also fails, the cleanup failure must be reported to the
// caller, not silently discarded — a discarded cleanup failure leaves exactly
// the corrupt-directory state ensureCloneLocked's own doc promises to remove.
func TestRepoCache_EnsureRepo_CloneFailure_CleanupErrorNotSwallowed(t *testing.T) {
	binDir := failingGitBinary(t)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, AuthConfig{})

	repoDir, err := cache.EnsureRepo(context.Background(), "https://example.com/owner/repo", ForgeGitGeneric)
	require.Error(t, err, "clone must fail")
	assert.Empty(t, repoDir)

	// Clean up the locked dir so t.TempDir()'s own removal doesn't fail.
	lockedDir := filepath.Join(cacheDir, "example.com", "owner", "repo", "locked")
	_ = os.Chmod(lockedDir, 0755)

	assert.Contains(t, err.Error(), "git clone failed", "must still report the original clone failure")
	assert.True(t,
		strings.Contains(err.Error(), "clean up") || strings.Contains(err.Error(), "permission denied"),
		"error = %q, want it to also name the cleanup failure instead of silently discarding it", err.Error())
}
