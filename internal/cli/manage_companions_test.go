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

	"github.com/ctxloom/ctxloom/internal/config"
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
func plantRealCompanion(t *testing.T, bin string) string {
	t.Helper()
	dir := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "executed")
	script := "#!/bin/sh\necho \"$1\" >> " + sentinel + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, bin), []byte(script), 0o755))
	t.Setenv("PATH", dir)
	return sentinel
}

// TestPrintCompanionStatus_ExecutesNothingEvenWhenApproved pins the property a
// status command owes its reader: asking what the state of things is must never
// run a foreign binary, and must never raise the trust-on-first-use question
// that would change that state.
//
// Consent is RECORDED first, on purpose. An unapproved companion is refused by
// the admission gate, so a report that wrongly reached for the resolved bundle
// set would still exec nothing and this test would pass while the property was
// broken. With the approval in place, admission says yes and the ONLY thing
// standing between this report and an execution is the report's own refusal to
// ask for content it has no use for.
func TestPrintCompanionStatus_ExecutesNothingEvenWhenApproved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sentinel companion is an sh script")
	}
	const bin = "ctxloom-companion-acme"
	root, _ := setupProject(t, "claude-code")
	testsupport.ChangeDir(t, root)
	sentinel := plantRealCompanion(t, bin)

	rec, err := config.SetCompanionConsent(bin, true)
	require.NoError(t, err, "the fixture must be able to approve the companion")
	require.True(t, rec.Approved)

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
// chasing nothing. The path is what `ctxloom companion trust` has to be pointed
// at, so it has to be in the line.
func TestPrintCompanionStatus_ReportsTheRefusedPathNotAnAbsence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sentinel companion is an sh script")
	}
	const bin = "ctxloom-companion-acme"
	root, _ := setupProject(t, "claude-code")
	testsupport.ChangeDir(t, root)
	sentinel := plantRealCompanion(t, bin)

	var out bytes.Buffer
	printCompanionStatus(&out)

	line := companionLineFor(t, out.String(), bin)
	assert.Contains(t, line, "NOT RUN", "found but never approved is not the same fact as not installed")
	assert.NotContains(t, line, "NOT FOUND", "the binary is on PATH; reporting it missing is a false errand")
	assert.Contains(t, line, "ctxloom companion trust", "a refusal a user cannot act on is a dead end")
	assert.NoFileExists(t, sentinel, "reporting a refusal must not run the file it refused")
}

// TestPrintCompanionStatus_NeverAsksTheConsentQuestionWithAHumanPresent is the
// prompt half, and the forced interactivity is the whole reason it is evidence.
//
// Under `go test` neither end of the terminal check is a terminal, so the
// admission cascade would decline to ask no matter what this report requested —
// "no question appeared" would be true for a reason that has nothing to do with
// the code under test. With a human declared present and a companion sitting in
// the one arm that WOULD ask (present on PATH, no recorded decision), the only
// thing left keeping the question off the screen is the report asking the gate
// not to raise it.
func TestPrintCompanionStatus_NeverAsksTheConsentQuestionWithAHumanPresent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sentinel companion is an sh script")
	}
	const bin = "ctxloom-companion-acme"
	root, _ := setupProject(t, "claude-code")
	testsupport.ChangeDir(t, root)
	sentinel := plantRealCompanion(t, bin)

	var asked bytes.Buffer
	// An empty answer stream, so a question that IS raised comes straight back
	// with no answer instead of blocking on a terminal nobody is typing at.
	defer config.SetCompanionPromptIOForTesting(strings.NewReader(""), &asked)()
	defer config.SetCompanionSessionInteractiveForTesting(true)()

	var out bytes.Buffer
	printCompanionStatus(&out)

	// Guard: the companion must land in the arm that would put the question,
	// or there was never a question to suppress.
	require.Contains(t, companionLineFor(t, out.String(), bin), "NOT RUN",
		"the fixture must reach the unconfirmed arm — the only arm that asks")

	assert.Empty(t, asked.String(),
		"a status report must never put the trust-on-first-use question to a reader who asked what the state of things is")
	assert.NoFileExists(t, sentinel, "and it must not run the binary either")
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
	sentinel := plantRealCompanion(t, bin)
	_, err := config.SetCompanionConsent(bin, true)
	require.NoError(t, err, "approve it, so the switch is the only thing withholding the exec")

	config.SetCompanionsDisabled(true)
	t.Cleanup(func() { config.SetCompanionsDisabled(false) })

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
