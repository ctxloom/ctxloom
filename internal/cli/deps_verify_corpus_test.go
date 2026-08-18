package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// decodeCorpusJSON marshals the emitted payload exactly as `--format json`
// does and reads it back as a generic map, so these tests assert against the
// WIRE FORM a CI consumer actually parses rather than against the Go struct.
// A field that fails to marshal (an `error`, an unexported field) is invisible
// to the struct and caught here.
func decodeCorpusJSON(t *testing.T, report operations.CorpusReport) map[string]any {
	t.Helper()
	raw, err := json.Marshal(corpusResultOf(report))
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return decoded
}

// TestCorpusResultJSONNamesEveryOffenderWithItsError is the structured half of
// the "name every offender" requirement. Routing through emit() is only worth
// anything if the findings SURVIVE the encoding — CorpusViolation carries an
// `error`, which marshals to `{}` and would silently drop the one part of the
// finding a reader can act on.
func TestCorpusResultJSONNamesEveryOffenderWithItsError(t *testing.T) {
	report := operations.CorpusReport{
		RemotesConfigured: 1, RemotesRead: 1, Parsed: 5,
		Violations: []operations.CorpusViolation{
			{
				Bundle: operations.CorpusBundle{Remote: "ctxloom-default", URL: "https://github.com/ctxloom/ctxloom-default", Path: ".ctxloom/content/bundles/go.yaml"},
				Err:    errors.New("unknown key `hoooks` on line 12"),
			},
			{
				Bundle: operations.CorpusBundle{Remote: "personal", URL: "https://github.com/me/ctxloom-personal", Path: ".ctxloom/content/bundles/rust.yaml"},
				Err:    errors.New("unknown key `promts` on line 3"),
			},
		},
	}

	decoded := decodeCorpusJSON(t, report)
	assert.Equal(t, "violated", decoded["verdict"])
	assert.EqualValues(t, 1, decoded["exit_code"], "a machine consumer branches on this, not on prose")

	violations, ok := decoded["violations"].([]any)
	require.True(t, ok, "violations must encode as an array")
	require.Len(t, violations, 2)

	byBundle := map[string]string{}
	for _, v := range violations {
		entry := v.(map[string]any)
		byBundle[entry["bundle"].(string)] = entry["error"].(string)
	}
	assert.Equal(t, map[string]string{
		"ctxloom-default:.ctxloom/content/bundles/go.yaml": "unknown key `hoooks` on line 12",
		"personal:.ctxloom/content/bundles/rust.yaml":      "unknown key `promts` on line 3",
	}, byBundle, "every offender must reach a JSON consumer WITH its parse error")
}

// TestCorpusResultJSONUndeterminedIsNotSuccess pins the gate's central
// distinction in the structured form: neither the verdict string nor the exit
// code may read as a pass when nothing was verified, and the gap must carry
// the reason it could not be read.
func TestCorpusResultJSONUndeterminedIsNotSuccess(t *testing.T) {
	decoded := decodeCorpusJSON(t, operations.CorpusReport{
		RemotesConfigured: 1,
		Gaps: []operations.CorpusGap{{
			Remote: "ctxloom-default",
			URL:    "https://github.com/ctxloom/ctxloom-default",
			Err:    errors.New("no local clone"),
		}},
	})

	assert.Equal(t, "undetermined", decoded["verdict"])
	assert.EqualValues(t, 2, decoded["exit_code"], "could-not-check must not share an exit code with clean or violated")
	assert.NotContains(t, decoded["summary"], "OK:")

	gaps, ok := decoded["gaps"].([]any)
	require.True(t, ok)
	require.Len(t, gaps, 1)
	gap := gaps[0].(map[string]any)
	assert.Equal(t, "ctxloom-default", gap["subject"])
	assert.Equal(t, "no local clone", gap["error"], "a gap without its reason is unactionable")
}

// TestCorpusResultJSONEmptyCorpusSaysNothingWasChecked keeps the empty corpus
// distinguishable from a clean one in the encoding a pipeline reads. This is
// the exact shape this project keeps getting bitten by: a success-looking
// payload produced by having compared nothing.
func TestCorpusResultJSONEmptyCorpusSaysNothingWasChecked(t *testing.T) {
	decoded := decodeCorpusJSON(t, operations.CorpusReport{})

	assert.Equal(t, "undetermined", decoded["verdict"])
	assert.EqualValues(t, 2, decoded["exit_code"])
	assert.EqualValues(t, 0, decoded["parsed"])
	assert.Contains(t, decoded["summary"], "nothing was checked")
}

// TestCorpusResultJSONCleanExitsZero is the counterweight: without it every
// assertion above is satisfied by a payload builder that reports failure
// unconditionally.
func TestCorpusResultJSONCleanExitsZero(t *testing.T) {
	decoded := decodeCorpusJSON(t, operations.CorpusReport{RemotesConfigured: 2, RemotesRead: 2, Parsed: 70})

	assert.Equal(t, "clean", decoded["verdict"])
	assert.EqualValues(t, 0, decoded["exit_code"])
	assert.EqualValues(t, 70, decoded["parsed"], "the clean verdict must carry the work it rests on")

	// Empty, not null: a consumer must be able to range over these without
	// first special-casing the clean corpus.
	assert.Equal(t, []any{}, decoded["violations"])
	assert.Equal(t, []any{}, decoded["gaps"])
}

// TestCorpusTextAndJSONAgreeOnTheVerdict guards the split introduced by
// routing through emit(): there are now two renderings of one result, and the
// failure mode that matters is them disagreeing — a pipeline reading exit_code
// 0 from JSON while the log a human reads says FAIL.
func TestCorpusTextAndJSONAgreeOnTheVerdict(t *testing.T) {
	reports := map[string]operations.CorpusReport{
		"clean": {RemotesConfigured: 1, RemotesRead: 1, Parsed: 3},
		"empty": {},
		"gap": {RemotesConfigured: 1, Gaps: []operations.CorpusGap{
			{Remote: "r", URL: "u", Err: errors.New("unreadable")},
		}},
		"violated": {RemotesConfigured: 1, RemotesRead: 1, Parsed: 1, Violations: []operations.CorpusViolation{
			{Bundle: operations.CorpusBundle{Remote: "r", Path: "p"}, Err: errors.New("bad")},
		}},
	}
	for name, report := range reports {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			textCode := reportCorpus(&out, report)
			decoded := decodeCorpusJSON(t, report)

			assert.EqualValues(t, textCode, decoded["exit_code"],
				"the text renderer and the structured payload must not disagree about the verdict")
			assert.Contains(t, out.String(), decoded["summary"],
				"the human rendering must carry the same summary sentence the payload does")
		})
	}
}
