package cli

import (
	"bytes"
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The format guard used to fire in PersistentPostRunE — AFTER RunE had already
// run. For a read-only command that is merely untidy. For a MUTATING one it is
// a defect with teeth: the work is done, the exit code says failure, and a
// caller that retries on non-zero repeats a mutation that already succeeded.
//
// Reproduced against a real binary before this was written:
//
//	$ ctxloom profile create bug --parent <p> --format json
//	Created profile "bug" with parents: ...
//	Saved to: .../profiles/bug.yaml
//	{ "error": "... --format json was accepted but this command does not support it yet ..." }
//	EXIT=1
//	$ ls .ctxloom/profiles/   ->   bug.yaml   <-- created anyway
//
// The fix refuses in PersistentPreRunE instead, so RunE never runs.

// TestFormatGuard_RefusesBeforeTheCommandDoesAnything is the test that
// distinguishes the fix from the bug it replaces.
//
// Asserting "returns an error" is NOT enough — the old, broken behaviour did
// that too, and a test written that way passes against the defect. The
// property that actually changed is that the command PRODUCES NOTHING, because
// its RunE never executed. `agent default` is the witness: on an empty project
// it succeeds and reports that no default agent is set, so any output at all
// proves RunE ran.
func TestFormatGuard_RefusesBeforeTheCommandDoesAnything(t *testing.T) {
	testsupport.ProjectDir(t)
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"agent", "default", "--format", "json"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		// rootCmd's --format is a process-wide PersistentFlags() value that
		// pflag does not reset between Execute() calls on the same tree, and
		// PersistentPreRun flips the structured-diagnostics channel to match.
		// Leaving either set reroutes later tests' warnings through the JSON
		// renderer — a test-order hazard, not a product bug.
		_ = rootCmd.PersistentFlags().Set("format", formatText)
		clidiag.SetStructured(false)
	})

	err := rootCmd.Execute()

	require.Error(t, err, "an unsupported --format must not exit 0 pretending to have honored it")
	assert.Contains(t, err.Error(), errFormatUnsupportedFragment,
		"the refusal must still name the remedy the old post-run guard named")

	assert.Empty(t, out.String(),
		"the command must produce NOTHING: refusing in PersistentPreRunE means RunE never ran. "+
			"Output here means the work happened first and the error came after — the defect this replaces.")
}

// TestFormatGuard_TextAndImplicitFormatsStillRun guards the other direction.
// A refusal that fires too eagerly would break every ordinary invocation, and
// that failure is far worse than the one being fixed: text is the default, and
// off a terminal cliemit.Resolve defaults to JSON without the caller asking for
// it. Neither may be refused.
func TestFormatGuard_TextAndImplicitFormatsStillRun(t *testing.T) {
	testsupport.ProjectDir(t)
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"agent", "default"}) // no --format at all
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		_ = rootCmd.PersistentFlags().Set("format", formatText)
		clidiag.SetStructured(false)
	})

	err := rootCmd.Execute()
	assert.NoError(t, err, "a command the caller never asked to format must run normally")
}

// TestFormatDebtAllowlist_IsReachableFromProduction pins the move that makes
// early refusal possible at all. The ledger used to live in
// format_coverage_test.go, where production code cannot see it — and a
// PersistentPreRunE check must know BEFORE RunE which commands carry debt,
// which is exactly what the ledger knows.
//
// Without this, the allowlist could drift back into test-only scope and the
// early refusal would silently degrade to refusing nothing.
func TestFormatDebtAllowlist_IsReachableFromProduction(t *testing.T) {
	require.NotEmpty(t, formatDebtAllowlist, "the ledger must be production-visible")
	// A mutating command that is still tracked as debt: this is the shape the
	// defect actually bit on.
	_, ok := formatDebtAllowlist["profile create"]
	assert.True(t, ok,
		"`profile create` mutates and still carries format debt; it is the case this fix exists for")
}
