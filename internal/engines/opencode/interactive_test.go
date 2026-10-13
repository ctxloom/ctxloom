package opencode

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func tuiSession(t *testing.T) engine.Session {
	t.Helper()
	s := session(t, qwenBody())
	s.Mode = engine.Interactive
	s.SocketRoot = filepath.Join(t.TempDir(), "sock")
	return s
}

// THE TUI (golden): the bare TUI on a pty, the prompt a flag value (inline,
// so a leading "-" stays its value), nothing on stdin; the argv parses
// against the interactive grammar and is refused by the structured one.
func TestExec_InteractiveArgvIsTheTUI(t *testing.T) {
	s := tuiSession(t)
	s.Prompt = "-do the thing"
	ex := mustExec(t, bind(t, s))
	assert.Equal(t, []string{"--model", "ctxloom/qwen3.8-27b", "--prompt=-do the thing"}, ex.Args)
	assert.True(t, ex.Interactive)
	assert.Nil(t, ex.StdinPrompt)
	p, err := interactiveGrammar.ParseArgv(ex.Args)
	require.NoError(t, err)
	assert.Equal(t, "-do the thing", p.Flags[flagPrompt])
	_, err = structuredGrammar.ParseArgv(ex.Args)
	require.ErrorIs(t, err, engine.ErrArgv)

	s.Prompt = ""
	s.Permission = engine.PermissionPolicy{Posture: engine.Posture{Engine: EngineName, Document: map[string]any{keyMode: modeAuto}}}
	ex = mustExec(t, bind(t, s))
	assert.Equal(t, []string{"--model", "ctxloom/qwen3.8-27b", "--auto"}, ex.Args, "no prompt, no flag; the auto posture approves the TUI's asks")
	_, err = interactiveGrammar.ParseArgv(ex.Args)
	require.NoError(t, err)
}

// The TUI runs over the structured run's env; only the snapshot default
// follows the mode (Q-J: on, for /undo), the label's value still wins, and
// the TUI alone names the wake socket the bridge plugin listens on.
func TestExec_InteractiveEnvIsTheStructuredEnv(t *testing.T) {
	s := tuiSession(t)
	tui := mustExec(t, bind(t, s))
	s.Mode = engine.Structured
	structured := mustExec(t, bind(t, s))
	assert.Equal(t, true, configDoc(t, tui)["snapshot"])
	assert.Equal(t, false, configDoc(t, structured)["snapshot"])
	delete(tui.Env, envConfigContent)
	delete(structured.Env, envConfigContent)
	assert.Equal(t, filepath.Join(s.SocketRoot, "wake.sock"), tui.Env[wake.EnvVar], "the owner's wake, in its own socket root")
	assert.DirExists(t, s.SocketRoot, "the root is made for the bridge plugin to listen in")
	assert.NotContains(t, structured.Env, wake.EnvVar, "a structured run has nobody to wake")
	delete(tui.Env, wake.EnvVar)
	assert.Equal(t, structured.Env, tui.Env)

	body := qwenBody()
	body["snapshot"] = false
	s = session(t, body)
	s.Mode = engine.Interactive
	assert.Equal(t, false, configDoc(t, mustExec(t, bind(t, s)))["snapshot"], "an explicit snapshot wins over the mode")
}

// RESUME (§9.5): the TUI continues the key Resume set, else the launch's
// native key, else — relaunching the same ctxloom session — its newest
// conversation; a fresh launch names none. A structured Exec never
// --continues: its key arrives per turn.
func TestExec_InteractiveResume(t *testing.T) {
	cases := []struct {
		name   string
		resume sessions.ResumeRef
		key    string
		want   []string
	}{
		{"fresh", sessions.ResumeRef{}, "", nil},
		{"relaunch without a key", sessions.ResumeRef{Harp: "h"}, "", []string{flagContinue}},
		{"the launch's key", sessions.ResumeRef{Harp: "h", NativeKey: "ses_l"}, "", []string{flagSession, "ses_l"}},
		{"Resume's key wins", sessions.ResumeRef{Harp: "h", NativeKey: "ses_l"}, "ses_r", []string{flagSession, "ses_r"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := tuiSession(t)
			s.Prompt = ""
			s.Resume = c.resume
			inst := bind(t, s)
			if c.key != "" {
				require.NoError(t, inst.Resume(c.key))
			}
			ex := mustExec(t, inst)
			assert.Equal(t, append([]string{"--model", "ctxloom/qwen3.8-27b"}, c.want...), ex.Args)
			_, err := interactiveGrammar.ParseArgv(ex.Args)
			require.NoError(t, err)
		})
	}
	s := session(t, qwenBody())
	s.Resume = sessions.ResumeRef{Harp: "h", NativeKey: "ses_l"}
	assert.Equal(t, []string{"run", "--format", "json", "--thinking", "--model", "ctxloom/qwen3.8-27b"}, mustExec(t, bind(t, s)).Args)
}

