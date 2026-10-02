package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// launchPath is one way claude is started: the interactive session, the
// print (-p) run, either of them resumed, and the structured (stream-json)
// turn the runner drives.
type launchPath struct {
	name   string
	mode   engine.Mode
	resume string
	turn   bool
}

var launchPaths = []launchPath{
	{name: "interactive", mode: engine.Interactive},
	{name: "interactive resumed", mode: engine.Interactive, resume: "native-1"},
	{name: "print", mode: engine.Structured},
	{name: "print resumed", mode: engine.Structured, resume: "native-1"},
	{name: "stream-json turn", mode: engine.Structured, turn: true},
	{name: "stream-json turn resumed", mode: engine.Structured, resume: "native-1", turn: true},
}

// launchArgv is the argv claude is started with on path, for a session
// whose repository verdict is trust.
func launchArgv(t *testing.T, path launchPath, trust engine.WorkspaceTrust, presented ...present.Presentation) ([]string, error) {
	t.Helper()
	kind, err := Build()
	require.NoError(t, err)
	inst, err := kind.Instance(engine.Session{
		Identity: sessions.Identity{Harp: "h"}, Label: engine.LabelConfig{Label: EngineName},
		Mode: path.mode, Permission: modePolicy(modeDefault), Prompt: "p", Trust: trust,
		Roots: present.Paths{ProjectRoot: present.Root{Host: "/host/p", Engine: "/p"}},
	})
	require.NoError(t, err)
	if path.resume != "" {
		require.NoError(t, inst.Resume(path.resume))
	}
	ex, err := inst.Exec(presented)
	if err != nil || !path.turn {
		return ex.Args, err
	}
	return (&streamJSONDriver{inst: inst.(*instance)}).argv(ex, engine.Turn{Prompt: "p"})
}

// An untrusted repository's session reads only the user source — the
// session home, where ctxloom's own hooks and settings live — and only the
// MCP servers ctxloom names on --mcp-config, on EVERY launch path: the
// repository's committed settings, hooks, agents' frontmatter and .mcp.json
// do not load, whoever started claude and however.
func TestLaunch_UntrustedRepoLoadsOnlyUserSources(t *testing.T) {
	for _, path := range launchPaths {
		for _, trust := range []engine.WorkspaceTrust{engine.TrustUntrusted, engine.TrustTrusted + 1} {
			args, err := launchArgv(t, path, trust)
			require.NoError(t, err, path.name)
			assert.True(t, argPair(args, flagSettingSources, "user"), "%s, trust=%d: %v", path.name, trust, args)
			assert.Equal(t, 1, countFlag(args, flagSettingSources), "%s: the sources are named once", path.name)
			assert.Equal(t, 1, countFlag(args, flagStrictMCPConfig), "%s: %v", path.name, args)
		}
	}
}

// A trusted repository keeps claude's default sources on every launch path:
// its own settings, hooks and MCP servers load, as they would in the
// human's own claude.
func TestLaunch_TrustedRepoKeepsDefaultSources(t *testing.T) {
	for _, path := range launchPaths {
		args, err := launchArgv(t, path, engine.TrustTrusted)
		require.NoError(t, err, path.name)
		assert.NotContains(t, args, flagSettingSources, path.name)
		assert.NotContains(t, args, flagStrictMCPConfig, path.name)
	}
}

// A --settings flag is a source --setting-sources does not filter, so a
// presentation naming a settings file (the unsafe-file settings form names
// <project>/.claude/settings.json, which the repository commits) would load
// the repository's own hooks into an untrusted session. It is refused on
// every launch path rather than run with the repository's hooks or without
// ctxloom's.
func TestLaunch_UntrustedRefusesAPresentedSettingsFile(t *testing.T) {
	file := present.Presentation{Args: []string{flagSettings, "/p/.claude/settings.json"}}
	for _, path := range launchPaths {
		_, err := launchArgv(t, path, engine.TrustUntrusted, file)
		assert.ErrorIs(t, err, errUntrustedSettingsPresented, path.name)
	}
}

