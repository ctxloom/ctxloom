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
func plantRealCompanion(t *testing.T, bin string) (sentinel string) {
	t.Helper()
	dir := t.TempDir()
	sentinel = filepath.Join(t.TempDir(), "executed")
	script := "#!/bin/sh\necho \"$1\" >> " + sentinel + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, bin), []byte(script), 0o755))
	t.Setenv("PATH", dir)
	return sentinel
}

// TestPrintCompanionStatus_ExecutesNothingForARegisteredCompanion pins the
// property a status command owes its reader: asking what the state of things
// is must never run a foreign binary — not even a registered one.
func TestPrintCompanionStatus_ExecutesNothingForARegisteredCompanion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sentinel companion is an sh script")
	}
	const bin = "ctxloom-companion-acme"
	sentinel := plantRealCompanion(t, bin)

	var out bytes.Buffer
	printCompanionStatus(&out, []string{"acme"})

	// Guard: the companion has to have been FOUND, or the sentinel assertion
	// below passes against a binary that was never resolved.
	line := companionLineFor(t, out.String(), "acme")
	assert.NotContains(t, line, "NOT FOUND", "the planted binary must resolve on PATH")
	assert.Contains(t, line, bin)
	assert.NoFileExists(t, sentinel, "a status report must execute nothing")
}

// TestPrintCompanionStatus_RegisteredButMissingNamesTheWayOut: a registered
// companion that resolves to nothing is reported with both remedies.
func TestPrintCompanionStatus_RegisteredButMissingNamesTheWayOut(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out bytes.Buffer
	printCompanionStatus(&out, []string{"acme"})
	line := companionLineFor(t, out.String(), "acme")
	assert.Contains(t, line, "NOT FOUND (ctxloom-companion-acme)")
	assert.Contains(t, line, "ctxloom companion remove acme --yes")
}

// TestPrintCompanionStatus_UnregisteredOnPathIsNotReported: a companion
// binary on PATH that nobody registered is not a companion of this machine.
func TestPrintCompanionStatus_UnregisteredOnPathIsNotReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sentinel companion is an sh script")
	}
	sentinel := plantRealCompanion(t, "ctxloom-companion-acme")
	var out bytes.Buffer
	printCompanionStatus(&out, nil)
	assert.Contains(t, out.String(), "none registered")
	assert.NotContains(t, out.String(), "acme")
	assert.NoFileExists(t, sentinel)
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

	// The switch is a property of the process composition, not a global.
	src, err := operations.ComposeSources(operations.Compose{NoCompanions: true})
	require.NoError(t, err)
	t.Cleanup(SetAppForTesting(operations.NewApp(src, operations.Switches{NoCompanions: true}, nil, strictness.Mode{Prog: "ctxloom"}, operations.Handed{Open: config.Open, Reporter: strictness.Sink("ctxloom"), Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims})))

	var out bytes.Buffer
	printCompanionStatus(&out, []string{"acme"})

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
