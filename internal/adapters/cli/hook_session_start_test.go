package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// TestHookSessionStart_AnUndecodablePayloadFails: a session_start payload the
// firing engine's codec cannot read is an error the process exits non-zero
// on, with nothing written for the engine to inject.
//
// MUTATION -- answer from an empty event when Decode fails -- turns this red.
func TestHookSessionStart_AnUndecodablePayloadFails(t *testing.T) {
	var out bytes.Buffer
	cmd := firedBy(t, &cobra.Command{}, claude.EngineName)
	cmd.SetIn(strings.NewReader("not json"))
	cmd.SetOut(&out)
	require.Error(t, hookSessionStartCmd.RunE(cmd, nil))
	assert.Empty(t, out.String())
}

// TestFiringEngine_WithoutAFlagIsTheRegistryDefault: a hook entry with no
// --engine (written by an earlier ctxloom, or by hand) reads and answers
// through the registry's default engine's codec — here the default's own
// payload decodes and the answer is its envelope.
//
// MUTATION -- refuse (or pick a non-default engine) when --engine is empty --
// turns this red.
func TestFiringEngine_WithoutAFlagIsTheRegistryDefault(t *testing.T) {
	def, err := App().Engines().Default()
	require.NoError(t, err)
	got, err := firingEngine(&cobra.Command{})
	require.NoError(t, err)
	assert.Equal(t, def.Root().Name, got.Root().Name)

	var diag bytes.Buffer
	t.Cleanup(clidiag.SetSink(&diag))
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("not json"))
	cmd.SetOut(&out)
	require.Error(t, hookSessionStartCmd.RunE(cmd, nil), "the default's codec refuses a payload that is not its wire")

	out.Reset()
	cmd = &cobra.Command{}
	cmd.SetIn(strings.NewReader(`{"session_id":"s","hook_event_name":"SessionStart","source":"startup"}`))
	cmd.SetOut(&out)
	require.NoError(t, hookSessionStartCmd.RunE(cmd, nil))
	reply, err := def.Hooks().Encode("session_start", engine.HookResponse{})
	require.NoError(t, err)
	assert.Equal(t, string(reply.Stdout), out.String(), "the answer is the default engine's empty envelope")
}

// TestFiringEngine_AnUnknownEngineIsRefused: an EXPLICIT --engine naming no
// registered engine is refused with a typed error, never rounded to the
// default.
//
// MUTATION -- fall back to the default for an unknown name -- turns this red.
func TestFiringEngine_AnUnknownEngineIsRefused(t *testing.T) {
	_, err := firingEngine(firedBy(t, &cobra.Command{}, "no-such-engine"))
	var unknown *UnknownHookEngineError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, "no-such-engine", unknown.Name)

	cmd := firedBy(t, &cobra.Command{}, "no-such-engine")
	cmd.SetIn(strings.NewReader(`{"session_id":"s"}`))
	require.ErrorAs(t, hookSessionStartCmd.RunE(cmd, nil), &unknown)
}

// TestAgentSetupNudge_Wiring proves the SessionStart hook fires the nudge
// exactly when the project rooted at workDir has profiles but no agents, and
// never blocks on a config it can't load.
func TestAgentSetupNudge_Wiring(t *testing.T) {
	// agentSetupNudge's the config read is real-OS-fs (no injected fs): isolate
	// HOME so the home-layer read (D2/D3 layering) never reaches this
	// developer's real ~/.ctxloom — each subtest's writeRoot fixture must be
	// the only source of profiles/agents it's asserting on.
	testsupport.Isolate(t)
	t.Setenv(projectroot.EnvVar, "") // don't let an ambient root override workDir

	// A profile is a project-bundle item, so "the project has profiles" means one
	// exists in the project bundle — writing a config block would only produce a
	// retired-key warning and no profile at all.
	writeRoot := func(t *testing.T, body string, profiles ...string) string {
		root := t.TempDir()
		appDir := filepath.Join(root, ".ctxloom")
		require.NoError(t, os.MkdirAll(appDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0644))
		if len(profiles) > 0 {
			require.NoError(t, os.MkdirAll(bundletree.ProjectProfilesDir(t, appDir), 0755))
			for _, name := range profiles {
				require.NoError(t, os.WriteFile(filepath.Join(bundletree.ProjectProfilesDir(t, appDir), name+".yaml"),
					[]byte("description: seeded by the test\n"), 0644))
			}
		}
		return root
	}

	// The hook process reads its own generation, pinned here to the project
	// the test wrote — the composition a hook spawned in that project opens.
	inProject := func(t *testing.T, root string) {
		t.Helper()
		testApp(t, configload.WithAppDir(filepath.Join(root, ".ctxloom")))
	}

	t.Run("profiles, no agents → nudge", func(t *testing.T) {
		root := writeRoot(t, "schema_version: 8\n", "default")
		inProject(t, root)
		assert.NotEmpty(t, agentSetupNudge())
	})

	t.Run("agent configured → silent", func(t *testing.T) {
		root := writeRoot(t, "schema_version: 8\nagents:\n  dev:\n    profiles: [default]\n", "default")
		inProject(t, root)
		assert.Empty(t, agentSetupNudge())
	})

	t.Run("no .ctxloom → silent, never blocks", func(t *testing.T) {
		inProject(t, t.TempDir())
		assert.Empty(t, agentSetupNudge())
	})
}

// TestHookSessionStart_PanicIsNotSuccess pins that a PANICKING
// session-start hook fails loud instead of reporting success: exit 0 would
// make a crash indistinguishable from "nothing to deliver", so it returns an
// error, writes no answer, and names the panic on stderr. The panic is
// induced through the real production path: a nil command has no flags or
// stdin to read.
func TestHookSessionStart_PanicIsNotSuccess(t *testing.T) {
	var runErr error
	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			runErr = hookSessionStartCmd.RunE(nil, nil)
		})
	})

	require.Error(t, runErr,
		"a panicking hook must return an error so the process exits non-zero")
	assert.Empty(t, stdout, "a crashed hook answers nothing")
	assert.Contains(t, stderr, "panic:",
		"the panic must stay diagnosable on stderr")
}
