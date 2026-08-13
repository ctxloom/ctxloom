// Untagged like probe_assert.go itself: these are the tests-of-tests for the
// capability ladder's trust anchor, and they must run under plain `just test`
// rather than only where a live engine and a paid turn are available.
//
// WHY THESE TESTS ARE THE POINT. Every @live cell in the ladder is a real,
// paid engine call; nothing hermetic ever executes one. So the ONLY thing that
// can check the verdict functions is a test that hands them canned output. If
// a verdict silently loosened — a strings.Contains flipped, a shape branch
// deleted, a check that passes on an empty nonce — every live cell would go
// green and the suite would be measuring nothing at all, loudly and expensively.
//
// FALSE-GREEN ATTEMPTS ARE FIRST-CLASS HERE. Each check below is attacked with
// the specific thing that would make it pass without the capability working:
// output carrying a DIFFERENT cell's harp (the foreign-channel attempt), an
// empty nonce (every string contains the empty string), canned output with no
// harp at all, fenced and prose-wrapped output, and a leak scan with nothing to
// scan. Those are not edge cases; they are the ways this suite would lie.
package acceptance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/harp"
)

func testCell(engine string) probeCellID {
	return probeCellID{Probe: "p0-hello-world", Engine: engine, Runtime: "host", Workspace: "none"}
}

func testVerdict(cell probeCellID) probeVerdict {
	return probeVerdict{Family: "engine-matrix", Cell: cell, Channel: channelComposedContext}
}

// --- the mint ---------------------------------------------------------------

func TestProbeMint_IsAFreshHarpNotTheSessionHarp(t *testing.T) {
	const ambient = "ugly-icy-squid"
	t.Setenv("CTXLOOM_SESSION_HARP", ambient)
	l := newProbeHarpLedger()

	got, err := l.Mint(testCell("claude-code"))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if got == ambient {
		t.Fatalf("the mint returned the AMBIENT session harp %q — a value the engine can read straight out of its own environment, so a cell using it would prove nothing about the channel it claims to test", got)
	}
	if err := harp.Validate(got); err != nil {
		t.Fatalf("a minted nonce must be a valid harp (it names a cell in evidence output and gets grepped by hand): %v", err)
	}
	if !strings.Contains(got, "-") {
		t.Fatalf("a minted nonce must be a multi-word harp, got %q — the whole reason it is not hex is that a human can hold it in their head", got)
	}
}

// The ambient harp is EXCLUDED BY CONSTRUCTION, not by luck. Seeding the
// ledger's used-set is what makes the exclusion true; if that seeding were
// dropped, this test is the only thing between us and a nonce an engine can
// satisfy from its environment.
func TestProbeMint_AmbientHarpIsSeededIntoTheUsedSet(t *testing.T) {
	const ambient = "ugly-icy-squid"
	t.Setenv("CTXLOOM_SESSION_HARP", ambient)
	l := newProbeHarpLedger()
	if _, excluded := l.used[ambient]; !excluded {
		t.Fatal("the ambient session harp must be in the ledger's used-set before the first mint — exclusion by construction, not by the improbability of a collision")
	}
}

func TestProbeMint_IsStablePerCellAndUniqueAcrossCells(t *testing.T) {
	t.Setenv("CTXLOOM_SESSION_HARP", "")
	l := newProbeHarpLedger()

	a1, err := l.Mint(testCell("claude-code"))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	a2, err := l.Mint(testCell("claude-code"))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if a1 != a2 {
		t.Fatalf("a cell's fixture step and its assertion step are separate steps and must agree on the nonce; got %q then %q", a1, a2)
	}
	b, err := l.Mint(testCell("codex"))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if a1 == b {
		t.Fatalf("two cells minted the same harp %q — a collision makes the foreign-harp scanner report an isolation leak that is really a name clash, the most expensive false alarm this suite can raise", a1)
	}
}

