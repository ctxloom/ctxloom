// Untagged, like the probe it checks: `just test` runs every case below with no
// built binary, no live engine and no paid turn.
//
// P3's verdict is the one part of the hook-firing probe a hermetic gate can
// execute — every cell is @live — and it is also the part most likely to be
// quietly loosened later. The natural "just make the cell pass" edits are all
// here as RED cases, on purpose, as the executable record of decisions somebody
// made once:
//
//   - a missing stamp file must FAIL, never skip. This is the probe's central
//     finding (the hook did not fire) and the shape a careless `if err != nil {
//     return nil }` would delete.
//   - an EMPTY stamp file must fail, and be named a silent no-op rather than an
//     ordinary mismatch.
//   - a stamp file carrying somebody else's harp must fail, so a leftover file
//     from a previous cell cannot green this one.
//   - stage (b)'s echo must be satisfiable ONLY by the stdout harp, never by the
//     argv harp — the two-channel property the probe rests on.
//
// The false-green cases are the ones with real teeth: each plants the RIGHT
// value through the WRONG channel and requires a red anyway.
package acceptance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hookCell builds a stage-(a)-only cell whose run succeeded, so each test below
// varies exactly one thing.
func hookCell(stampHarp, stampBody string) *hookProbeState {
	return &hookProbeState{
		engine: "claude-code", runtime: "host", workspace: "none",
		stampHarp: stampHarp,
		stampBody: stampBody,
		stampPath: "/tmp/p3-hook-probe/stamp-claude-code.txt",
		stdout:    "OK",
	}
}

func TestHookProbeAssert_StampCarryingTheArgvHarpPasses(t *testing.T) {
	const harp = "swift-amber-falcon"
	if err := hookProbeAssert(hookCell(harp, harp+"\n")); err != nil {
		t.Fatalf("a stamp file carrying the argv harp is the proof this probe exists to collect, got: %v", err)
	}
}

// A hook that fired TWICE is still a hook that fired. The script appends, so a
// second firing adds a line; the verdict must not require the file to hold
// exactly one.
func TestHookProbeAssert_RepeatedFiringStillPasses(t *testing.T) {
	const harp = "swift-amber-falcon"
	if err := hookProbeAssert(hookCell(harp, harp+"\n"+harp+"\n")); err != nil {
		t.Fatalf("a hook that fired twice has still fired, got: %v", err)
	}
}

// THE CENTRAL CASE. A missing stamp file is the finding, and it must be a
// failure that names the channel — never a skip, never a nil.
func TestHookProbeAssert_MissingStampFileIsAHookDeliveryFailure(t *testing.T) {
	h := hookCell("swift-amber-falcon", "")
	h.stampErr = os.ErrNotExist
	err := hookProbeAssert(h)
	if err == nil {
		t.Fatal("a missing stamp file must FAIL: it is the probe's whole finding — the engine did not exec the hook ctxloom wrote")
	}
	shape, ok := probeShapeOf(err)
	if !ok || shape != channelHookStamp.Shape {
		t.Fatalf("a missing stamp must carry the hook channel's own shape %q so a sweep can diff it, got shape=%q ok=%v (%v)",
			channelHookStamp.Shape, shape, ok, err)
	}
	if !strings.Contains(err.Error(), "DID NOT FIRE") {
		t.Fatalf("the failure must say plainly that the hook did not fire, got: %v", err)
	}
}

// ctxloom's characteristic bug, at the hook layer: the engine exec'd the
// command and it produced nothing. Different subsystem from "never ran", so it
// gets its own shape.
func TestHookProbeAssert_EmptyStampFileIsNamedASilentNoOp(t *testing.T) {
	err := hookProbeAssert(hookCell("swift-amber-falcon", "   \n\t\n"))
	if err == nil {
		t.Fatal("a stamp file that exists and is EMPTY must not pass — exit 0 plus zero bytes is this project's characteristic failure")
	}
	shape, _ := probeShapeOf(err)
	if shape != shapeSilentNoOp {
		t.Fatalf("an empty stamp must be shaped as a silent no-op, not blurred into a mismatch, got %q (%v)", shape, err)
	}
}