// The unsafe-file MCP approach writes ctxloom's servers into the project's
// own .mcp.json and names no flag. --strict-mcp-config makes claude ignore
// that file, so an untrusted session would launch without ctxloom's
// endpoint, silently; loading it instead would load the servers the
// repository committed there. Refused on every launch path; a trusted
// session reads it as claude always has.
func TestLaunch_UntrustedRefusesTheProjectMCPFile(t *testing.T) {
	file := present.Presentation{HostPath: "/host/p/.mcp.json", EnginePath: "/p/.mcp.json"}
	for _, path := range launchPaths {
		_, err := launchArgv(t, path, engine.TrustUntrusted, file)
		assert.ErrorIs(t, err, errUntrustedProjectMCP, path.name)
		_, err = launchArgv(t, path, engine.TrustTrusted, file)
		assert.NoError(t, err, path.name)
	}
	elsewhere := present.Presentation{HostPath: "/home/s/.mcp.json", EnginePath: "/home/s/.mcp.json", Args: []string{flagMCPConfig, "/home/s/.mcp.json"}}
	_, err := launchArgv(t, launchPaths[0], engine.TrustUntrusted, elsewhere)
	assert.NoError(t, err, "the session-home MCP file rides --mcp-config, which strict mode keeps")
}

// The structured turn's posture is its one --settings whatever the verdict:
// a presentation naming another is refused even when the posture has
// nothing to say (bypass), which the second-settings check alone let
// through.
func TestTurnArgv_RefusesAPresentedSettingsFile(t *testing.T) {
	file := present.Presentation{Args: []string{flagSettings, "/p/.claude/settings.json"}}
	_, err := headlessTurnArgv(t, modePolicy(modeBypass), engine.Turn{}, file)
	assert.ErrorIs(t, err, errTurnSettingsPresented)
}

// --- the verdict: claude's own record, read the way claude reads it ---

// hostWithProjects writes a host home whose .claude.json carries projects.
func hostWithProjects(t *testing.T, projects map[string]any) string {
	t.Helper()
	home := t.TempDir()
	data, err := json.Marshal(map[string]any{"projects": projects})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, hostConfigRelPath), data, 0o600))
	return home
}

func accepted(v bool) map[string]any { return map[string]any{"hasTrustDialogAccepted": v} }

// gitRepo makes dir a repository root (a .git directory).
func gitRepo(t *testing.T, dir string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	return dir
}

func verdict(t *testing.T, host, workDir string) engine.WorkspaceTrust {
	t.Helper()
	v, err := claudeRepoTrust{}.Verdict(nil, engine.TrustQuery{HostHome: host, WorkDir: workDir})
	require.NoError(t, err)
	return v
}

// The repository root the human trusted in their own claude trusts a run
// anywhere beneath it.
func TestVerdict_TheTrustedRootTrustsItsSubdirectories(t *testing.T) {
	root := gitRepo(t, t.TempDir())
	sub := filepath.Join(root, "a", "b")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	host := hostWithProjects(t, map[string]any{root: accepted(true)})
	assert.Equal(t, engine.TrustTrusted, verdict(t, host, root))
	assert.Equal(t, engine.TrustTrusted, verdict(t, host, sub))
}

// A directory the human never trusted is untrusted, and so is one whose
// recorded answer is false.
func TestVerdict_NoAnswerOrAFalseOneIsUntrusted(t *testing.T) {
	root := gitRepo(t, t.TempDir())
	assert.Equal(t, engine.TrustUntrusted, verdict(t, hostWithProjects(t, map[string]any{}), root))
	assert.Equal(t, engine.TrustUntrusted, verdict(t, hostWithProjects(t, map[string]any{root: accepted(false)}), root))
	assert.Equal(t, engine.TrustUntrusted, verdict(t, hostWithProjects(t, map[string]any{root: map[string]any{"hasTrustDialogAccepted": "true"}}), root),
		"only the boolean true is an acceptance")
}

// claude bounds its walk at the repository root: trusting a directory
// ABOVE a repository does not trust the repository, which is how a cloned
// repo under a trusted ~/src stays untrusted.
func TestVerdict_TrustAboveTheRepositoryDoesNotReachIntoIt(t *testing.T) {
	parent := t.TempDir()
	root := gitRepo(t, filepath.Join(parent, "cloned"))
	host := hostWithProjects(t, map[string]any{parent: accepted(true)})
	assert.Equal(t, engine.TrustUntrusted, verdict(t, host, root))
}