func TestProbeMint_SnapshotIsACopy(t *testing.T) {
	l := newProbeHarpLedger()
	cell := testCell("claude-code")
	if _, err := l.Mint(cell); err != nil {
		t.Fatalf("mint: %v", err)
	}
	snap := l.Snapshot()
	delete(snap, cell)
	if len(l.Snapshot()) != 1 {
		t.Fatal("Snapshot must hand back a copy: a caller that scans the map must not be able to empty the ledger and make every later scan inert")
	}
}

// --- cell identity ----------------------------------------------------------

func TestProbeCellID_StampsEveryAxisAndOmitsEmptyOnes(t *testing.T) {
	full := probeCellID{Probe: "p4-plan-sentinel", Engine: "codex", Runtime: "container", Workspace: "worktree", Variant: "control"}
	for _, want := range []string{"probe=p4-plan-sentinel", "engine=codex", "runtime=container", "workspace=worktree", "variant=control"} {
		if !strings.Contains(full.String(), want) {
			t.Fatalf("a cell stamp missing %q is unusable in the one context it is ever read in — a matrix of them; got %s", want, full)
		}
	}
	bare := probeCellID{Engine: "kiro", Runtime: "host", Workspace: "none"}
	if strings.Contains(bare.String(), "probe=") || strings.Contains(bare.String(), "variant=") {
		t.Fatalf("a probe with one dimension must not stamp empty fields into its own evidence; got %s", bare)
	}
}

// --- ran: run failure and the silent no-op ----------------------------------

func TestProbeRan_RunFailureIsNamedAndCarriesStderr(t *testing.T) {
	v := testVerdict(testCell("claude-code"))
	_, err := v.ran(probeRun{Err: errors.New("exit status 3"), ExitCode: 3, Stderr: "ctxloom: isolation finding: container could not start"})
	if err == nil {
		t.Fatal("a failed run must not pass")
	}
	if !strings.Contains(err.Error(), "the run itself failed") || !strings.Contains(err.Error(), "container could not start") {
		t.Fatalf("a failed run must be named as such and carry stderr — stderr is where ctxloom says WHY; got: %v", err)
	}
	if shape, ok := probeShapeOf(err); !ok || shape != shapeRunFailed {
		t.Fatalf("the shape must ride the error as data so a sweep's red-map diff never has to match prose; got %q ok=%v", shape, ok)
	}
}

func TestProbeRan_EmptyStdoutOnCleanExitIsNamedASilentNoOp(t *testing.T) {
	v := testVerdict(testCell("claude-code"))
	_, err := v.ran(probeRun{Stdout: "   \n  ", Stderr: "banner"})
	if err == nil {
		t.Fatal("exit 0 with zero bytes must not pass — that is this project's characteristic bug, not a pass")
	}
	if !strings.Contains(err.Error(), "silent no-op") {
		t.Fatalf("empty stdout must be named a silent no-op, never blurred into some other mismatch; got: %v", err)
	}
	if shape, _ := probeShapeOf(err); shape != shapeSilentNoOp {
		t.Fatalf("shape must be %q, got %q", shapeSilentNoOp, shape)
	}
}

func TestProbeRan_TrimsWhitespaceAndNothingElse(t *testing.T) {
	v := testVerdict(testCell("claude-code"))
	got, err := v.ran(probeRun{Stdout: "\n  {\"hello\":\"x\"}  \n"})
	if err != nil {
		t.Fatalf("surrounding whitespace must not fail a cell: %v", err)
	}
	if got != `{"hello":"x"}` {
		t.Fatalf("ran must return the whitespace-trimmed output and strip nothing else; got %q", got)
	}
}

// --- jsonObject: the strictness decision, made executable -------------------

func TestProbeJSONObject_FencesArePreservedAsAFormatFailure(t *testing.T) {
	v := testVerdict(testCell("claude-code"))
	_, err := v.jsonObject("```json\n{\"hello\":\"swift-amber-falcon\"}\n```")
	if err == nil {
		t.Fatal("code fences must not pass — loosening this deletes the signal the ladder exists to produce")
	}
	if shape, _ := probeShapeOf(err); shape != shapeOutputFormat {
		t.Fatalf("fences must be an output-format failure, got %q: %v", shape, err)
	}
}