// A leftover stamp from an earlier cell must not green this one. The step
// deletes the file before the run; this is the assertion-side half of the same
// guarantee, so neither alone is load-bearing.
func TestHookProbeAssert_AnotherCellsHarpInTheStampIsAValueFailure(t *testing.T) {
	err := hookProbeAssert(hookCell("swift-amber-falcon", "brave-copper-otter\n"))
	if err == nil {
		t.Fatal("a stamp carrying a DIFFERENT cell's harp must not pass — that is somebody else's evidence")
	}
	shape, _ := probeShapeOf(err)
	if shape != shapeValue {
		t.Fatalf("wrong-harp bytes are a VALUE failure (something ran, not this hook with this argv), got %q (%v)", shape, err)
	}
}

// An unminted harp would make strings.Contains vacuously true, so a cell that
// forgot to mint would sail through the one check that proves its channel. The
// probe refuses rather than passing.
func TestHookProbeAssert_EmptyArgvHarpIsRefusedRatherThanPassingVacuously(t *testing.T) {
	err := hookProbeAssert(hookCell("", "anything at all\n"))
	if err == nil {
		t.Fatal("an empty argv harp must be refused: every file contains the empty string, so this check would pass without proving anything")
	}
	if !strings.Contains(err.Error(), "nothing to look for") {
		t.Fatalf("the refusal must explain itself as a caller bug, got: %v", err)
	}
}

func TestHookProbeAssert_RunFailureIsReportedWithStderr(t *testing.T) {
	h := hookCell("swift-amber-falcon", "swift-amber-falcon\n")
	h.runErr = errors.New("exit status 3")
	h.exitCode = 3
	h.stderr = "ctxloom: engine launch failed"
	err := hookProbeAssert(h)
	if err == nil {
		t.Fatal("a failed run must not pass even when the stamp is there — the cell measured a run that did not happen")
	}
	if !strings.Contains(err.Error(), "the run itself failed") || !strings.Contains(err.Error(), "engine launch failed") {
		t.Fatalf("a failed run must be named as such and carry stderr, got: %v", err)
	}
}

// The carriage evidence must reach the failure message. It is what tells the
// next person whether to open ctxloom's writer (we never wrote the hook) or the
// vendor's changelog (we wrote it, they did not run it).
func TestHookProbeAssert_FailureCarriesTheCarriageEvidence(t *testing.T) {
	h := hookCell("swift-amber-falcon", "")
	h.stampErr = os.ErrNotExist
	h.carriage = "hook command found in: .claude/settings.json"

	err := hookProbeAssert(h)
	if err == nil || !strings.Contains(err.Error(), ".claude/settings.json") {
		t.Fatalf("a red must carry where the hook command was found, or it cannot say whether carriage or firing failed, got: %v", err)
	}

	h.carriage = ""
	err = hookProbeAssert(h)
	if err == nil || !strings.Contains(err.Error(), "not proof of absence") {
		t.Fatalf("with no carriage hit the message must stop short of claiming a proven absence, got: %v", err)
	}
}

// --- stage (b): hook-output ingestion ------------------------------------------

func hookEchoCell(stampHarp, echoHarp, stdout string) *hookProbeState {
	h := &hookProbeState{
		engine: "codex", runtime: "host", workspace: "none",
		stampHarp: stampHarp,
		echoHarp:  echoHarp,
		stampBody: stampHarp + "\n",
		stampPath: "/tmp/p3-hook-probe/stamp-codex.txt",
		stdout:    stdout,
	}
	return h
}

