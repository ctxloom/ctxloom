package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// TestReportCorpusNamesEveryOffenderWithItsError is the requirement that keeps
// this gate alive: a check that prints "3 bundles failed" gives its reader
// nothing to act on, so the next person to meet it disables it instead of
// fixing the content. Every offending bundle must arrive with its parse error.
func TestReportCorpusNamesEveryOffenderWithItsError(t *testing.T) {
	report := operations.CorpusReport{
		RemotesConfigured: 1,
		RemotesRead:       1,
		Parsed:            5,
		Violations: []operations.CorpusViolation{
			{
				Bundle: operations.CorpusBundle{Remote: "ctxloom-default", URL: "https://github.com/ctxloom/ctxloom-default", Path: ".ctxloom/content/bundles/go.yaml"},
				Err:    errors.New("invalid bundle YAML: unknown key `hoooks` on line 12"),
			},
			{
				Bundle: operations.CorpusBundle{Remote: "personal", URL: "https://github.com/me/ctxloom-personal", Path: ".ctxloom/content/bundles/rust.yaml"},
				Err:    errors.New("invalid bundle YAML: unknown key `promts` on line 3"),
			},
		},
	}

	var out bytes.Buffer
	code := reportCorpus(&out, report)

	assert.Equal(t, 1, code)
	text := out.String()
	for _, want := range []string{
		".ctxloom/content/bundles/go.yaml",
		"unknown key `hoooks` on line 12",
		".ctxloom/content/bundles/rust.yaml",
		"unknown key `promts` on line 3",
	} {
		assert.Contains(t, text, want, "every offender and its reason must reach the reader")
	}
}

// TestReportCorpusUndeterminedIsNotSuccess pins the distinction the whole gate
// turns on: a corpus that could not be read exits non-zero and says so, rather
// than printing the OK line for work it never did.
func TestReportCorpusUndeterminedIsNotSuccess(t *testing.T) {
	report := operations.CorpusReport{
		RemotesConfigured: 1,
		Gaps:              []operations.CorpusGap{{Remote: "ctxloom-default", URL: "https://github.com/ctxloom/ctxloom-default", Err: errors.New("no local clone")}},
	}

	var out bytes.Buffer
	code := reportCorpus(&out, report)

	assert.Equal(t, 2, code, "could-not-check must not share an exit code with either clean or violated")
	text := out.String()
	assert.Contains(t, text, "UNDETERMINED")
	assert.Contains(t, text, "ctxloom-default")
	assert.Contains(t, text, "no local clone")
	assert.NotContains(t, text, "OK:", "an unverified corpus must never print the success line")
}

// TestReportCorpusEmptyCorpusSaysNothingWasChecked separates the two ways a
// check can come back undetermined, because they call for different fixes: a
// gap means repair the clone, an empty corpus means the gate is pointed at
// nothing.
func TestReportCorpusEmptyCorpusSaysNothingWasChecked(t *testing.T) {
	var out bytes.Buffer
	code := reportCorpus(&out, operations.CorpusReport{})

	assert.Equal(t, 2, code)
	assert.Contains(t, out.String(), "nothing was checked")
	assert.NotContains(t, out.String(), "OK:")
}

// TestReportCorpusCleanExitsZero is the counterweight: without it every
// assertion above is satisfied by a reporter that fails unconditionally.
func TestReportCorpusCleanExitsZero(t *testing.T) {
	var out bytes.Buffer
	code := reportCorpus(&out, operations.CorpusReport{RemotesConfigured: 2, RemotesRead: 2, Parsed: 70})

	assert.Equal(t, 0, code)
	text := out.String()
	assert.Contains(t, text, "OK:")
	assert.Contains(t, text, "parsed 70 bundle(s)", "the clean verdict must show the work it rests on")
}