func TestProbeJSONObject_PreambleIsAFormatFailure(t *testing.T) {
	v := testVerdict(testCell("claude-code"))
	_, err := v.jsonObject("Here is your JSON:\n{\"hello\":\"swift-amber-falcon\"}")
	if err == nil {
		t.Fatal("a prose preamble must not pass")
	}
	if shape, _ := probeShapeOf(err); shape != shapeOutputFormat {
		t.Fatalf("a preamble must be an output-format failure, got %q", shape)
	}
}

func TestProbeJSONObject_NonObjectIsAFormatFailureNotAPanic(t *testing.T) {
	v := testVerdict(testCell("claude-code"))
	if _, err := v.jsonObject(`["swift-amber-falcon"]`); err == nil {
		t.Fatal("a JSON array must not pass")
	}
}

// --- carriesNonce: the channel-honesty check --------------------------------

// FALSE-GREEN ATTEMPT #1, and the one that would be invisible: an empty nonce.
// strings.Contains(anything, "") is TRUE, so a probe that forgot to mint would
// sail through the single check that proves its channel delivered anything.
func TestProbeCarriesNonce_EmptyNonceIsRefusedRatherThanVacuouslyPassing(t *testing.T) {
	v := testVerdict(testCell("claude-code"))
	err := v.carriesNonce(`{"hello":"world"}`, "")
	if err == nil {
		t.Fatal("an empty nonce must be refused: every value contains the empty string, so this would pass without proving the channel delivered anything")
	}
	if !strings.Contains(err.Error(), "no minted nonce") {
		t.Fatalf("the refusal must name the cause (a probe that never minted), got: %v", err)
	}
}

// FALSE-GREEN ATTEMPT #2, the foreign channel: the cell's output carries a harp
// — just not THIS cell's. A check that only asked "does it look like a harp"
// would pass. It must key on the exact minted value.
func TestProbeCarriesNonce_AnotherCellsHarpDoesNotSatisfyThisCell(t *testing.T) {
	v := testVerdict(testCell("claude-code"))
	const mine, theirs = "swift-amber-falcon", "brisk-copper-otter"
	err := v.carriesNonce(`{"hello":"`+theirs+`"}`, mine)
	if err == nil {
		t.Fatalf("a DIFFERENT cell's harp must not satisfy this cell's delivery check — that is the foreign-channel false green the minted nonce exists to rule out")
	}
	if !strings.Contains(err.Error(), mine) {
		t.Fatalf("the failure must name the harp that was actually expected, got: %v", err)
	}
}

// FALSE-GREEN ATTEMPT #3: canned, well-formed output with no harp in it at all
// — what a memorised "hello world" answer looks like. It must be attributed to
// the CHANNEL, not reported as a generic mismatch, because the channel is a
// different subsystem from output formatting.
func TestProbeCarriesNonce_CannedOutputIsAttributedToTheDeclaredChannel(t *testing.T) {
	v := testVerdict(testCell("claude-code"))
	err := v.carriesNonce(`{"hello":"world"}`, "swift-amber-falcon")
	if err == nil {
		t.Fatal("output with no trace of the nonce must not pass")
	}
	if !strings.Contains(err.Error(), string(channelComposedContext.Shape)) {
		t.Fatalf("a missing nonce must be labelled with the CHANNEL's own failure shape (%s), got: %v", channelComposedContext.Shape, err)
	}
	if !strings.Contains(err.Error(), channelComposedContext.Where) {
		t.Fatalf("the failure must say WHERE the nonce was planted — a probe that cannot name its channel is not testing one; got: %v", err)
	}
}

// The channel is carried, not hardcoded: a probe planting into an MCP tool
// result must not report a CONTEXT-DELIVERY failure, or the ladder's whole
// attribution story collapses into one indistinguishable red.
func TestProbeCarriesNonce_ChannelLabelComesFromTheProbeNotTheHelper(t *testing.T) {
	v := probeVerdict{Family: "probe", Cell: testCell("codex"), Channel: channelMCPToolResult}
	err := v.carriesNonce(`{"nonce":"world"}`, "swift-amber-falcon")
	if err == nil {
		t.Fatal("missing nonce must fail")
	}
	if !strings.Contains(err.Error(), string(channelMCPToolResult.Shape)) {
		t.Fatalf("the MCP probe's red must be labelled %s, not the context channel's label; got: %v", channelMCPToolResult.Shape, err)
	}
	if strings.Contains(err.Error(), string(channelComposedContext.Shape)) {
		t.Fatalf("the helper must not stamp a channel the probe did not declare; got: %v", err)
	}
}