func TestHookProbeAssert_EchoCellPassesOnTheExactObject(t *testing.T) {
	err := hookProbeAssert(hookEchoCell("swift-amber-falcon", "brave-copper-otter",
		`{"hook":"brave-copper-otter"}`))
	if err != nil {
		t.Fatalf("stage (b) must pass when the stdout harp comes back in the exact object, got: %v", err)
	}
}

// THE FALSE-GREEN ATTEMPT THAT MATTERS MOST. The engine echoes the ARGV harp —
// the value it could have read straight out of the settings file it was handed
// — instead of the harp that existed only on the hook's stdout. Stage (b) must
// red: the two channels are separate by construction and one must never satisfy
// the other.
func TestHookProbeAssert_EchoingTheArgvHarpDoesNotSatisfyTheStdoutChannel(t *testing.T) {
	err := hookProbeAssert(hookEchoCell("swift-amber-falcon", "brave-copper-otter",
		`{"hook":"swift-amber-falcon"}`))
	if err == nil {
		t.Fatal("the argv harp must NOT satisfy the stdout channel: it is quotable from the settings file, which is precisely the reachability stage (b) is not testing")
	}
	shape, _ := probeShapeOf(err)
	if shape != channelHookStdout.Shape {
		t.Fatalf("the red must be attributed to hook-output INGESTION, not to firing, got %q (%v)", shape, err)
	}
}

// Stage (a) is checked BEFORE stage (b), so a cell whose hook never fired is
// reported as a firing failure even if its stdout happens to carry the echo
// harp. The order is the diagnostic: a stage-(b) red is only meaningful once
// firing is proven.
// BOTH STAGES FAIL HERE, and that is the point. Either ordering reds this cell,
// so a test where only one stage fails cannot tell the two orderings apart —
// measured: a mutant that judged ingestion first SURVIVED such a test. With
// both failing, the reported shape is the whole assertion: it must be the
// STAMP channel, because "the hook never fired" explains the missing echo,
// while "the echo did not arrive" explains nothing about the missing file and
// would send a reader to codex's ingestion when the hook never ran.
func TestHookProbeAssert_FiringIsJudgedBeforeIngestion(t *testing.T) {
	h := hookEchoCell("swift-amber-falcon", "brave-copper-otter", `{"hook":"something-else-entirely"}`)
	h.stampBody = ""
	h.stampErr = os.ErrNotExist

	err := hookProbeAssert(h)
	if err == nil {
		t.Fatal("a cell whose hook never fired must not pass on its stdout")
	}
	shape, _ := probeShapeOf(err)
	if shape != channelHookStamp.Shape {
		t.Fatalf("firing must be judged FIRST: with both stages failing the red belongs to the stamp channel, not to ingestion, got %q (%v)", shape, err)
	}

	// And the ordering must not be an accident of this one input: a cell whose
	// hook DID fire and whose echo did not arrive must still report ingestion.
	h.stampBody = h.stampHarp + "\n"
	h.stampErr = nil
	err = hookProbeAssert(h)
	if shape, _ := probeShapeOf(err); shape != channelHookStdout.Shape {
		t.Fatalf("with firing proven, a missing echo must be reported as an INGESTION failure, got %q (%v)", shape, err)
	}
}

// Stage (b) inherits the floor's output strictness, and that decision is
// recorded here rather than left to be rediscovered: fences are a FINDING.
func TestHookProbeAssert_EchoCellRefusesFencesAndPreamble(t *testing.T) {
	for name, stdout := range map[string]string{
		"fenced":   "```json\n{\"hook\":\"brave-copper-otter\"}\n```",
		"preamble": "Here is your JSON:\n{\"hook\":\"brave-copper-otter\"}",
	} {
		t.Run(name, func(t *testing.T) {
			err := hookProbeAssert(hookEchoCell("swift-amber-falcon", "brave-copper-otter", stdout))
			if err == nil {
				t.Fatal("decorated output must be a finding, never something the matcher launders away")
			}
			if shape, _ := probeShapeOf(err); shape != shapeOutputFormat {
				t.Fatalf("decoration is an OUTPUT-FORMAT failure, got %q (%v)", shape, err)
			}
		})
	}
}

