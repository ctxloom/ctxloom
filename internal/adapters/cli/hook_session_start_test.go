package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/textblocks"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// TestShouldInjectResumedEssence covers the source gate: the resumed essence
// rides an initial launch but never /clear or /compact.
func TestShouldInjectResumedEssence(t *testing.T) {
	for _, src := range []string{"startup", "resume", "", "unexpected"} {
		assert.True(t, shouldInjectResumedEssence(src), "source %q should inject essence", src)
	}
	for _, src := range []string{"clear", "compact"} {
		assert.False(t, shouldInjectResumedEssence(src), "source %q must not inject essence", src)
	}
}

// TestResumePartsIncludeSession covers the parts gate: essence is for resumes
// that carried the session, not tasks-only resumes.
func TestResumePartsIncludeSession(t *testing.T) {
	assert.True(t, resumePartsIncludeSession(""), "empty parts default to session+tasks")
	assert.True(t, resumePartsIncludeSession("session,tasks"))
	assert.True(t, resumePartsIncludeSession("session"))
	assert.True(t, resumePartsIncludeSession("tasks, session "))
	assert.False(t, resumePartsIncludeSession("tasks"))
}

// TestBuildSessionStartOutput covers the envelope around the resumed
// essence: none without an essence, the framed essence under the SessionStart
// event, and an essence too long for claude's additionalContext cap cut to
// fit with a pointer to the whole of it.
func TestSessionStartContext(t *testing.T) {
	const limit = 7500
	assert.Empty(t, sessionStartContext("", "/out/essence.md", limit), "no essence, nothing delivered")

	body := sessionStartContext("what we did last time", "/out/essence.md", limit)
	assert.Contains(t, body, "<ctxloom-resumed-session>")
	assert.Contains(t, body, "what we did last time")
	assert.NotContains(t, body, "/out/essence.md", "an essence that fits is delivered whole, with no pointer")

	long := strings.Repeat("é essence line\n", limit)
	cut := sessionStartContext(long, "/out/essence.md", limit)
	assert.LessOrEqual(t, len(cut), limit, "the delivered context stays under the engine's limit")
	assert.True(t, utf8.ValidString(cut), "the cut never splits a character")
	assert.Contains(t, cut, "/out/essence.md", "the cut names where the whole essence is")
	assert.Contains(t, cut, "/recover", "the cut names /recover")
	assert.True(t, strings.HasSuffix(cut, "</ctxloom-resumed-session>\n"), "the frame stays closed")

	noPath := sessionStartContext(long, "", limit)
	assert.Contains(t, noPath, "/recover", "a cut with no essence path still names /recover")

	assert.Contains(t, sessionStartContext(long, "/out/essence.md", 0), long, "an engine that declares no limit gets the essence whole")
}

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

// presence conditions.
func TestResumedEssenceForInjection(t *testing.T) {
	testsupport.Isolate(t)
	harp := "swift-amber-falcon"
	essence, err := harpEssencePath(t, harp)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(essence, []byte("  compacted summary  \n"), 0o644))

	assert.Equal(t, "compacted summary",
		resumedEssenceForInjection("startup", harp, "session,tasks"), "trimmed essence on startup")
	assert.Empty(t, resumedEssenceForInjection("clear", harp, "session,tasks"), "no essence on /clear")
	assert.Empty(t, resumedEssenceForInjection("startup", "", "session,tasks"), "no essence without a resume")
	assert.Empty(t, resumedEssenceForInjection("startup", harp, "tasks"), "no essence for tasks-only resume")
	assert.Empty(t, resumedEssenceForInjection("startup", "no-such-harp", "session,tasks"), "no essence when file missing")
}

