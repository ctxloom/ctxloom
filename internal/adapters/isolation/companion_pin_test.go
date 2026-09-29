package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

// pinWorld is a hermetic companion world: ctxloom's own PATH resolves
// companions in admitted (where some are signed and some are not), and the
// companion pin the host launch consults is the real PinAdmittedCompanions
// over a trust root that authorizes only the signing key used here.
type pinWorld struct {
	admitted string
}

func newPinWorld(t *testing.T, signed, unsigned []string) pinWorld {
	t.Helper()
	w := pinWorld{admitted: t.TempDir()}
	signers := filepath.Join(t.TempDir(), "allowed_signers")
	for _, name := range signed {
		p := writeCompanion(t, w.admitted, name, "#!/bin/sh\necho admitted "+name+"\n")
		testsupport.SignCompanionForTesting(t, p, signers)
	}
	for _, name := range unsigned {
		writeCompanion(t, w.admitted, name, "#!/bin/sh\necho unsigned "+name+"\n")
	}
	var root *allowedsigners.Store
	if len(signed) > 0 {
		var err error
		root, _, err = allowedsigners.ParseFile(signers)
		require.NoError(t, err)
	} else {
		root = allowedsigners.NewStore()
	}
	restore := companions.SetLookPathForTesting(func(bin string) (string, error) {
		p := filepath.Join(w.admitted, bin)
		if _, err := os.Stat(p); err != nil {
			return "", err
		}
		return p, nil
	})
	t.Cleanup(restore)
	store := t.TempDir()
	SetCompanionPin(func() (string, error) { return companions.PinAdmittedCompanions(store, root) })
	t.Cleanup(func() { SetCompanionPin(nil) })
	return w
}

func writeCompanion(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(body), 0o755)) //nolint:gosec // a fake companion must be executable
	return p
}

// envPath is the PATH a process started with env sees: the LAST assignment,
// which is the one os/exec keeps.
func envPath(env []string) string {
	path := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	return path
}

// resolveIn is PATH resolution over an explicit PATH string, by order alone:
// every candidate these tests write is executable, and an exec bit is not
// what makes a file runnable on Windows.
func resolveIn(t *testing.T, path, name string) string {
	t.Helper()
	for _, dir := range filepath.SplitList(path) {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	t.Fatalf("%s does not resolve on PATH %q", name, path)
	return ""
}

// TestHostRunnerEnv_AdmittedCompanionsLeadPath: the environment a host
// runner (and so the engine it launches) starts with resolves ltk and
// taskloom to the ADMITTED bytes, even though a shadowing binary of each name
// sits earlier on the PATH the runner would otherwise inherit. Both host spawn
// shapes are checked: the pty runner (RunnerCommand) and the plain one.
func TestHostRunnerEnv_AdmittedCompanionsLeadPath(t *testing.T) {
	w := newPinWorld(t, []string{"ltk", "taskloom"}, nil)
	shadow := t.TempDir()
	writeCompanion(t, shadow, "ltk", "#!/bin/sh\necho SHADOW\n")
	writeCompanion(t, shadow, "taskloom", "#!/bin/sh\necho SHADOW\n")
	t.Setenv("PATH", shadow+string(os.PathListSeparator)+os.Getenv("PATH"))

	plain, err := hostRunnerCmd(context.Background(), []string{"runner", "claude"}, map[string]string{"K": "V"})
	require.NoError(t, err)
	for label, env := range map[string][]string{
		"pty":   RunnerCommand("claude", nil).Env,
		"plain": plain.Env,
	} {
		path := envPath(env)
		first := filepath.SplitList(path)[0]
		assert.NotEqual(t, shadow, first, "%s: the inherited PATH must not lead", label)
		for _, name := range []string{"ltk", "taskloom"} {
			got, err := os.ReadFile(resolveIn(t, path, name)) //nolint:gosec // a path this test built
			require.NoError(t, err)
			want, err := os.ReadFile(filepath.Join(w.admitted, name)) //nolint:gosec // a path this test built
			require.NoError(t, err)
			assert.Equal(t, string(want), string(got), "%s: bare %s reaches the admitted bytes, never the shadow", label, name)
		}
		assert.Contains(t, path, shadow, "%s: the rest of the inherited PATH is kept, behind the pin", label)
	}
}

// TestHostRunnerEnv_NoPinLeavesPathAlone: with nothing admitted there is no
// pin, and the inherited PATH is passed through untouched.
func TestHostRunnerEnv_NoPinLeavesPathAlone(t *testing.T) {
	newPinWorld(t, nil, []string{"ltk"})
	t.Setenv("PATH", "/only/this")
	assert.Equal(t, "/only/this", envPath(RunnerCommand("claude", nil).Env))
}

// TestStageCompanions_StagesOnlyAdmittedWithSignature: the agent image gets
// the admitted binary and its signature; a present-but-unsigned companion is
// refused, and the refusal is said out loud.
func TestStageCompanions_StagesOnlyAdmittedWithSignature(t *testing.T) {
	w := newPinWorld(t, []string{"ltk"}, []string{"taskloom"})
	withRealCompanionLookPath(t)
	warn := captureWarnings(t)

	ctxDir := t.TempDir()
	require.NoError(t, stageCompanions(ctxDir))

	staged := filepath.Join(ctxDir, "companions")
	got, err := os.ReadFile(filepath.Join(staged, "ltk")) //nolint:gosec // a path this test built
	require.NoError(t, err)
	want, err := os.ReadFile(filepath.Join(w.admitted, "ltk")) //nolint:gosec // a path this test built
	require.NoError(t, err)
	assert.Equal(t, want, got, "the staged ltk is the admitted bytes")
	assert.FileExists(t, filepath.Join(staged, "ltk.sig"), "the signature is staged beside the binary")
	assert.FileExists(t, filepath.Join(staged, "ltk.release"), "the release statement the signature covers is staged too")
	info, err := os.Stat(filepath.Join(staged, "ltk"))
	require.NoError(t, err)
	fileperm.Equal(t, 0o755, info.Mode(), "staged companion is 0755 exactly, umask notwithstanding")

	assert.NoFileExists(t, filepath.Join(staged, "taskloom"), "an unverified companion is never staged")
	assert.NoFileExists(t, filepath.Join(staged, "reprise"), "an absent companion is never staged")
	assert.Contains(t, warn.String(), "taskloom", "the refusal is reported")
}

// TestAgentImage_StagedCompanionsLeadPath: inside the container the staged
// companions live in /usr/local/bin, and the image says so in its own PATH
// rather than trusting the base image's default order — a base (a devcontainer,
// a user Containerfile) can put anything ahead of it.
func TestAgentImage_StagedCompanionsLeadPath(t *testing.T) {
	for label, cf := range map[string]string{
		"agent":   string(composeAgentContainerfile("claude-code")),
		"overlay": string(overlayContainerfile("example/base:latest", "")),
	} {
		copyAt := strings.Index(cf, "COPY companions/ /usr/local/bin/")
		envAt := strings.Index(cf, `ENV PATH="/usr/local/bin:${PATH}"`)
		require.GreaterOrEqual(t, copyAt, 0, "%s stages companions", label)
		assert.Greater(t, envAt, copyAt, "%s: /usr/local/bin leads PATH once the companions are staged", label)
	}
}
