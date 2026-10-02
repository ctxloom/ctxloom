package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// The env switch is PER INVOCATION: read once, then removed from this
// process's environment, so no process ctxloom starts inherits it by the
// ordinary route. The session's own hooks and MCP server get the waiver from
// the launch instead (bundles.EngineSigCheckEnv), never from this variable.
func TestConsumeSigCheckEnv_ReadsTheSwitchThenRemovesItFromEveryChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the child probe is a POSIX shell")
	}
	t.Setenv(bundles.SigCheckEnv, "1")

	assert.True(t, consumeEnvSwitch(bundles.SigCheckEnv), "the switch was on")

	_, still := os.LookupEnv(bundles.SigCheckEnv)
	assert.False(t, still, "this process no longer carries it")
	out, _ := exec.Command("sh", "-c", "printenv "+bundles.SigCheckEnv+" || true").Output()
	assert.Empty(t, strings.TrimSpace(string(out)), "a child started the ordinary way does not inherit it")
}

func TestConsumeSigCheckEnv_OffWhenUnset(t *testing.T) {
	t.Setenv(bundles.SigCheckEnv, "")
	assert.False(t, consumeEnvSwitch(bundles.SigCheckEnv))
}

// withSigCheckEnv fixes what the process environment said for one test.
func withSigCheckEnv(t *testing.T, on bool) {
	t.Helper()
	prev := sigCheckEnv
	sigCheckEnv = func() bool { return on }
	t.Cleanup(func() { sigCheckEnv = prev })
}

// withSessionSigCheckEnv fixes what the session carrier said for one test.
func withSessionSigCheckEnv(t *testing.T, on bool) {
	t.Helper()
	prev := sessionSigCheckEnv
	sessionSigCheckEnv = func() bool { return on }
	t.Cleanup(func() { sessionSigCheckEnv = prev })
}

// Owner ruling 2026-10-02: a waived session's hooks share its waiver. The
// launch puts the session carrier on the engine's environment; only the
// commands that serve a session honour it.
func TestSigCheckDisabled_AHookHonoursTheSessionCarrier(t *testing.T) {
	withSigCheckEnv(t, false)
	withSessionSigCheckEnv(t, true)
	hook, _, err := rootCmd.Find([]string{"hook", "inject-context"})
	require.NoError(t, err)

	assert.True(t, sigCheckDisabled(hook), "a hook of a waived session decides waived")
	assert.True(t, sigCheckDisabled(hookCmd))
}

// The carrier reaches whatever the engine starts — its shell too. A `ctxloom
// run` typed there is a NEW invocation (an agent launched by hand), and it
// never inherits the session's waiver.
func TestSigCheckDisabled_OnlyTheCommandsServingASessionHonourTheCarrier(t *testing.T) {
	withSigCheckEnv(t, false)
	withSessionSigCheckEnv(t, true)

	assert.False(t, sigCheckDisabled(runCmd), "a run started from the session's shell is enforced")
	assert.False(t, sigCheckDisabled(rootCmd))
	assert.False(t, sigCheckDisabled(nil))
}

func TestSigCheckDisabled_AHookOfAnEnforcedSessionIsEnforced(t *testing.T) {
	withSigCheckEnv(t, false)
	withSessionSigCheckEnv(t, false)
	assert.False(t, sigCheckDisabled(hookCmd))
}

// The carrier is consumed like the invocation switch, by every process, so
// nothing a hook (or anything else) starts inherits it in turn.
func TestConsumeEnvSwitch_RemovesTheSessionCarrier(t *testing.T) {
	t.Setenv(sessions.EnvSigCheckWaived, sessions.SigCheckWaivedOn)
	assert.True(t, consumeEnvSwitch(sessions.EnvSigCheckWaived))
	_, still := os.LookupEnv(sessions.EnvSigCheckWaived)
	assert.False(t, still)
}

// setSigCheckFlag sets --disable-sig-check as a parsed command line would,
// and undoes it: a Changed flag leaking into the next test would decide it.
func setSigCheckFlag(t *testing.T, value string) {
	t.Helper()
	f := rootCmd.PersistentFlags().Lookup(bundles.SigCheckFlag)
	require.NotNil(t, f, "--%s is a root persistent flag", bundles.SigCheckFlag)
	require.NoError(t, rootCmd.PersistentFlags().Set(bundles.SigCheckFlag, value))
	t.Cleanup(func() {
		f.Changed = false
		sigCheckFlag = false
	})
}

func TestSigCheckDisabled_TheEnvSwitchAloneDisables(t *testing.T) {
	withSigCheckEnv(t, true)
	assert.True(t, sigCheckDisabled(rootCmd))
}

func TestSigCheckDisabled_OffByDefault(t *testing.T) {
	withSigCheckEnv(t, false)
	assert.False(t, sigCheckDisabled(rootCmd))
	assert.False(t, sigCheckDisabled(nil))
}

func TestSigCheckDisabled_TheFlagAloneDisables(t *testing.T) {
	withSigCheckEnv(t, false)
	setSigCheckFlag(t, "true")
	assert.True(t, sigCheckDisabled(rootCmd))
}

func TestSigCheckDisabled_AnExplicitFlagWinsOverTheEnvInEitherDirection(t *testing.T) {
	withSigCheckEnv(t, true)
	setSigCheckFlag(t, "false")
	assert.False(t, sigCheckDisabled(rootCmd), "--disable-sig-check=false beats the env switch")
}

// installAppForTest composes theApp with sw and restores the previous one.
func installAppForTest(t *testing.T, sw operations.Switches) {
	t.Helper()
	prev := theApp
	t.Cleanup(func() { theApp = prev })
	installApp(nil, nil, sw, strictness.Mode{Prog: "ctxloom"})
}

