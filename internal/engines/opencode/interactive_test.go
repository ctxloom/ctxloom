package opencode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

func tuiSession(t *testing.T) engine.Session {
	t.Helper()
	s := session(t, qwenBody())
	s.Mode = engine.Interactive
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
// follows the mode (Q-J: on, for /undo), and the label's value still wins.
func TestExec_InteractiveEnvIsTheStructuredEnv(t *testing.T) {
	s := tuiSession(t)
	tui := mustExec(t, bind(t, s))
	s.Mode = engine.Structured
	structured := mustExec(t, bind(t, s))
	assert.Equal(t, true, configDoc(t, tui)["snapshot"])
	assert.Equal(t, false, configDoc(t, structured)["snapshot"])
	delete(tui.Env, envConfigContent)
	delete(structured.Env, envConfigContent)
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