// THE TUI PREFERENCES (Q-I): the user's tui.json and tui.jsonc, read under
// their XDG config base, land verbatim in the session home; one the user
// has since removed goes; a missing base or an unreadable file warns and
// carries nothing; no session home is refused.
func TestTUIConfigCarry(t *testing.T) {
	root := safefs.New()
	fsys := root.Fs
	dir := t.TempDir()
	home, xdg := filepath.Join(dir, "session", "home"), filepath.Join(dir, "xdg")
	require.NoError(t, fsys.MkdirAll(home, 0o700))
	require.NoError(t, fsys.MkdirAll(filepath.Join(xdg, "opencode"), 0o700))
	carry := tuiConfigCarry{getenv: func(k string) string {
		return map[string]string{"XDG_CONFIG_HOME": xdg}[k]
	}}
	req := engine.InstanceConfigRequest{HostHome: dir, InstanceHome: home}
	testsupport.WriteFile(t, fsys, filepath.Join(xdg, "opencode", "tui.json"), []byte(`{"theme":"gruvbox"}`), 0o644)
	testsupport.WriteFile(t, fsys, filepath.Join(xdg, "opencode", "tui.jsonc"), nil, 0o644)
	testsupport.WriteFile(t, fsys, filepath.Join(xdg, "opencode", "opencode.json"), []byte(`{}`), 0o644)

	rep, err := carry.WriteInstanceConfig(req, root)
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(home, "tui.json"), filepath.Join(home, "tui.jsonc")}, rep.Wrote)
	assert.Empty(t, rep.Warnings)
	got, err := afero.ReadFile(fsys, filepath.Join(home, "tui.json"))
	require.NoError(t, err)
	assert.Equal(t, `{"theme":"gruvbox"}`, string(got))
	fi, err := fsys.Stat(filepath.Join(home, "tui.json"))
	require.NoError(t, err)
	assert.Equal(t, safefs.PrivateFileMode, fi.Mode().Perm())
	ok, _ := afero.Exists(fsys, filepath.Join(home, "opencode.json"))
	assert.False(t, ok, "only the TUI preferences are carried")

	require.NoError(t, fsys.Remove(filepath.Join(xdg, "opencode", "tui.jsonc")))
	require.NoError(t, fsys.Remove(filepath.Join(xdg, "opencode", "tui.json")))
	require.NoError(t, fsys.MkdirAll(filepath.Join(xdg, "opencode", "tui.json"), 0o700))
	rep, err = carry.WriteInstanceConfig(req, root)
	require.NoError(t, err)
	assert.Empty(t, rep.Wrote)
	require.Len(t, rep.Warnings, 1, "an unreadable tui.json warns")
	ok, _ = afero.Exists(fsys, filepath.Join(home, "tui.jsonc"))
	assert.False(t, ok, "a copy the user removed goes")

	none := tuiConfigCarry{getenv: func(string) string { return "" }}
	rep, err = none.WriteInstanceConfig(engine.InstanceConfigRequest{InstanceHome: home}, root)
	require.NoError(t, err)
	assert.Len(t, rep.Warnings, 1, "no base to read: a warning, never a refusal")

	_, err = carry.WriteInstanceConfig(engine.InstanceConfigRequest{}, root)
	require.Error(t, err)
}

// The kind wires the carry as its instance config.
func TestHome_CarriesTheTUIConfig(t *testing.T) {
	assert.IsType(t, tuiConfigCarry{}, Opencode{}.Home().InstanceConfig)
}

// The TUI's socket root is there for the bridge plugin to listen in while
// opencode runs, and goes with it: no directory is left behind per session.
func TestExecute_TheSocketRootLivesAsLongAsTheTUI(t *testing.T) {
	s := tuiSession(t)
	var during bool
	e, err := Build()
	require.NoError(t, err)
	b := e.(agent.Hosted).Backend(func(_ context.Context, _ agent.LaunchSpec, _ io.Reader, _, _ io.Writer, _ <-chan agent.WindowSize) (int32, error) {
		_, err := os.Stat(s.SocketRoot)
		during = err == nil
		return 0, nil
	})
	_, err = b.Execute(context.Background(), &agent.ExecuteRequest{Mode: agent.ModeInteractive, Session: &s}, io.Discard, io.Discard)
	require.NoError(t, err)
	assert.True(t, during, "the root exists while opencode runs")
	assert.NoDirExists(t, s.SocketRoot, "and goes when it ends")
}
