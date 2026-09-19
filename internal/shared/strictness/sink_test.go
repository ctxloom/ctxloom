package strictness_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// A core function that reports through a report.Sink must render, through
// the ONE CLI sink, to the byte-identical stderr the same site produced when
// it called clidiag/strictness itself. The expected text is produced by the
// live legacy path in the same test, not hand-written, and pinned once as a
// literal so neither side can drift silently.
func TestDiagnosticSink_RendersWhatClidiagRenderedToday(t *testing.T) {
	err := errors.New("permission denied")

	// Three representative sites, as they report now: a plain warning
	// (sessions' transcript link), a dedup'd warning (profiles' unknown
	// key), and a warn+fail pair (config's trust root).
	var found report.Findings
	rep := report.To(&found)
	rep.Warnf("engine transcript link: %v", err)
	rep.WarnOncef("%s", "profile x: unknown key y")
	rep.WarnOncef("%s", "profile x: unknown key y")
	rep.Warnf("allowed_signers %s exists but cannot be read, its keys are NOT trusted this session: %v", "/p", err)
	rep.Failf(report.KindTrust, "make the allowed_signers file readable, or remove it",
		"allowed_signers %s exists but cannot be read: %v", "/p", err)

	// The legacy path, live, for the same sites.
	var legacy bytes.Buffer
	restore := clidiag.SetSink(&legacy)
	clidiag.ResetWarnOnce()
	strictness.Reset()
	clidiag.Warn("ctxloom", "engine transcript link: %v", err)
	clidiag.WarnOnce("ctxloom", "%s", "profile x: unknown key y")
	clidiag.WarnOnce("ctxloom", "%s", "profile x: unknown key y")
	clidiag.Warn("ctxloom", "allowed_signers %s exists but cannot be read, its keys are NOT trusted this session: %v", "/p", err)
	strictness.Fail(strictness.ClassTrust, "make the allowed_signers file readable, or remove it",
		"allowed_signers %s exists but cannot be read: %v", "/p", err)
	legacyFindings := strictness.All()
	restore()

	// The new path: the same findings through the CLI's sink.
	var rendered bytes.Buffer
	restore = clidiag.SetSink(&rendered)
	clidiag.ResetWarnOnce()
	strictness.Reset()
	t.Cleanup(func() { restore(); clidiag.ResetWarnOnce(); strictness.Reset() })
	sink := strictness.Sink("ctxloom")
	for _, f := range found {
		sink.Report(f)
	}

	assert.Equal(t, legacy.String(), rendered.String(), "the sink must render byte-for-byte what the sites rendered themselves")
	assert.Equal(t, legacyFindings, strictness.All(), "a fail-loudly finding must land in the startup ledger exactly as strictness.Fail recorded it")

	// Pinned once, so a change on BOTH sides still shows.
	const today = "ctxloom: warning: engine transcript link: permission denied\n" +
		"ctxloom: warning: profile x: unknown key y\n" +
		"ctxloom: warning: allowed_signers /p exists but cannot be read, its keys are NOT trusted this session: permission denied\n" +
		"ctxloom: warning: allowed_signers /p exists but cannot be read: permission denied\n"
	assert.Equal(t, today, rendered.String())
	require.Len(t, legacyFindings, 1)
	assert.Equal(t, strictness.Finding{
		Class:   strictness.ClassTrust,
		Message: "allowed_signers /p exists but cannot be read: permission denied",
		FixIt:   "make the allowed_signers file readable, or remove it",
	}, legacyFindings[0])
}

// Structured mode is a property of the sink's process, not of the site: a
// finding rendered while --format json is on comes out as the same envelope
// clidiag wrote.
func TestDiagnosticSink_StructuredModeRendersTheEnvelope(t *testing.T) {
	var rendered bytes.Buffer
	restore := clidiag.SetSink(&rendered)
	clidiag.SetStructured(true)
	t.Cleanup(func() { clidiag.SetStructured(false); restore() })

	strictness.Sink("ctxloom").Report(report.Warnf("engine transcript link: %v", errors.New("permission denied")))
	assert.Equal(t, `{"prog":"ctxloom","warning":"engine transcript link: permission denied"}`+"\n", rendered.String())
}

// The prog is the sink's, so a family binary renders its own name.
func TestDiagnosticSink_ProgIsTheSinks(t *testing.T) {
	var rendered bytes.Buffer
	restore := clidiag.SetSink(&rendered)
	t.Cleanup(restore)

	strictness.Sink("taskloom").Report(report.Warnf("refused"))
	assert.Equal(t, "taskloom: warning: refused\n", rendered.String())
}

// A quiet finding lands in the ledger and prints nothing.
func TestDiagnosticSink_QuietRecordsWithoutRendering(t *testing.T) {
	var rendered bytes.Buffer
	restore := clidiag.SetSink(&rendered)
	strictness.Reset()
	t.Cleanup(func() { restore(); strictness.Reset() })

	strictness.Sink("ctxloom").Report(report.Recordf(report.KindApply, "narrow it", "too big"))
	assert.Empty(t, rendered.String())
	require.Len(t, strictness.All(), 1)
	assert.Equal(t, "too big", strictness.All()[0].Message)
}
