package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// plantRealCompanion makes $PATH hold exactly one directory containing a REAL
// executable named bin, which appends its first argument to a sentinel file
// when it runs; it returns the sentinel's path. Nothing except an actual
// execution can create that file, which is what makes it evidence rather than a
// restatement of the code under test.
//
// $PATH is REPLACED, not prepended: companion discovery scans it, so whatever
// the developer has installed would otherwise decide how many rows the report
// has and what they say.
//
// The exec seams are deliberately left at their production bodies. A recorder
// installed into config's exec seam would only witness an exec that still
// travels through the seam, so a report that grew its own way to run the binary
// would leave such a recorder empty and the assertion green.
// Returns the sentinel the script touches when executed, AND the binary's own
// path — admission now reads the binary's bytes to verify them, so a caller has
// to be able to name the file it is vouching for.
func plantRealCompanion(t *testing.T, bin string) (sentinel, binPath string) {
	t.Helper()
	dir := t.TempDir()
	sentinel = filepath.Join(t.TempDir(), "executed")
	script := "#!/bin/sh\necho \"$1\" >> " + sentinel + "\n"
	binPath = filepath.Join(dir, bin)
	require.NoError(t, os.WriteFile(binPath, []byte(script), 0o755))
	t.Setenv("PATH", dir)
	return sentinel, binPath
}

// TestPrintCompanionStatus_ExecutesNothingEvenWhenAdmissible pins the property
// a status command owes its reader: asking what the state of things is must
// never run a foreign binary.
//
// The companion is SIGNED first, on purpose. An unsigned companion is refused
// by the admission gate, so a report that wrongly reached for the resolved
// bundle set would still exec nothing and this test would pass while the
// property was broken. With a valid signature in place, admission says yes and
// the ONLY thing standing between this report and an execution is the report's
// own refusal to ask for content it has no use for.
func TestPrintCompanionStatus_ExecutesNothingEvenWhenAdmissible(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sentinel companion is an sh script")
	}
	const bin = "ctxloom-companion-acme"
	root, _ := setupProject(t, "claude-code")
	testsupport.ChangeDir(t, root)
	sentinel, binPath := plantRealCompanion(t, bin)

	// Signed and trusted in this project's own root: the only thing that
	// admits a companion now.
	testsupport.SignCompanionForTesting(t, binPath,
		filepath.Join(root, ".ctxloom", "allowed_signers"))

	var out bytes.Buffer
	printCompanionStatus(&out)

	// Guard: the companion has to have been FOUND and reported as runnable.
	// Without this the sentinel assertion below would pass just as well against
	// a fixture whose binary was never discovered — absence satisfying absence.
	line := companionLineFor(t, out.String(), bin)
	assert.NotContains(t, line, "NOT RUN",
		"consent was recorded, so admission must say yes — otherwise the gate, not the report, is what withheld the exec")
	assert.NotContains(t, line, "NOT FOUND", "the planted binary must be discovered on PATH")

	assert.NoFileExists(t, sentinel,
		"a status report must execute nothing — not even a companion this machine's human approved")
}

// TestPrintCompanionStatus_ReportsTheRefusedPathNotAnAbsence is the other half
// of the same report: a companion present on PATH and never confirmed is not
// missing, and telling a user to install what they already have sends them
// chasing nothing. The path is what `ctxloom companion show` has to be pointed
// at, so it has to be in the line.
func TestPrintCompanionStatus_ReportsTheRefusedPathNotAnAbsence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sentinel companion is an sh script")
	}
	const bin = "ctxloom-companion-acme"
	root, _ := setupProject(t, "claude-code")
	testsupport.ChangeDir(t, root)
	sentinel, _ := plantRealCompanion(t, bin)

	var out bytes.Buffer
	printCompanionStatus(&out)

	line := companionLineFor(t, out.String(), bin)
	assert.Contains(t, line, "NOT RUN", "found but never approved is not the same fact as not installed")
	assert.NotContains(t, line, "NOT FOUND", "the binary is on PATH; reporting it missing is a false errand")
	assert.Contains(t, line, "ctxloom companion show", "a refusal a user cannot act on is a dead end")
	assert.NoFileExists(t, sentinel, "reporting a refusal must not run the file it refused")
}

// TestPrintCompanionStatus_DisabledSaysSoAndStillRunsNothing covers the
// --no-companions path. "Off" must mean no companion code runs, and the report
// must SAY the switch is on rather than rendering an empty or absent section —
// a section that quietly disappears reads as "you have no companions", which is
// a different fact from "you told me not to look".
func TestPrintCompanionStatus_DisabledSaysSoAndStillRunsNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sentinel companion is an sh script")
	}
	const bin = "ctxloom-companion-acme"
	root, _ := setupProject(t, "claude-code")
	testsupport.ChangeDir(t, root)
	sentinel, binPath := plantRealCompanion(t, bin)
	// Admissible on its own merits, so the DISABLE SWITCH below is the only
	// thing withholding the exec — otherwise this would pass for the wrong
	// reason, with the companion refused for want of a signature.
	testsupport.SignCompanionForTesting(t, binPath,
		filepath.Join(root, ".ctxloom", "allowed_signers"))

	// The switch is a property of the process composition, not a global.
	src, err := operations.ComposeSources(operations.Compose{NoCompanions: true})
	require.NoError(t, err)
	t.Cleanup(SetAppForTesting(operations.NewApp(src, operations.Switches{NoCompanions: true}, nil, strictness.Mode{Prog: "ctxloom"}, operations.Handed{Open: config.Open, Reporter: strictness.Sink("ctxloom"), Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims})))

	var out bytes.Buffer
	printCompanionStatus(&out)

	assert.Contains(t, out.String(), "Companions:")
	assert.Contains(t, out.String(), "disabled", "the report must name the switch rather than going quiet")
	assert.NotContains(t, out.String(), bin, "nothing was looked at, so nothing may be claimed about it")
	assert.NoFileExists(t, sentinel)
}

// companionLineFor returns the one report line naming bin, failing loudly when
// there is none — a missing line must never be read as a passing assertion.
func companionLineFor(t *testing.T, report, bin string) string {
	t.Helper()
	for _, line := range strings.Split(report, "\n") {
		if strings.Contains(line, bin) {
			return line
		}
	}
	t.Fatalf("the report names no companion %q; report was:\n%s", bin, report)
	return ""
}