func TestProbeCarriesNonce_PresentNoncePasses(t *testing.T) {
	v := testVerdict(testCell("claude-code"))
	if err := v.carriesNonce(`{"hello":"swift-amber-falcon"}`, "swift-amber-falcon"); err != nil {
		t.Fatalf("a present nonce must pass: %v", err)
	}
}

// --- exactObject ------------------------------------------------------------

func TestProbeExactObject_ExactPasses(t *testing.T) {
	v := testVerdict(testCell("claude-code"))
	got := map[string]any{"hello": "swift-amber-falcon"}
	if err := v.exactObject(got, "", "hello", "swift-amber-falcon"); err != nil {
		t.Fatalf("the exact object must pass: %v", err)
	}
}

func TestProbeExactObject_ExtraKeyIsAShapeFailure(t *testing.T) {
	v := testVerdict(testCell("claude-code"))
	got := map[string]any{"hello": "swift-amber-falcon", "note": "done"}
	err := v.exactObject(got, "", "hello", "swift-amber-falcon")
	if err == nil {
		t.Fatal("an extra key must not pass")
	}
	if shape, _ := probeShapeOf(err); shape != shapeShape {
		t.Fatalf("an extra key is a shape failure, got %q", shape)
	}
}

func TestProbeExactObject_MissingKeyIsAShapeFailure(t *testing.T) {
	v := testVerdict(testCell("claude-code"))
	err := v.exactObject(map[string]any{"greeting": "swift-amber-falcon"}, "", "hello", "swift-amber-falcon")
	if err == nil {
		t.Fatal("the wrong key must not pass")
	}
	if shape, _ := probeShapeOf(err); shape != shapeShape {
		t.Fatalf("a missing key is a shape failure, got %q", shape)
	}
}

// A wrong VALUE is a different bug from a wrong SHAPE: the engine answered the
// question, with the wrong answer. Collapsing the two would make a delivery
// regression read as a chatty model.
func TestProbeExactObject_WrongValueIsAValueFailureNotAShapeFailure(t *testing.T) {
	v := testVerdict(testCell("claude-code"))
	err := v.exactObject(map[string]any{"hello": "brisk-copper-otter"}, "", "hello", "swift-amber-falcon")
	if err == nil {
		t.Fatal("the wrong value must not pass")
	}
	if shape, _ := probeShapeOf(err); shape != shapeValue {
		t.Fatalf("a wrong value is a VALUE failure, got %q: %v", shape, err)
	}
}

// --- PX: the foreign-harp scanner -------------------------------------------

func TestForeignHarps_AnotherCellsHarpInThisCellsOutputIsALeak(t *testing.T) {
	self := testCell("claude-code")
	other := testCell("codex")
	ledger := map[probeCellID]string{self: "swift-amber-falcon", other: "brisk-copper-otter"}

	err := assertNoForeignHarps(self, ledger,
		probeArtifact{Name: "stdout", Body: `{"hello":"swift-amber-falcon","leaked":"brisk-copper-otter"}`})
	if err == nil {
		t.Fatal("another cell's harp in this cell's output is cross-cell contamination and must fail")
	}
	if shape, _ := probeShapeOf(err); shape != shapeLeak {
		t.Fatalf("a leak must carry the leak shape, got %q", shape)
	}
	if !strings.Contains(err.Error(), "brisk-copper-otter") || !strings.Contains(err.Error(), "engine=codex") {
		t.Fatalf("a leak report must name the harp AND the cell it belongs to, or nobody can chase it; got: %v", err)
	}
}