// A stage-(a)-only cell must not be judged on its stdout at all. kiro is the
// live reason: its P0 host/none cell is red-mapped because terminal decoration
// leaks into a one-shot run's stdout, and a P3 cell that parsed stdout would
// inherit that red and report a hook-firing failure that is nothing of the kind.
func TestHookProbeAssert_StageAOnlyCellIgnoresStdoutShape(t *testing.T) {
	h := hookCell("swift-amber-falcon", "swift-amber-falcon\n")
	h.engine = "kiro"
	h.stdout = "\x1b[38;5;141m> \x1b[0mOK\x1b[0m"
	if err := hookProbeAssert(h); err != nil {
		t.Fatalf("a stage-(a)-only cell asserts a FILE; decorated stdout must not red it, got: %v", err)
	}
}

// --- the per-engine stage-(b) declaration ---------------------------------------

// The declaration must mirror production's own ApproachTable, and getting it
// wrong in the permissive direction is the expensive mistake: it would red
// claude and kiro for failing to do something ctxloom never asked of them.
func TestHookProbeIngestsHookStdout_MatchesTheDeclaredApproachTables(t *testing.T) {
	for engine, want := range map[string]bool{
		"codex":       true,  // codexApproaches: ApproachHook FIRST for SurfaceContext — the default context route
		"claude-code": false, // declares ApproachHook, but SurfaceFor resolves it to noopContextDelivery
		"kiro":        false, // no hook entry in its ApproachTable; context arrives via steering files
		"opencode":    false, // noHooksReason: no hook mechanism at all
	} {
		if got := hookProbeIngestsHookStdout(engine); got != want {
			t.Errorf("hookProbeIngestsHookStdout(%q) = %v, want %v — this function decides whether a cell is asked to prove ingestion, so a wrong answer reds an engine for a claim ctxloom never made", engine, got, want)
		}
	}
}

// --- the fixture material -------------------------------------------------------

// The script IS the probe. If it stops stamping, every cell that uses it becomes
// a tautology — a green proving nothing — so the stamp line is pinned here.
//
// The path is pinned as SCRIPT-RELATIVE, resolved from $0. That is not a style
// choice: a cwd-relative path lands the proof wherever the vendor binary chose
// to exec the hook from, and a host-absolute path does not exist inside a
// container or a per-agent worktree — so either one silently reds a container or
// worktree cell as "the hook never fired" when it fired perfectly.
func TestHookProbeScript_StampsItsArgvBesideItself(t *testing.T) {
	script := hookProbeScript("stamp.txt", "")

	if !strings.Contains(script, `"$1"`) {
		t.Error("the script must read the harp from its ARGV: that is the planted channel, and a harp baked into the script body would prove only that the fixture can write a file")
	}
	if !strings.Contains(script, `>> "$(dirname "$0")"/'stamp.txt'`) {
		t.Errorf("the script must APPEND to a stamp path resolved from $0 (append, so a second firing is visible rather than flattened; from $0, so the proof lands beside the script on every axis), got:\n%s", script)
	}
	if strings.Contains(script, ">> '/") {
		t.Errorf("the stamp path must not be host-absolute — such a path exists in neither a container's filesystem namespace nor a per-agent worktree, and the cell would red as a hook that never fired, got:\n%s", script)
	}
	if strings.Contains(script, "hookSpecificOutput") {
		t.Error("a stage-(a)-only script must print NOTHING on stdout: an engine that ingests hook output would be handed context this cell never intended to plant")
	}
}

