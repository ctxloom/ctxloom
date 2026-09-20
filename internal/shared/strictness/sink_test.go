package strictness_test

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// A core function that reports through a report.Sink must render, through
// the terminal renderer (strictness.Sink, the one production implementation
// of the port), to the byte-identical stderr the same site produced when
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

// A Once finding is one ledger entry per window however many times it is
// reported — a long-lived server re-consults a loaded config from every
// session, and one broken file must not become N copies of one finding
// inside a single refusal — and it re-fires in the next window, so the next
// session is refused over the same unfixed config rather than opened silently.
func TestSink_OnceFindingLedgersOncePerWindowAndRefiresInTheNext(t *testing.T) {
	restore := clidiag.SetSink(&bytes.Buffer{})
	clidiag.ResetWarnOnce()
	strictness.Reset()
	t.Cleanup(func() { restore(); clidiag.ResetWarnOnce(); strictness.Reset() })

	sink := strictness.Sink("ctxloom")
	f := report.FailOncef(report.KindConfig, "fix the config file", "yaml: did not parse")

	mark1 := strictness.Checkpoint()
	sink.Report(f)
	sink.Report(f)
	sink.Report(f)
	assert.Len(t, strictness.Since(mark1), 1, "one broken config file is one finding per window")

	mark2 := strictness.Checkpoint()
	sink.Report(f)
	assert.Len(t, strictness.Since(mark2), 1, "the next window must see the finding again")
}

// Distinct texts are distinct findings even when both are Once.
func TestSink_DistinctOnceFindingsAreDistinctLedgerEntries(t *testing.T) {
	restore := clidiag.SetSink(&bytes.Buffer{})
	strictness.Reset()
	t.Cleanup(func() { restore(); strictness.Reset() })

	sink := strictness.Sink("ctxloom")
	mark := strictness.Checkpoint()
	sink.Report(report.FailOncef(report.KindConfig, "", "yaml: did not parse"))
	sink.Report(report.FailOncef(report.KindConfig, "", "yaml: unknown key foo"))
	assert.Len(t, strictness.Since(mark), 2)
}

// Strictness is a VALUE per composition, not a process global: two modes in
// one process, one degraded and one strict, judge the same findings
// differently and neither leaks into the other — however they interleave.
func TestMode_TwoModesInOneProcessDoNotInterfere(t *testing.T) {
	found := []strictness.Finding{
		{Class: strictness.ClassConfig, Message: "ordinary"},
		{Class: strictness.ClassIsolation, Message: "hard", NonDegradable: true},
	}
	strict := strictness.Mode{Prog: "ctxloom"}
	degraded := strictness.Mode{Prog: "taskloom", Degraded: true}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			assert.Len(t, strict.Actionable(found), 2, "strict mode acts on every finding")
		}()
		go func() {
			defer wg.Done()
			got := degraded.Actionable(found)
			if assert.Len(t, got, 1, "degraded mode waives the ordinary finding") {
				assert.Equal(t, "hard", got[0].Message)
			}
		}()
	}
	wg.Wait()
}

// The coordinator's sites, captured before they moved onto a Reporter: a
// plain warning (the checkpoint snapshot fallback), a dedup'd warning (the
// runner's spool doorbell drop, which a reconnect loop repeats) and the
// runner Home's fail-loudly refusal of a second terminal nudge. The legacy
// path renders them live in the same test; the literal pins the text.
func TestDiagnosticSink_RendersWhatCoordRenderedToday(t *testing.T) {
	err := errors.New("unexpected end of JSON input")

	var found report.Findings
	rep := report.To(&found)
	rep.Warnf("coordinator: checkpoint snapshot %s unreadable, falling back to a full replay: %v", "/s/items.snapshot", err)
	rep.WarnOncef("runner: spool doorbell dropped (run channel down); %s is still on disk and will be delivered by the next sweep", "harp/out/1")
	rep.WarnOncef("runner: spool doorbell dropped (run channel down); %s is still on disk and will be delivered by the next sweep", "harp/out/1")
	rep.FailOncef(report.KindConfig,
		"construct ONE TerminalInjector per Home and call Wrap on it once per turn (see llm_serve.go) instead of building a new injector for each turn",
		"runner: a terminal nudge is already registered for this run; the second registration is refused, which silently disables the session owner's mail wake")
	rep.Recordf(report.KindApply, "narrow the relayed tool's request before the 4MiB cap fails it",
		"host-relay tool %s returned %d bytes (watch: >3MiB)", "search_content", 3_200_000)

	var legacy bytes.Buffer
	restore := clidiag.SetSink(&legacy)
	clidiag.ResetWarnOnce()
	strictness.Reset()
	clidiag.Warn("ctxloom", "coordinator: checkpoint snapshot %s unreadable, falling back to a full replay: %v", "/s/items.snapshot", err)
	clidiag.WarnOnce("ctxloom", "runner: spool doorbell dropped (run channel down); %s is still on disk and will be delivered by the next sweep", "harp/out/1")
	clidiag.WarnOnce("ctxloom", "runner: spool doorbell dropped (run channel down); %s is still on disk and will be delivered by the next sweep", "harp/out/1")
	strictness.FailOnce(strictness.ClassConfig,
		"construct ONE TerminalInjector per Home and call Wrap on it once per turn (see llm_serve.go) instead of building a new injector for each turn",
		"runner: a terminal nudge is already registered for this run; the second registration is refused, which silently disables the session owner's mail wake")
	strictness.Record(strictness.ClassApply, "narrow the relayed tool's request before the 4MiB cap fails it",
		"host-relay tool %s returned %d bytes (watch: >3MiB)", "search_content", 3_200_000)
	legacyFindings := strictness.All()
	restore()

	var rendered bytes.Buffer
	restore = clidiag.SetSink(&rendered)
	clidiag.ResetWarnOnce()
	strictness.Reset()
	t.Cleanup(func() { restore(); clidiag.ResetWarnOnce(); strictness.Reset() })
	sink := strictness.Sink("ctxloom")
	for _, f := range found {
		sink.Report(f)
	}

	assert.Equal(t, legacy.String(), rendered.String())
	assert.Equal(t, legacyFindings, strictness.All())

	const today = "ctxloom: warning: coordinator: checkpoint snapshot /s/items.snapshot unreadable, falling back to a full replay: unexpected end of JSON input\n" +
		"ctxloom: warning: runner: spool doorbell dropped (run channel down); harp/out/1 is still on disk and will be delivered by the next sweep\n" +
		"ctxloom: warning: runner: a terminal nudge is already registered for this run; the second registration is refused, which silently disables the session owner's mail wake\n"
	assert.Equal(t, today, rendered.String(), "Record is quiet on stderr; FailOnce renders once and ledgers once")
	require.Len(t, legacyFindings, 2)
	assert.Equal(t, strictness.ClassConfig, legacyFindings[0].Class)
	assert.Equal(t, strictness.ClassApply, legacyFindings[1].Class)
}
