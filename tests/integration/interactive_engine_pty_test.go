//go:build integration && !windows

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// An interactive engine is hosted on a plain pty by the runner itself, so an
// interactive `ctxloom run` needs no terminal multiplexer on the host.
//
// This is the binary-level journey that reaches runner.RunLaunchSpec's
// interactive branch. The mock engine cannot stand in for it: its backend
// answers in-process and never calls the injected launcher, so every
// mock-driven pty test stays green whatever that branch does. The engine here
// is the claude-code backend running a fake `claude` binary.

// fakeInteractiveClaudeBody reports claude's version floor (checked before
// launch), says whether it was handed a terminal, echoes one typed line, and
// exits cleanly. A run reports any engine failure, and any launch failure, as
// a non-zero exit, so only an engine that ran and ended cleanly exits 0.
const fakeInteractiveClaudeBody = `#!/bin/sh
case "$1" in --version) echo "%s (Claude Code)"; exit 0;; esac
if [ -t 0 ] && [ -t 1 ]; then echo "FAKE-ENGINE-ON-A-TTY"; fi
echo "FAKE-ENGINE-READY"
IFS= read -r line
echo "FAKE-ENGINE-GOT:$line"
exit 0
`

// pathWithout returns the process PATH with every executable called name
// hidden. An entry holding one is replaced by a mirror of its OTHER entries,
// so the rest of the toolchain an interactive run needs stays reachable.
func pathWithout(t *testing.T, name string) string {
	t.Helper()
	sep := string(os.PathListSeparator)
	var kept []string
	for _, dir := range strings.Split(os.Getenv("PATH"), sep) {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			kept = append(kept, dir)
			continue
		}
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		mirror := t.TempDir()
		for _, e := range entries {
			if e.Name() == name {
				continue
			}
			require.NoError(t, os.Symlink(filepath.Join(dir, e.Name()), filepath.Join(mirror, e.Name())))
		}
		kept = append(kept, mirror)
	}
	for _, dir := range kept {
		_, err := os.Stat(filepath.Join(dir, name))
		require.True(t, os.IsNotExist(err), "%s is still reachable through %s", name, dir)
	}
	return strings.Join(kept, sep)
}

func TestRunPTY_InteractiveEngineRunsOnAPlainPtyWithoutTmux(t *testing.T) {
	env := setupTestEnv(t)
	writeFragment(t, env, "rules", []string{"rules"}, "Project rules for the session.")
	writeProfile(t, env, "dev", "description: dev\nbundles:\n  - local#fragments/rules\n")
	writeHostClaudeCredential(t, env)

	e, ok := engines.Registry().Lookup(claude.EngineName)
	require.True(t, ok, "claude is composed")
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"),
		[]byte(fmt.Sprintf(fakeInteractiveClaudeBody, e.Root().Version.Floor)), 0o755))
	// A login shell that fails: binary resolution falls back to the user's
	// login-shell PATH (shellenv), which would otherwise find the host's tmux
	// again behind the PATH this test hides it from.
	noShell := filepath.Join(t.TempDir(), "no-login-shell")
	require.NoError(t, os.WriteFile(noShell, []byte("#!/bin/sh\nexit 1\n"), 0o755))

	env.SetChildEnv("PATH", bin+string(os.PathListSeparator)+pathWithout(t, "tmux"))
	env.SetChildEnv("SHELL", noShell)
	env.SetChildEnv("CLAUDE_CODE_OAUTH_TOKEN", "")
	env.SetChildEnv("ANTHROPIC_API_KEY", "")
	env.SetChildEnv("CLAUDE_CONFIG_DIR", "")
	env.SetChildEnv(secureStorageEnv, "")

	_ = env.Run("agent", "create", "dev", "--profiles", "dev", "--llm", "claude-code", "--auth", "login", "--permissions", "plan")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())

	sess, err := env.RunPTY(ptyCols, ptyRows, nil, "run", "--agent", "dev")
	require.NoError(t, err)
	defer sess.Close()

	ready := sess.WaitForOutput(ptyRunTimeout, func(out string) bool { return strings.Contains(out, "FAKE-ENGINE-READY") })
	require.True(t, ready, "the engine never started; captured so far: %q", sess.Output())
	assert.Contains(t, sess.Output(), "FAKE-ENGINE-ON-A-TTY", "the engine's stdin and stdout are a terminal")

	_, err = sess.Write([]byte("typed-through-the-pty\r"))
	require.NoError(t, err)
	echoed := sess.WaitForOutput(ptyRunTimeout, func(out string) bool {
		return strings.Contains(out, "FAKE-ENGINE-GOT:typed-through-the-pty")
	})
	require.True(t, echoed, "a typed line never reached the engine; captured so far: %q", sess.Output())

	exited, _ := sess.Wait(ptyRunTimeout)
	require.True(t, exited, "ctxloom run did not exit within %s; captured so far: %q", ptyRunTimeout, sess.Output())
	assert.Equal(t, 0, sess.ExitCode(), "the run ends with the engine's clean exit; captured: %q", sess.Output())
}
