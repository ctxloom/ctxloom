package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// The engine-home policy moved codex's project-scoped home out of the project
// root, which means every checkout that ran an older ctxloom is carrying a
// <WorkDir>/.codex holding real user property: a hand-edited config.toml,
// prompts, skills, the seeded auth.json, and codex's own accumulated sessions.
// The ruling (2026-08-11) was to MOVE it rather than start fresh beside it —
// starting fresh silently drops a user's engine configuration, which is this
// project's signature failure mode.
//
// These tests assert PAYLOAD, not just placement: the bytes at the new home,
// the foreign key a user hand-added, the mode on the credential file. Each one
// guards its own comparison with a non-empty precondition, so a fixture that
// silently wrote nothing cannot make "the bytes match" true by vacuity.

// legacyFixture writes a realistic pre-relocation <workDir>/.codex and returns
// the workDir. Every file carries content a byte comparison can fail on.
func legacyFixture(t *testing.T, workDir string) {
	t.Helper()
	legacy := legacyProjectHome(workDir)
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, PromptsDirName), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, SkillsDirName, "humanize"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, "sessions"), 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(legacy, ConfigFileName),
		[]byte("model = 'o3'\napproval_policy = 'on-request'\n\n[hooks]\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, PromptsDirName, "team-onboarding.md"),
		[]byte("# onboarding\nhand written\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, SkillsDirName, "humanize", "SKILL.md"),
		[]byte("---\nname: humanize\n---\nBody.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, AuthFileName),
		[]byte(`{"tokens":{"access_token":"fixture"}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "sessions", "rollout-1.jsonl"),
		[]byte("{\"kind\":\"turn\"}\n"), 0o644))
}

// requireBytes reads path and fails unless it holds something — the empty-source
// guard. Without it a migration that moved zero bytes would satisfy every
// "contents are equal" assertion below.
func requireBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "%s must exist", path)
	require.NotEmpty(t, data, "%s is empty; a byte comparison against it would be vacuous", path)
	return data
}

// TestMigrateLegacyHome_MovesEveryByte is the legacy-only case: the whole
// directory arrives at the new home with its contents intact — including a
// config.toml key ctxloom has never heard of, which is the one thing a
// "start fresh, warn the user" migration would have destroyed.
func TestMigrateLegacyHome_MovesEveryByte(t *testing.T) {
	workDir := t.TempDir()
	legacyFixture(t, workDir)

	before := map[string][]byte{}
	for _, rel := range []string{
		ConfigFileName,
		filepath.Join(PromptsDirName, "team-onboarding.md"),
		filepath.Join(SkillsDirName, "humanize", "SKILL.md"),
		AuthFileName,
		filepath.Join("sessions", "rollout-1.jsonl"),
	} {
		before[rel] = requireBytes(t, filepath.Join(legacyProjectHome(workDir), rel))
	}

	stderr := captureStderr(t, func() {
		home := resolveInTreeHome(afero.NewOsFs(), workDir)
		assert.Equal(t, StateHome(workDir), home)
	})

	newHome := ProjectHome(workDir)
	for rel, want := range before {
		assert.Equal(t, string(want), string(requireBytes(t, filepath.Join(newHome, rel))),
			"%s must arrive byte-for-byte at the new home", rel)
	}
	assert.Contains(t, string(requireBytes(t, filepath.Join(newHome, ConfigFileName))), "approval_policy",
		"a foreign config.toml key the user set by hand survives the move")

	// The credential keeps its owner-only mode: a widened copy would hand away
	// the whole reason this home lives in the state tier.
	info, err := os.Stat(filepath.Join(newHome, AuthFileName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "auth.json must stay owner-only through the move")

	// The emptied legacy directory is gone, and the move is announced.
	_, err = os.Stat(legacyProjectHome(workDir))
	assert.True(t, os.IsNotExist(err), "the legacy home must not survive the move")
	assert.Contains(t, stderr, legacyProjectHome(workDir), "the note names where the files came FROM")
	assert.Contains(t, stderr, newHome, "the note names where the files went TO")
}

// TestMigrateLegacyHome_FreshProjectSaysNothing: the ordinary case. No legacy
// directory means no move, no warning, and above all nothing created at the
// legacy path — a migration that helpfully mkdir'd its own source would
// reintroduce the directory the policy just retired.
func TestMigrateLegacyHome_FreshProjectSaysNothing(t *testing.T) {
	workDir := t.TempDir()

	stderr := captureStderr(t, func() {
		assert.Equal(t, StateHome(workDir), resolveInTreeHome(afero.NewOsFs(), workDir))
	})

	assert.Empty(t, stderr, "a project with no legacy home has nothing to report")
	_, err := os.Stat(legacyProjectHome(workDir))
	assert.True(t, os.IsNotExist(err), "nothing may create the legacy path")
}

// TestMigrateLegacyHome_BothExistMovesNothing: two homes is the case this
// function must NOT resolve on its own. Merging two config.tomls is not
// something it can do correctly and picking one silently would discard the
// other, so it moves nothing, leaves both intact, and says out loud which one
// codex now reads.
func TestMigrateLegacyHome_BothExistMovesNothing(t *testing.T) {
	workDir := t.TempDir()
	legacyFixture(t, workDir)
	legacyBytes := requireBytes(t, filepath.Join(legacyProjectHome(workDir), ConfigFileName))

	newHome := ProjectHome(workDir)
	require.NoError(t, os.MkdirAll(newHome, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(newHome, ConfigFileName), []byte("model = 'gpt-5'\n"), 0o644))
	currentBytes := requireBytes(t, filepath.Join(newHome, ConfigFileName))

	stderr := captureStderr(t, func() {
		resolveInTreeHome(afero.NewOsFs(), workDir)
	})

	assert.Equal(t, string(legacyBytes), string(requireBytes(t, filepath.Join(legacyProjectHome(workDir), ConfigFileName))),
		"the legacy home is left exactly as it was")
	assert.Equal(t, string(currentBytes), string(requireBytes(t, filepath.Join(newHome, ConfigFileName))),
		"the current home is left exactly as it was")
	assert.Contains(t, stderr, legacyProjectHome(workDir), "the warning names the legacy home")
	assert.Contains(t, stderr, newHome, "the warning names the current home, which is the live one")
}

// TestMigrateLegacyHome_SymlinkIsNeverFollowed: in this repo's own checkout
// .codex has been a symlink to the real ~/.codex. Following one would move a
// developer's entire global codex home into a project's state directory.
func TestMigrateLegacyHome_SymlinkIsNeverFollowed(t *testing.T) {
	workDir := t.TempDir()
	elsewhere := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(elsewhere, ConfigFileName), []byte("model = 'o3'\n"), 0o644))
	require.NoError(t, os.Symlink(elsewhere, legacyProjectHome(workDir)))

	stderr := captureStderr(t, func() {
		resolveInTreeHome(afero.NewOsFs(), workDir)
	})

	assert.Contains(t, stderr, "SYMLINK", "refusing to follow the link must be said out loud")
	assert.NotEmpty(t, requireBytes(t, filepath.Join(elsewhere, ConfigFileName)),
		"the symlink's target is untouched")
	link, err := os.Lstat(legacyProjectHome(workDir))
	require.NoError(t, err)
	assert.NotZero(t, link.Mode()&os.ModeSymlink, "the link itself survives; ctxloom does not delete what it will not read")
	_, err = os.Stat(ProjectHome(workDir))
	assert.True(t, os.IsNotExist(err), "nothing was moved into the new home")
}

// TestMigrateLegacyHome_RunsOnTheStaticWriterPath drives the migration through
// the entry point `ctxloom manage install` reaches — CodexHookWriter's
// SettingsWriter role — rather than through the shared helper directly. Two
// entry points call one function; a test that only ever calls the function
// proves neither of them does.
func TestMigrateLegacyHome_RunsOnTheStaticWriterPath(t *testing.T) {
	workDir := t.TempDir()
	legacyFixture(t, workDir)
	want := requireBytes(t, filepath.Join(legacyProjectHome(workDir), PromptsDirName, "team-onboarding.md"))

	w := &CodexHookWriter{FS: afero.NewOsFs()}
	captureStderr(t, func() {
		require.NoError(t, w.WriteSettings(&wire.HooksConfig{}, &wire.MCPConfig{
			Servers: map[string]wire.MCPServer{"srv": {Command: "run-srv"}},
		}, nil, workDir))
	})

	assert.Equal(t, string(want),
		string(requireBytes(t, filepath.Join(ProjectHome(workDir), PromptsDirName, "team-onboarding.md"))),
		"the static writer path migrates the user's prompts along with everything else")

	// The user's own config.toml keys survive the migration AND the write that
	// followed it: the write loads the migrated file rather than starting a new
	// one.
	cfg := requireBytes(t, filepath.Join(ProjectHome(workDir), ConfigFileName))
	assert.Contains(t, string(cfg), "approval_policy", "the migrated config.toml is what the writer merged into")
	assert.Contains(t, string(cfg), "run-srv", "and the write itself still landed")
}

// TestMigrateLegacyHome_RunsOnTheRunPath is the same proof for the other entry
// point: Codex.Setup.
func TestMigrateLegacyHome_RunsOnTheRunPath(t *testing.T) {
	workDir := t.TempDir()
	legacyFixture(t, workDir)
	want := requireBytes(t, filepath.Join(legacyProjectHome(workDir), AuthFileName))

	restoreSeed := stubSeedCodexHomeFn(t, func(string) (bool, error) { return true, nil })
	defer restoreSeed()

	b := NewCodex()
	captureStderr(t, func() {
		require.NoError(t, b.Setup(context.Background(), &agent.SetupRequest{WorkDir: workDir}))
	})

	assert.Equal(t, string(want), string(requireBytes(t, filepath.Join(ProjectHome(workDir), AuthFileName))),
		"the run path migrates the seeded credential rather than re-authenticating from scratch")
	_, err := os.Stat(legacyProjectHome(workDir))
	assert.True(t, os.IsNotExist(err), "the legacy home is gone after a run")
}

// TestSetup_InTreePreSeedsTrustForTheWorkingDirectory pins the trust half of
// the policy. The relocated home is one codex has never seen, so the
// `[projects."<abs cwd>"] trust_level = "trusted"` entry codex accumulates on
// its own is not in it — without a pre-seed codex re-prompts, or (under `codex
// exec`, which has nobody to ask) runs silently untrusted. The key is the
// WORKING DIRECTORY: codex keys trust by the cwd it runs in, so an entry naming
// the home would answer a question it never asks.
func TestSetup_InTreePreSeedsTrustForTheWorkingDirectory(t *testing.T) {
	workDir := t.TempDir()
	restoreSeed := stubSeedCodexHomeFn(t, func(string) (bool, error) { return true, nil })
	defer restoreSeed()

	b := NewCodex()
	require.NoError(t, b.Setup(context.Background(), &agent.SetupRequest{
		WorkDir: workDir,
		Managed: &agent.ManagedConfig{Hooks: &wire.HooksConfig{}},
	}))

	absWork, err := filepath.Abs(workDir)
	require.NoError(t, err)
	assert.Equal(t, absWork, b.resolvedTrustAbsPath)

	cfg := requireBytes(t, filepath.Join(ProjectHome(workDir), ConfigFileName))
	assert.Contains(t, string(cfg), absWork, "the trust entry names the working directory")
	assert.Contains(t, string(cfg), "trust_level", "and grants it")
	assert.NotContains(t, string(cfg), ProjectHome(workDir),
		"no trust entry may name the config HOME — codex keys trust by cwd")
}