func TestHookProbeScript_EchoCellEmitsTheEngineHookOutputEnvelope(t *testing.T) {
	script := hookProbeScript("stamp.txt", "brave-copper-otter")
	for _, want := range []string{"hookSpecificOutput", "additionalContext", "brave-copper-otter"} {
		if !strings.Contains(script, want) {
			t.Errorf("a stage-(b) script must emit the engine's own hook-output envelope carrying the stdout harp; %q missing from:\n%s", want, script)
		}
	}
	if !strings.Contains(script, `"$1"`) {
		t.Error("a stage-(b) script must still stamp: stage (b) is judged only after firing is proven")
	}
}

// Paths and JSON bodies contain characters a shell would otherwise eat. A
// broken quote here would not fail loudly — it would produce a hook that runs
// and stamps the wrong thing, or nothing.
func TestHookProbeScript_QuotesHostilePaths(t *testing.T) {
	script := hookProbeScript("it's a dir/$HOME/stamp.txt", "")
	if !strings.Contains(script, `'it'\''s a dir/$HOME/stamp.txt'`) {
		t.Errorf("a path with a quote, a space and a $ must be single-quoted and escaped, got:\n%s", script)
	}
}

// The hook must reach the engine through the BUNDLE authoring surface, which is
// the path a real user's profile takes (bundles.BundleHooks.SessionStart →
// config.ResolveBundleHooks → the per-engine SettingsWriter). A fixture that
// wrote the engine's native file itself would prove the engine execs files we
// hand-made, which is not the claim.
func TestHookProbeBundleYAML_DeclaresASessionStartCommandHookCarryingTheHarp(t *testing.T) {
	y := hookProbeBundleYAML("/tmp/p3/stamp.sh", "swift-amber-falcon")
	for _, want := range []string{
		"hooks:",
		"session_start:",
		"/tmp/p3/stamp.sh swift-amber-falcon",
		"type: command",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("bundle YAML must declare a session_start command hook whose command carries the harp on argv; %q missing from:\n%s", want, y)
		}
	}
	// session_start and not session_end, deliberately: codex declares
	// session_end unsupported (unsupportedHookKinds / NoSessionEndReason), so a
	// probe planted there could never run on all three engines.
	if strings.Contains(y, "session_end") {
		t.Error("P3 plants on session_start — the one kind all three hook-carrying engines support")
	}
}

// A stage-(a)-only prompt must not smuggle in an output contract: the assertion
// is a file, and asking for JSON would import P0's format flakiness into a
// probe that is immune to it.
func TestHookProbePrompt_StageAAsksForNothingParseable(t *testing.T) {
	plain := hookProbePrompt(false)
	if strings.Contains(plain, "JSON") || strings.Contains(plain, "{") {
		t.Errorf("a stage-(a)-only cell reads a FILE; its prompt must not impose an output contract that could red the cell for prose habits, got: %q", plain)
	}
	echo := hookProbePrompt(true)
	for _, want := range []string{"hook", "JSON", "no markdown code fences"} {
		if !strings.Contains(echo, want) {
			t.Errorf("a stage-(b) prompt must ask for the strict one-key object; %q missing from: %q", want, echo)
		}
	}
	if strings.Contains(echo, "swift") || strings.Contains(echo, "-") && strings.Contains(echo, "phrase is ") {
		t.Errorf("the prompt must never contain the harp itself — that would plant the value in a second channel and false-green the probe: %q", echo)
	}
}

// --- the carriage evidence scan --------------------------------------------------

const carriageNeedle = "/tmp/p3/stamp-claude-code.sh"

