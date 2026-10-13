package cli

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/ctxloom/ctxloom/pkg/clifmt/cobrafmt"
)

// THE EXIT-CODE LADDER (docs/cli-ux-principles.md §7). Categorical, not a
// severity scale.

// The ladder is CATEGORICAL, not a severity scale, and its whole value is that
// each code answers a different question. This pins the values and their
// distinctness: collapsing exitCodeRefused onto 1 would report a decision as a
// fault, and onto 0 would make an unattended sync that refused
// indistinguishable from one with nothing to do.
func TestExitCodeLadder_RefusedIsTwoAndDistinctFromErrorAndFindings(t *testing.T) {
	assert.Equal(t, 2, exitCodeRefused, "the documented contract (docs/cli-ux-principles.md §7) is 2")
	assert.Equal(t, 3, exitCodeFatalFindings)
	assert.NotEqual(t, exitCodeRefused, exitCodeFatalFindings)
	assert.NotEqual(t, 0, exitCodeRefused, "a run that deliberately did none of what was asked is not a success")
	assert.NotEqual(t, 1, exitCodeRefused, "a refusal is not an error; 1 would send a user hunting a fault on their own machine")
}

// ctxloom's outcomes end through the family's tail (cobrafmt.Report), over
// the root Run dispatches:
//   - a refusal is REPORTED — the message and its fix are the point — and
//     exits exitCodeRefused;
//   - refusedExit and a wrapped engine's own status are SILENT (the command
//     already said everything) and exit with their code;
//   - any other error is the family's human line and exits exitCodeError.
func TestOutcomes_EndThroughTheFamilyTail(t *testing.T) {
	root := rootCommand()
	require.NoError(t, root.ParseFlags(nil))
	cases := []struct {
		name       string
		err        error
		wantCode   int
		wantStderr string
	}{
		{"a refusal is reported with its fix", fmt.Errorf("run: %w", refusal{remedyErr{"no token", "export it"}}), exitCodeRefused, "ctxloom: run: no token\n  fix: export it\n"},
		{"refusedExit is silent", refusedExit(), exitCodeRefused, ""},
		{"an engine's own status passes through silently", fmt.Errorf("launch: %w", clifmt.ExitStatus{Code: 7}), 7, ""},
		{"an ordinary error is the family line", errors.New("boom"), exitCodeError, "ctxloom: boom\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			assert.Equal(t, tc.wantCode, cobrafmt.Report(root, "ctxloom", &stderr, tc.err))
			assert.Equal(t, tc.wantStderr, stderr.String())
		})
	}
}

type remedyErr struct{ msg, fix string }

func (e remedyErr) Error() string  { return e.msg }
func (e remedyErr) Remedy() string { return e.fix }
