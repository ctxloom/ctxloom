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
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// The env switch is PER INVOCATION: read once, then removed from this
// process's environment, so nothing ctxloom starts — an engine, its MCP
// server, a hook, a delegated agent — inherits a waiver it never asked for.
func TestConsumeSigCheckEnv_ReadsTheSwitchThenRemovesItFromEveryChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the child probe is a POSIX shell")
	}
	t.Setenv(bundles.SigCheckEnv, "1")

	assert.True(t, consumeSigCheckEnv(), "the switch was on")

	_, still := os.LookupEnv(bundles.SigCheckEnv)
	assert.False(t, still, "this process no longer carries it")
	out, _ := exec.Command("sh", "-c", "printenv "+bundles.SigCheckEnv+" || true").Output()
	assert.Empty(t, strings.TrimSpace(string(out)), "a child started the ordinary way does not inherit it")
}

func TestConsumeSigCheckEnv_OffWhenUnset(t *testing.T) {
	t.Setenv(bundles.SigCheckEnv, "")
	assert.False(t, consumeSigCheckEnv())
}

// withSigCheckEnv fixes what the process environment said for one test.
func withSigCheckEnv(t *testing.T, on bool) {
	t.Helper()
	prev := sigCheckEnv
	sigCheckEnv = func() bool { return on }
	t.Cleanup(func() { sigCheckEnv = prev })
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
	printSignatureCheck(&buf, signatureCheckEnforced)
	assert.Empty(t, buf.String())
}