func writeCarriageFile(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHookProbeCarriageScan_FindsTheDeliveredHookAndSaysNothingWhenAbsent(t *testing.T) {
	root := t.TempDir()

	if got := hookProbeCarriageScan(hookProbeCarriage{Needle: carriageNeedle, Roots: []string{root}}); got != "" {
		t.Errorf("an empty tree must yield NO carriage claim (silence is honest here; the settings may ride an out-of-cwd scratch), got %q", got)
	}

	settings := writeCarriageFile(t, filepath.Join(root, ".claude", "settings.json"),
		`{"hooks":{"SessionStart":[{"hooks":[{"command":"`+carriageNeedle+` swift-amber-falcon"}]}]}}`)

	got := hookProbeCarriageScan(hookProbeCarriage{Needle: carriageNeedle, Roots: []string{root}})
	if !strings.Contains(got, settings) {
		t.Errorf("the scan must name the file that carried the hook command — that is what separates a carriage red from a firing red, got %q", got)
	}
}

// THE FLAW A LIVE RUN EXPOSED, now pinned. The fixture DECLARES the hook in a
// bundle YAML it writes itself, so the command string is in the project tree
// whether or not any settings writer ever ran. Counting that file made the scan
// report "hook command found in .ctxloom/content/bundles/bundle-hookprobe.yaml"
// on a run where delivery could not be confirmed at all — a diagnostic pointing
// confidently at the wrong subsystem, which is worse than one that says nothing.
func TestHookProbeCarriageScan_ExcludesTheFixturesOwnDeclaration(t *testing.T) {
	root := t.TempDir()
	bundle := writeCarriageFile(t, filepath.Join(root, ".ctxloom", "content", "bundles", "bundle-hookprobe.yaml"),
		"hooks:\n  session_start:\n    - command: \""+carriageNeedle+" swift-amber-falcon\"\n")

	got := hookProbeCarriageScan(hookProbeCarriage{
		Needle:   carriageNeedle,
		Roots:    []string{root},
		Authored: []string{bundle},
	})
	if got != "" {
		t.Errorf("the fixture's OWN hook declaration is not evidence that ctxloom delivered anything; the scan must ignore it, got %q", got)
	}

	// Without the exclusion the same tree reports carriage — which is exactly
	// the false diagnostic this guard exists to prevent, kept executable so
	// nobody "simplifies" Authored away.
	if unguarded := hookProbeCarriageScan(hookProbeCarriage{Needle: carriageNeedle, Roots: []string{root}}); unguarded == "" {
		t.Error("this test is inert: the unguarded scan must still find the fixture file, or it is not demonstrating what Authored prevents")
	}
}

// The session root holds every session ever recorded. NotBefore is what keeps
// the scan from walking all of them and from attributing another run's
// delivered settings to this cell.
func TestHookProbeCarriageScan_NotBeforeExcludesOlderRuns(t *testing.T) {
	root := t.TempDir()
	old := writeCarriageFile(t, filepath.Join(root, "old-session", "settings.json"),
		`{"command":"`+carriageNeedle+`"}`)
	stale := time.Now().Add(-2 * time.Hour)
	for _, p := range []string{old, filepath.Dir(old)} {
		if err := os.Chtimes(p, stale, stale); err != nil {
			t.Fatal(err)
		}
	}

	cutoff := time.Now().Add(-time.Minute)
	if got := hookProbeCarriageScan(hookProbeCarriage{
		Needle: carriageNeedle, Roots: []string{root}, NotBefore: cutoff,
	}); got != "" {
		t.Errorf("a session untouched since before this run cannot hold this run's delivery, got %q", got)
	}

	fresh := writeCarriageFile(t, filepath.Join(root, "this-session", "settings.json"),
		`{"command":"`+carriageNeedle+`"}`)
	got := hookProbeCarriageScan(hookProbeCarriage{
		Needle: carriageNeedle, Roots: []string{root}, NotBefore: cutoff,
	})
	if !strings.Contains(got, fresh) {
		t.Errorf("this run's own delivered settings must still be found, got %q", got)
	}
}

// The watcher exists because a POST-RUN scan cannot see the answer: ctxloom
// scrubs delivered settings at teardown. This test models that exactly — the
// file appears after the watch starts and is emptied before it stops — and
// requires the evidence to survive.
func TestHookProbeCarriageWatcher_SeesADeliveryThatIsScrubbedBeforeTheRunEnds(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, "session", "ephemeral", "settings.json")

	w := hookProbeWatchCarriage(hookProbeCarriage{Needle: carriageNeedle, Roots: []string{root}})

	writeCarriageFile(t, settings, `{"command":"`+carriageNeedle+`"}`)
	// Give the poller a chance to observe it, then scrub exactly as teardown
	// does. A generous wait: the assertion is about the watcher seeing a
	// transient file, and a tight sleep would make this test flaky on a loaded
	// box rather than making it stricter.
	time.Sleep(6 * hookProbeCarriagePollInterval)
	writeCarriageFile(t, settings, "{}")

	got := w.Stop()
	if !strings.Contains(got, settings) {
		t.Errorf("the watcher must retain a delivery that existed only DURING the run — a post-run scan reports the scrubbed `{}` and would call a working carriage a failure, got %q", got)
	}
	// And the post-run scan alone would indeed miss it, which is what makes the
	// watcher load-bearing rather than decorative.
	if after := hookProbeCarriageScan(hookProbeCarriage{Needle: carriageNeedle, Roots: []string{root}}); after != "" {
		t.Errorf("this test is inert: after the scrub a plain scan must find nothing, or it is not demonstrating what the watcher buys, got %q", after)
	}
}

