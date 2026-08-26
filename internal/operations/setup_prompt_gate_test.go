package operations

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// seedRemoteSetupCommand seeds an UNREVIEWED remote bundle shipping an
// agent-setup command, and returns a config that can see it. Modelled on
// seedRemoteFixture; separate because this one needs a commands section and
// nothing else needs one.
func seedRemoteSetupCommand(t *testing.T, marker string) *config.Config {
	t.Helper()
	testsupport.Isolate(t)

	repoDir := filepath.Join(t.TempDir(), "source")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".ctxloom", "content", "bundles"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(repoDir, ".ctxloom", "content", "bundles", "tools.yaml"),
		[]byte("version: 1.0.0\ndescription: remote tools bundle\ncommands:\n  agent-setup:\n    content: \""+marker+"\"\n"),
		0o644))
	_, err = wt.Add(".ctxloom/content/bundles/tools.yaml")
	require.NoError(t, err)
	commit, err := wt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	lm := remote.NewLockfileManager(appDir)
	lock, err := lm.Load()
	require.NoError(t, err)
	repoURL := "file://" + repoDir
	lock.AddEntry(remote.ItemTypeBundle, repoURL+"@bundles/tools",
		remote.LockEntry{SHA: commit.String(), URL: repoURL, FetchedAt: time.Now().UTC()})
	require.NoError(t, lm.Save(lock))

	return config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
}

// TestResolveSetupPrompt_WithholdsUnreviewedRemoteGuidance is the reason this
// path was changed: agent-setup text used to resolve through AdmitAll, so an
// UNREVIEWED REMOTE bundle's guidance reached the engine at init without anyone
// having looked at it. Every sibling bundle surface was already gated.
//
// The built-in must still be delivered whole -- withholding one contribution
// must never block setup, which is why the composition falls back rather than
// failing.
func TestResolveSetupPrompt_WithholdsUnreviewedRemoteGuidance(t *testing.T) {
	cfg := seedRemoteSetupCommand(t, "UNREVIEWED-REMOTE-GUIDANCE")

	got := ResolveSetupPrompt(cfg, "BUILTIN-DEFAULT")

	assert.NotContains(t, got, "UNREVIEWED-REMOTE-GUIDANCE",
		"an unreviewed remote bundle's agent-setup text reached the init prompt ungated")
	assert.Contains(t, got, "BUILTIN-DEFAULT",
		"withholding a contribution must not block setup; the built-in still composes")
}

// TestResolveSetupPrompt_LocalGuidanceStillAdmitted pins the other arm, and is
// what makes gating cheap: the decision function auto-allows local, builtin and
// trusted-signer content, so a project's OWN agent-setup command still
// contributes. A gate that withheld everything would satisfy the test above and
// break init for every user.
func TestResolveSetupPrompt_LocalGuidanceStillAdmitted(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	writeRegenBundle(t, appDir, "onboarding", `version: "1.0"
commands:
  agent-setup:
    content: "LOCAL-PROJECT-GUIDANCE"
`)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	got := ResolveSetupPrompt(cfg, "BUILTIN-DEFAULT")

	assert.Contains(t, got, "LOCAL-PROJECT-GUIDANCE",
		"a project's own agent-setup guidance must still be admitted")
	assert.Contains(t, got, "BUILTIN-DEFAULT")
}