// The notice is said ONCE per process, however many times the process
// composes (init re-pins its directory), and its text is the constant every
// other surface (doctor) shows.
func TestInstallApp_AnnouncesTheWaiverOncePerProcess(t *testing.T) {
	var buf bytes.Buffer
	t.Cleanup(clidiag.SetSink(&buf))
	clidiag.ResetWarnOnce()
	t.Cleanup(clidiag.ResetWarnOnce)

	installAppForTest(t, operations.Switches{SigCheckDisabled: true})
	installAppForTest(t, operations.Switches{SigCheckDisabled: true})

	assert.True(t, theApp.SigCheckDisabled, "the switch reaches the App")
	assert.Equal(t, 1, strings.Count(buf.String(), bundles.SigCheckDisabledNotice), "said once, in exactly these words")
}

func TestInstallApp_SaysNothingWhenTheCheckIsEnforced(t *testing.T) {
	var buf bytes.Buffer
	t.Cleanup(clidiag.SetSink(&buf))
	clidiag.ResetWarnOnce()
	t.Cleanup(clidiag.ResetWarnOnce)

	installAppForTest(t, operations.Switches{})

	assert.False(t, theApp.SigCheckDisabled)
	assert.NotContains(t, buf.String(), bundles.SigCheckDisabledNotice)
}

// resetSigCheckFlagAfter undoes a --disable-sig-check that runCLI parsed:
// runCLI restores run's own flags, not the root's persistent ones.
func resetSigCheckFlagAfter(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		rootCmd.PersistentFlags().Lookup(bundles.SigCheckFlag).Changed = false
		sigCheckFlag = false
	})
}

func TestDryRun_ReportsTheSignatureCheckPosture(t *testing.T) {
	runCLIFixture(t)
	withSigCheckEnv(t, false)
	resetSigCheckFlagAfter(t)

	res := runCLI(t, "run", "--dry-run", "--format", "json", "-p", "dev", "hi")
	require.NoError(t, res.err, res.all())
	var got dryRunJSON
	require.NoError(t, json.Unmarshal([]byte(res.out), &got), "payload: %s", res.out)
	assert.Equal(t, signatureCheckEnforced, got.SignatureCheck)

	res = runCLI(t, "--"+bundles.SigCheckFlag, "run", "--dry-run", "--format", "json", "-p", "dev", "hi")
	require.NoError(t, res.err, res.all())
	require.NoError(t, json.Unmarshal([]byte(res.out), &got), "payload: %s", res.out)
	assert.Equal(t, signatureCheckDisabled, got.SignatureCheck, "the flag reaches the generation the preview decides with")

	res = runCLI(t, "--"+bundles.SigCheckFlag, "run", "--dry-run", "--format", "text", "-p", "dev", "hi")
	require.NoError(t, res.err, res.all())
	assert.Contains(t, res.stdout, "=== Signature Check ===\n"+signatureCheckDisabled+": "+bundles.SigCheckDisabledNotice+"\n")
}

func TestDryRun_TextSaysNothingAboutAnEnforcedCheck(t *testing.T) {
	var buf bytes.Buffer
	printSignatureCheck(&buf, signatureCheckEnforced, []string{"acme/a"})
	assert.Empty(t, buf.String())
}

// The owner accepted that the waiver hides tampering of an installed signed
// tree only on condition that the dry run names every tree it hid.
func TestDryRun_TextNamesEditedSignedTreesTheWaiverAccepted(t *testing.T) {
	var buf bytes.Buffer
	printSignatureCheck(&buf, signatureCheckDisabled, []string{"acme/a", "acme/b"})
	assert.Contains(t, buf.String(), signatureCheckDisabled+": "+bundles.SigCheckDisabledNotice+"\n")
	assert.Contains(t, buf.String(), editedSignedTreesLabel+": acme/a, acme/b\n")

	buf.Reset()
	printSignatureCheck(&buf, signatureCheckDisabled, nil)
	assert.NotContains(t, buf.String(), editedSignedTreesLabel, "no edited tree, no line")
}

// A command run inside a waived session that does not serve it (a doctor, a
// run typed in the engine's shell) decides enforced, and still knows the
// session it runs in is waived — so it can say so.
func TestSwitches_CarryTheSessionsWaiverWithoutWaivingTheInvocation(t *testing.T) {
	withSigCheckEnv(t, false)
	withSessionSigCheckEnv(t, true)

	sw := switches(runCmd)

	assert.False(t, sw.SigCheckDisabled, "the invocation verifies")
	assert.True(t, sw.SessionSigCheckWaived, "and knows its session does not")
}

func TestDryRun_NamesTheWaiverOfTheSessionItRunsIn(t *testing.T) {
	runCLIFixture(t)
	withSigCheckEnv(t, false)
	withSessionSigCheckEnv(t, true)
	resetSigCheckFlagAfter(t)

	res := runCLI(t, "run", "--dry-run", "--format", "json", "-p", "dev", "hi")
	require.NoError(t, res.err, res.all())
	var got dryRunJSON
	require.NoError(t, json.Unmarshal([]byte(res.out), &got), "payload: %s", res.out)
	assert.Equal(t, signatureCheckEnforced, got.SignatureCheck, "a run typed in the session's shell verifies")
	assert.Equal(t, signatureCheckDisabled, got.SessionSignatureCheck, "and names the waiver of the session it runs in")

	res = runCLI(t, "run", "--dry-run", "--format", "text", "-p", "dev", "hi")
	require.NoError(t, res.err, res.all())
	assert.Contains(t, res.stdout, "=== Signature Check ===\nsession: "+bundles.SessionSigCheckNotice+"\n")
}