// An empty needle would match every file and report carriage everywhere, which
// would send a reader to the wrong subsystem with total confidence.
func TestHookProbeCarriageScan_RefusesAnEmptyNeedle(t *testing.T) {
	got := hookProbeCarriageScan(hookProbeCarriage{Roots: []string{t.TempDir()}})
	if !strings.Contains(got, "skipped") {
		t.Errorf("an empty needle must be refused, not matched against everything, got %q", got)
	}
}

// --- the ledger keys ---------------------------------------------------------------

// The two stages mint under two keys. One key holding two harps would make the
// ledger — and the foreign-harp scanner that reads it — describe a cell that
// does not exist.
func TestHookProbeState_StagesMintUnderDistinctLedgerKeys(t *testing.T) {
	h := &hookProbeState{engine: "codex", runtime: "host", workspace: "none"}
	if h.cell() == h.echoCell() {
		t.Fatal("stage (a) and stage (b) must mint under DISTINCT cell keys, or the ledger records one harp where two were planted")
	}
	if h.cell().Probe != probeP3 || h.echoCell().Probe != probeP3 {
		t.Errorf("both keys must name P3 so a sweep and the leak scanner address the same probe, got %s / %s", h.cell(), h.echoCell())
	}
	if h.echoCell().Variant != "echo" {
		t.Errorf("the stage-(b) key must carry a variant naming the channel, got %q", h.echoCell().Variant)
	}
}

// The registry row and the code must agree about which feature runs P3, and the
// row must be wired rather than still claiming to be planned.
func TestHookProbeRegistryRow_IsWiredToThisFeature(t *testing.T) {
	p, ok := probeSpecByName(probeP3)
	if !ok {
		t.Fatalf("the registry has no %q row", probeP3)
	}
	if p.Feature != "capability_hook_firing.feature" {
		t.Errorf("P3 must name its feature file, got %q", p.Feature)
	}
	runnable := map[string]bool{}
	for _, c := range p.Cells {
		switch c.Status {
		case probeWired, probeLiveVerified:
			runnable[c.Engine] = true
		case probePlanned:
			t.Errorf("%s is still PLANNED although the feature exists — a planned row beside a runnable scenario is the registry lying about what is built", c.ID(p.Name))
		}
	}
	for _, e := range []string{"claude-code", "codex", "kiro"} {
		if !runnable[e] {
			t.Errorf("P3 must declare a runnable %s cell: it is one of the three engines that carry hooks at all", e)
		}
	}
	if runnable["opencode"] {
		t.Error("opencode must stay gated-out (noHooksReason) — a runnable row for an engine with no hook mechanism would skip forever and read as coverage")
	}
}