// TestClearRecoveryMessage covers the user-facing /recover nudge gate: it fires
// only on a /clear, and only when the current session's pre-clear transcript
// is recoverable.
func TestClearRecoveryMessage(t *testing.T) {
	assert.Contains(t, clearRecoveryMessage("clear", true), "/recover", "the nudge must name the /recover command")
	assert.Empty(t, clearRecoveryMessage("clear", false), "no nudge when there's nothing to recover")
	for _, src := range []string{"startup", "resume", "compact", ""} {
		assert.Empty(t, clearRecoveryMessage(src, true),
			"source %q retains or re-delivers context — nothing to recover", src)
	}
}

// makes would get no nudge at all.
func TestCurrentSessionRecoverable(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open(nil)
	require.NoError(t, err)

	// Shape 1: a PRIOR clear already recorded a rotation (a second-or-later
	// clear in this session). Recoverable regardless of what the incoming
	// payload's session id is.
	rotated, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	_, err = mgr.RecordOutputDir(rotated.HarpName, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(rotated.HarpName, "pre-clear-id", "/pre-clear.jsonl"))
	require.NoError(t, mgr.BindSession(rotated.HarpName, "post-clear-id", "/post-clear.jsonl"))

	// Shape 2: bound once, no rotation recorded yet (the FIRST clear in this
	// session — session-bind for THIS clear hasn't run). The incoming payload
	// carries a NEW id that differs from the entry's current binding: that
	// disagreement IS the not-yet-recorded displacement.
	displaced, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	_, err = mgr.RecordOutputDir(displaced.HarpName, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(displaced.HarpName, "pre-clear-id", "/pre-clear.jsonl"))

	// Shape 3: bound to the SAME id the incoming payload carries — an
	// idempotent rebind (e.g. a duplicate hook fire), not a displacement.
	// Nothing was thrown away.
	sameID, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	_, err = mgr.RecordOutputDir(sameID.HarpName, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(sameID.HarpName, "only-id", "/t.jsonl"))

	assert.True(t, currentSessionRecoverable("clear", rotated.HarpName, "post-clear-id"),
		"clear-source + rotation history present -> recoverable, regardless of the incoming payload id")
	assert.True(t, currentSessionRecoverable("clear", displaced.HarpName, "new-post-clear-id"),
		"clear-source + bound entry whose current id differs from the incoming payload id -> the displacement about to be recorded is itself the recoverable signal")
	assert.False(t, currentSessionRecoverable("clear", sameID.HarpName, "only-id"),
		"clear-source + entry already bound to the SAME id the payload carries -> nothing displaced, nothing to recover")

	for _, src := range []string{"startup", "resume", "compact", ""} {
		assert.False(t, currentSessionRecoverable(src, rotated.HarpName, "post-clear-id"),
			"source %q is not a /clear, even with rotation history present -> not recoverable", src)
		assert.False(t, currentSessionRecoverable(src, displaced.HarpName, "new-post-clear-id"),
			"source %q is not a /clear, even with a displaced binding present -> not recoverable", src)
	}

	assert.False(t, currentSessionRecoverable("clear", "", "any-id"),
		"an empty harp name is never recoverable")
	assert.False(t, currentSessionRecoverable("clear", "no-such-harp-in-the-index", "any-id"),
		"a harp the index has never heard of is never recoverable")
}

// TestSessionStartSystemMessageComposition pins the join behavior the
// session-start RunE relies on for output.SystemMessage (the two
// SessionStart nudges — clear-recovery + agent-setup — coexisting):
// non-empty parts join with a blank line, empties drop (textblocks.Join).
func TestSessionStartSystemMessageComposition(t *testing.T) {
	assert.Equal(t, "a\n\nb", textblocks.Join("a", "b"))
	assert.Equal(t, "b", textblocks.Join("", "b"))
	assert.Equal(t, "a", textblocks.Join("a", ""))
	assert.Empty(t, textblocks.Join("", ""))
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
		root := writeRoot(t, "schema_version: 7\n", "default")
		inProject(t, root)
		assert.NotEmpty(t, agentSetupNudge())
	})

	t.Run("agent configured → silent", func(t *testing.T) {
		root := writeRoot(t, "schema_version: 7\nagents:\n  dev:\n    profiles: [default]\n", "default")
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