// Outside any repository claude walks every ancestor.
func TestVerdict_OutsideARepositoryEveryAncestorCounts(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "plain", "dir")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	host := hostWithProjects(t, map[string]any{parent: accepted(true)})
	assert.Equal(t, engine.TrustTrusted, verdict(t, host, dir))
}

// A worktree is the repository's: claude keys its project by the canonical
// root (the main checkout the common dir belongs to), so a trusted main
// checkout trusts a delegated child's worktree of it.
func TestVerdict_AWorktreeTakesItsMainCheckoutsAnswer(t *testing.T) {
	main := gitRepo(t, t.TempDir())
	admin := filepath.Join(main, ".git", "worktrees", "wt")
	require.NoError(t, os.MkdirAll(admin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte("../..\n"), 0o644))
	wt := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+admin+"\n"), 0o644))

	assert.Equal(t, engine.TrustTrusted, verdict(t, hostWithProjects(t, map[string]any{main: accepted(true)}), wt))
	assert.Equal(t, engine.TrustUntrusted, verdict(t, hostWithProjects(t, map[string]any{}), wt))
}

// No host home, or no host config, is no answer: untrusted, not an error.
func TestVerdict_NoHostConfigIsUntrusted(t *testing.T) {
	root := gitRepo(t, t.TempDir())
	assert.Equal(t, engine.TrustUntrusted, verdict(t, "", root))
	assert.Equal(t, engine.TrustUntrusted, verdict(t, t.TempDir(), root))
}

// A host config that does not parse is an error: the verdict is not
// guessed, and the caller fails closed on it.
func TestVerdict_AnUnreadableHostConfigIsAnError(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, hostConfigRelPath), []byte("{not json"), 0o600))
	_, err := claudeRepoTrust{}.Verdict(afero.NewOsFs(), engine.TrustQuery{HostHome: home, WorkDir: gitRepo(t, t.TempDir())})
	require.Error(t, err)
}

// claude declares its trust: the verdict is its own record's.
func TestClaude_DeclaresRepoTrust(t *testing.T) {
	kind, err := Build()
	require.NoError(t, err)
	_, ok := kind.Trust().Get()
	assert.True(t, ok)
}

// --- the instance config: the answer is copied only for a trusted repository ---

// An untrusted repository's session home carries no trust answer for it:
// ctxloom does not answer claude's trust prompt on the human's behalf.
func TestWriteInstanceConfig_UntrustedSeedsNoTrustAnswer(t *testing.T) {
	host := writeHostConfig(t, realisticHostClaudeJSON)
	instance := t.TempDir()
	workDir := t.TempDir()
	_, err := claudeInstanceConfig{}.WriteInstanceConfig(engine.InstanceConfigRequest{
		HostHome: host, InstanceHome: instance, WorkDir: workDir, Trust: engine.TrustUntrusted,
	}, nil)
	require.NoError(t, err)
	cfg := readInstanceConfig(t, instance)
	projects, _ := cfg["projects"].(map[string]any)
	for dir, e := range projects {
		entry, _ := e.(map[string]any)
		assert.NotEqual(t, true, entry["hasTrustDialogAccepted"], "untrusted, yet %s carries a trust answer", dir)
	}
}

// A trusted repository's session home carries the human's answer for the
// directory the run works in, so claude does not re-ask what the human's
// own claude already settled.
func TestWriteInstanceConfig_TrustedCopiesTheAnswer(t *testing.T) {
	host := writeHostConfig(t, realisticHostClaudeJSON)
	instance := t.TempDir()
	workDir := t.TempDir()
	_, err := claudeInstanceConfig{}.WriteInstanceConfig(engine.InstanceConfigRequest{
		HostHome: host, InstanceHome: instance, WorkDir: workDir, Trust: engine.TrustTrusted,
	}, nil)
	require.NoError(t, err)
	projects, _ := readInstanceConfig(t, instance)["projects"].(map[string]any)
	entry, _ := projects[workDir].(map[string]any)
	assert.Equal(t, true, entry["hasTrustDialogAccepted"])
}