func TestForeignHarps_ThisCellsOwnHarpIsNotALeak(t *testing.T) {
	self := testCell("claude-code")
	ledger := map[probeCellID]string{self: "swift-amber-falcon", testCell("codex"): "brisk-copper-otter"}
	if err := assertNoForeignHarps(self, ledger, probeArtifact{Name: "stdout", Body: `{"hello":"swift-amber-falcon"}`}); err != nil {
		t.Fatalf("a cell echoing its OWN nonce is the probe working, not a leak: %v", err)
	}
}

// The scan's own false green: nothing to read. A scanner handed no artifacts
// finds no leak for the most literal reason there is.
func TestForeignHarps_NoArtifactsIsAnErrorNotAPass(t *testing.T) {
	self := testCell("claude-code")
	ledger := map[probeCellID]string{self: "swift-amber-falcon", testCell("codex"): "brisk-copper-otter"}
	if err := assertNoForeignHarps(self, ledger); err == nil {
		t.Fatal("a scan with nothing to scan must refuse, not report cleanliness")
	}
}

// The second false green: a cell whose nonce never went through the ledger.
// Its own harp would then read as foreign, or — worse — the ledger is empty and
// the scan is inert while looking like it ran.
func TestForeignHarps_CellMissingFromTheLedgerIsAnError(t *testing.T) {
	self := testCell("claude-code")
	ledger := map[probeCellID]string{testCell("codex"): "brisk-copper-otter"}
	err := assertNoForeignHarps(self, ledger, probeArtifact{Name: "stdout", Body: "{}"})
	if err == nil {
		t.Fatal("a cell absent from the mint ledger must refuse: its nonce was never minted through the ledger, so the scan is measuring the wrong thing")
	}
	if !strings.Contains(err.Error(), "ledger") {
		t.Fatalf("the refusal must name the ledger as the cause, got: %v", err)
	}
}

// The single-cell case (`just capability-probe` runs exactly one) is legitimately
// inert — but it must SAY so rather than pass as evidence of isolation.
func TestForeignHarps_LedgerHoldingOnlySelfIsInertNotAFailure(t *testing.T) {
	self := testCell("claude-code")
	ledger := map[probeCellID]string{self: "swift-amber-falcon"}
	if err := assertNoForeignHarps(self, ledger, probeArtifact{Name: "stdout", Body: "{}"}); err != nil {
		t.Fatalf("a one-cell run has nothing foreign to find and must not red: %v", err)
	}
}

func TestProbeFileArtifact_MissingFileIsAnErrorNotAnEmptyScan(t *testing.T) {
	if _, err := probeFileArtifact("transcript.jsonl", filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("an unreadable artifact must refuse: a scan that skips it reports no leak for the wrong reason")
	}
}

func TestProbeFileArtifact_ReadsTheFileItWasGiven(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(path, []byte(`{"harp":"brisk-copper-otter"}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	a, err := probeFileArtifact("transcript.jsonl", path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(a.Body, "brisk-copper-otter") {
		t.Fatalf("the artifact must carry the file's bytes, got %q", a.Body)
	}
}

// --- PX: the ambient-harp belt and braces -----------------------------------

func TestAmbientHarp_SessionHarpReachingAChildIsALeak(t *testing.T) {
	t.Setenv("CTXLOOM_SESSION_HARP", "ugly-icy-squid")
	err := assertNoAmbientSessionHarp(testCell("claude-code"),
		probeArtifact{Name: "composed context", Body: "session ugly-icy-squid context follows"})
	if err == nil {
		t.Fatal("the suite's own session harp reaching a child's composed context must fail: it means a nonce check somewhere could be satisfied through a channel no probe declared")
	}
	if shape, _ := probeShapeOf(err); shape != shapeLeak {
		t.Fatalf("shape must be %q, got %q", shapeLeak, shape)
	}
}

func TestAmbientHarp_UnsetVarAssertsNothingRatherThanInventingAFixture(t *testing.T) {
	t.Setenv("CTXLOOM_SESSION_HARP", "")
	if err := assertNoAmbientSessionHarp(testCell("claude-code"), probeArtifact{Name: "stdout", Body: "anything at all"}); err != nil {
		t.Fatalf("with no ambient harp there is no claim to check: %v", err)
	}
}
