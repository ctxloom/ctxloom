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

// --- RootGlobs: reaching the container overlay ------------------------------

// TestHookProbeCarriage_RootGlobsReachARootCreatedAfterTheQueryWasBuilt is the
// claim RootGlobs exists to make. A container cell's managed-config overlay
// does not exist when the watcher starts, so a scan that resolved its roots
// once would never walk it — and would still print a confident NOT SEEN.
func TestHookProbeCarriage_RootGlobsReachARootCreatedAfterTheQueryWasBuilt(t *testing.T) {
	base := t.TempDir()
	q := hookProbeCarriage{
		Needle:    carriageNeedle,
		RootGlobs: []string{filepath.Join(base, "iso-*")},
	}

	// Built BEFORE the root exists: the pattern matches nothing yet.
	if got := hookProbeCarriageScan(q); got != "" {
		t.Fatalf("scan found something before the root existed: %q", got)
	}
	if roots := q.resolveRoots(); len(roots) != 0 {
		t.Fatalf("expected no roots before creation, got %v", roots)
	}

	// The overlay appears mid-run, exactly as containerConfigOverlay's cfg0 does.
	overlay := filepath.Join(base, "iso-4093164660", "cfg0")
	if err := os.MkdirAll(overlay, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCarriageFile(t, filepath.Join(overlay, "settings.json"), carriageNeedle)

	got := hookProbeCarriageScan(q)
	if got == "" {
		t.Fatal("RootGlobs did not reach a root created after the query was built — a container cell's carriage is unobservable and every such cell reports NOT SEEN regardless of what ctxloom delivered")
	}
	if !strings.Contains(got, "settings.json") {
		t.Fatalf("hit does not name the delivered file: %q", got)
	}
}

// TestHookProbeCarriage_ResolveRootsPutsFixedRootsFirstAndDedupes pins the
// report order and the dedup. A glob that re-matches a fixed root would
// otherwise walk it twice and report the same hit twice.
func TestHookProbeCarriage_ResolveRootsPutsFixedRootsFirstAndDedupes(t *testing.T) {
	base := t.TempDir()
	fixed := filepath.Join(base, "aaa")
	if err := os.MkdirAll(fixed, 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(base, "zzz")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}

	q := hookProbeCarriage{
		Roots:     []string{fixed},
		RootGlobs: []string{filepath.Join(base, "*")}, // matches BOTH, including fixed
	}
	roots := q.resolveRoots()
	if len(roots) != 2 {
		t.Fatalf("expected the fixed root and one new match, got %v", roots)
	}
	if roots[0] != fixed {
		t.Fatalf("fixed roots must come first (report order), got %v", roots)
	}
	if roots[1] != other {
		t.Fatalf("expected the globbed root second, got %v", roots)
	}
}

// TestHookProbeCarriage_ResolveRootsSurvivesAMalformedPattern: a bad glob is a
// harness bug and must never redden a cell, because this scan is evidence and
// not a gate. The fixed roots still resolve.
func TestHookProbeCarriage_ResolveRootsSurvivesAMalformedPattern(t *testing.T) {
	q := hookProbeCarriage{
		Roots:     []string{"/fixed"},
		RootGlobs: []string{"[malformed"},
	}
	roots := q.resolveRoots()
	if len(roots) != 1 || roots[0] != "/fixed" {
		t.Fatalf("a malformed pattern must drop out and leave the fixed roots, got %v", roots)
	}
}

// TestHookProbeContainerOverlayGlobs_TargetTheScratchIsolationCreates checks the
// glob is aimed where isolation actually puts the overlay.
//
// THE ONE BINDING THIS CANNOT CHECK HERMETICALLY is the prefix itself:
// isolation.prepareContainerScratch calls os.MkdirTemp with it and the constant
// is unexported, so if it is renamed this test still passes and the live
// container cell is what catches it. That is stated rather than papered over —
// the value here is proving the glob matches a scratch-SHAPED directory in the
// base the scratch really lands in, which is the half that broke by hand.
func TestHookProbeContainerOverlayGlobs_TargetTheScratchIsolationCreates(t *testing.T) {
	globs := hookProbeContainerOverlayGlobs()
	if len(globs) == 0 {
		t.Fatal("no overlay globs at all — every container cell is blind")
	}

	// A real scratch root, named the way isolation names one.
	scratch, err := os.MkdirTemp("", hookProbeContainerOverlayScratchPrefix)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(scratch)

	var matched bool
	for _, g := range globs {
		hits, err := filepath.Glob(g)
		if err != nil {
			t.Fatalf("glob %q is malformed: %v", g, err)
		}
		for _, h := range hits {
			if h == scratch {
				matched = true
			}
		}
	}
	if !matched {
		t.Fatalf("globs %v did not match a scratch root at %s — the container overlay would never be walked", globs, scratch)
	}
}

// TestHookProbeCarriageWatcher_SearchedIsTheUnionAcrossPasses: a globbed root
// exists only while the run does, so asking after teardown must not understate
// where the watcher looked.
func TestHookProbeCarriageWatcher_SearchedIsTheUnionAcrossPasses(t *testing.T) {
	base := t.TempDir()
	fixed := filepath.Join(base, "fixed")
	if err := os.MkdirAll(fixed, 0o755); err != nil {
		t.Fatal(err)
	}

	w := hookProbeWatchCarriage(hookProbeCarriage{
		Needle:    carriageNeedle,
		Roots:     []string{fixed},
		RootGlobs: []string{filepath.Join(base, "ephemeral-*")},
	})

	ephemeral := filepath.Join(base, "ephemeral-1")
	if err := os.MkdirAll(ephemeral, 0o755); err != nil {
		t.Fatal(err)
	}
	// Let at least one pass observe it, then take it away as teardown does.
	deadline := time.Now().Add(5 * time.Second)
	var walked bool
	for time.Now().Before(deadline) && !walked {
		for _, r := range w.Searched() {
			if r == ephemeral {
				walked = true
			}
		}
		if !walked {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !walked {
		t.Fatal("the watcher never walked the ephemeral root")
	}
	if err := os.RemoveAll(ephemeral); err != nil {
		t.Fatal(err)
	}
	w.Stop()

	var sawEphemeral, sawFixed bool
	for _, r := range w.Searched() {
		switch r {
		case ephemeral:
			sawEphemeral = true
		case fixed:
			sawFixed = true
		}
	}
	if !sawFixed {
		t.Fatal("the fixed root is missing from Searched")
	}
	if !sawEphemeral {
		t.Fatal("a root that existed only mid-run dropped out of Searched — a NOT SEEN would understate where the scan looked")
	}
}

// TestHookProbeCarriageOrUnknown_NamesTheRootsItSearched: the NOT SEEN line has
// to derive its "where" from the roots actually walked. Asserting it in prose
// goes false the moment a root is added, which is exactly what happened when
// container cells arrived.
func TestHookProbeCarriageOrUnknown_NamesTheRootsItSearched(t *testing.T) {
	h := &hookProbeState{carriageRoots: []string{"/proj", "/tmp/ctxloom-iso-7/cfg0"}}
	got := hookProbeCarriageOrUnknown(h)
	if !strings.Contains(got, "/proj") || !strings.Contains(got, "/tmp/ctxloom-iso-7/cfg0") {
		t.Fatalf("the NOT SEEN line must name every root it searched, got: %q", got)
	}
}

// TestHookProbeCarriageOrUnknown_SaysSoWhenNothingWasSearched: "we looked and
// found nothing" and "we looked nowhere" are different claims, and only one of
// them is evidence.
func TestHookProbeCarriageOrUnknown_SaysSoWhenNothingWasSearched(t *testing.T) {
	got := hookProbeCarriageOrUnknown(&hookProbeState{})
	if !strings.Contains(got, "NO ROOT AT ALL") {
		t.Fatalf("an empty root set must be reported as such rather than reading as a searched absence, got: %q", got)
	}
}

// --- the in-container carriage scan ----------------------------------------

// TestHookProbeIsContainerAxis_CoversBothOwnerships: matching one ownership
// exactly would leave the other silently on the host-only path, still printing
// a confident NOT SEEN while never looking where the bytes are.
func TestHookProbeIsContainerAxis_CoversBothOwnerships(t *testing.T) {
	for _, axis := range []string{"container-rootless", "container-rootful"} {
		if !hookProbeIsContainerAxis(axis) {
			t.Errorf("%q must take the in-container vantage point", axis)
		}
	}
	if hookProbeIsContainerAxis("host") {
		t.Error("a host cell must not try to exec into a container")
	}
}

// TestHookProbeContainerScan_ReportsTheFilesTheContainerActuallyHolds is the
// positive claim: a hit inside the container is carriage evidence the host can
// never produce on this axis.
func TestHookProbeContainerScan_ReportsTheFilesTheContainerActuallyHolds(t *testing.T) {
	var gotContainer, gotNeedle, gotDirs string
	run := func(container string, env map[string]string, argv ...string) ([]byte, error) {
		gotContainer = container
		gotNeedle = env["CTXLOOM_PROBE_NEEDLE"]
		gotDirs = env["CTXLOOM_PROBE_DIRS"]
		return []byte("/home/agent/.ctxloom/sessions/h/ephemeral/.claude/settings.json\n"), nil
	}

	got := hookProbeContainerScan(run, "ctxloom-iso-abc", "p3/stamp.sh", []string{"/proj"}, nil)
	if got == "" {
		t.Fatal("a file carrying the needle inside the container must be reported as carriage")
	}
	if !strings.Contains(got, "ephemeral/.claude/settings.json") {
		t.Fatalf("the hit must name the file: %q", got)
	}
	if !strings.Contains(got, "ctxloom-iso-abc") {
		t.Fatalf("the hit must name the container it came from: %q", got)
	}
	if gotContainer != "ctxloom-iso-abc" {
		t.Errorf("scanned the wrong container: %q", gotContainer)
	}
	// The needle must travel as an ENV VAR, never spliced into the script: it
	// is a filesystem path, and a quote in it would otherwise become shell.
	if gotNeedle != "p3/stamp.sh" {
		t.Errorf("needle must ride the environment, got %q", gotNeedle)
	}
	if !strings.Contains(gotDirs, "/proj") {
		t.Errorf("the caller's dirs must reach the script, got %q", gotDirs)
	}
}

// TestHookProbeContainerScan_EmptyOutputIsNotAHit: grep printing nothing is the
// ordinary "not delivered" answer and must not be dressed up as evidence.
func TestHookProbeContainerScan_EmptyOutputIsNotAHit(t *testing.T) {
	run := func(string, map[string]string, ...string) ([]byte, error) { return []byte("\n  \n"), nil }
	if got := hookProbeContainerScan(run, "c", "needle", nil, nil); got != "" {
		t.Fatalf("whitespace-only grep output must not read as a hit: %q", got)
	}
}

// TestHookProbeContainerScan_ErrorsAreSilent: this scan is EVIDENCE, never a
// gate. A container that has gone away, or an image with no shell, must not
// turn a firing failure into a harness failure.
func TestHookProbeContainerScan_ErrorsAreSilent(t *testing.T) {
	run := func(string, map[string]string, ...string) ([]byte, error) {
		return []byte("something"), errors.New("no such container")
	}
	if got := hookProbeContainerScan(run, "c", "needle", nil, nil); got != "" {
		t.Fatalf("an exec error must yield no evidence rather than a false hit: %q", got)
	}
}

// TestHookProbeContainerScan_RefusesToScanWithoutItsInputs: an empty needle
// would make grep match every line and report the whole tree as carriage.
func TestHookProbeContainerScan_RefusesToScanWithoutItsInputs(t *testing.T) {
	run := func(string, map[string]string, ...string) ([]byte, error) {
		t.Fatal("must not exec without a container and a needle")
		return nil, nil
	}
	if got := hookProbeContainerScan(run, "", "needle", nil, nil); got != "" {
		t.Fatalf("no container: %q", got)
	}
	if got := hookProbeContainerScan(run, "c", "", nil, nil); got != "" {
		t.Fatalf("no needle: %q", got)
	}
	if got := hookProbeContainerScan(nil, "c", "needle", nil, nil); got != "" {
		t.Fatalf("no exec seam: %q", got)
	}
}

// TestHookProbeContainerCarriageScript_MatchesLiterallyAndExpandsHomeInside:
// the two properties that make this script correct rather than merely working.
func TestHookProbeContainerCarriageScript_MatchesLiterallyAndExpandsHomeInside(t *testing.T) {
	if !strings.Contains(hookProbeContainerCarriageScript, "grep -rlF") {
		t.Error("the needle is a PATH: a regex match would hit things it should not")
	}
	if !strings.Contains(hookProbeContainerCarriageScript, `"$HOME/.ctxloom"`) {
		t.Error("HOME must expand INSIDE the container — the container's home is not the host's, which is the entire reason this scan exists")
	}
	if strings.Contains(hookProbeContainerCarriageScript, "$CTXLOOM_PROBE_NEEDLE\"") &&
		!strings.Contains(hookProbeContainerCarriageScript, `"$CTXLOOM_PROBE_NEEDLE"`) {
		t.Error("the needle must be quoted where it is expanded")
	}
}

// TestHookProbeContainerScan_ExcludesTheFixturesOwnDeclarationAndGit is the
// regression this scan earned the hard way. Its FIRST live run reported
//
//	.ctxloom/content/bundles/bundle-hookprobe.yaml, .git/index
//
// as carriage — the bundle YAML the fixture itself writes to DECLARE the hook,
// and git's index carrying the same path because the fixture is committed.
// Both are present whether or not any settings writer ever ran, so reporting
// them says "ctxloom delivered the hook" while meaning nothing of the sort.
// The host scan already had Authored for exactly this; the container scan
// reintroduced the bug by not having it.
func TestHookProbeContainerScan_ExcludesTheFixturesOwnDeclarationAndGit(t *testing.T) {
	authored := "/proj/.ctxloom/content/bundles/bundle-hookprobe.yaml"
	run := func(string, map[string]string, ...string) ([]byte, error) {
		return []byte(authored + "\n"), nil
	}
	if got := hookProbeContainerScan(run, "c", "needle", []string{"/proj"}, []string{authored}); got != "" {
		t.Fatalf("the fixture's own declaration must never read as delivery evidence, got: %q", got)
	}

	// A real delivery alongside it still counts.
	run2 := func(string, map[string]string, ...string) ([]byte, error) {
		return []byte(authored + "\n/home/agent/.ctxloom/sessions/h/ephemeral/.claude/settings.json\n"), nil
	}
	got := hookProbeContainerScan(run2, "c", "needle", []string{"/proj"}, []string{authored})
	if !strings.Contains(got, "settings.json") {
		t.Fatalf("a genuine delivery must survive the exclusion: %q", got)
	}
	if strings.Contains(got, "bundle-hookprobe.yaml") {
		t.Fatalf("the authored file leaked into the evidence: %q", got)
	}

	// And git is excluded at the grep, so the index never reaches us.
	if !strings.Contains(hookProbeContainerCarriageScript, "--exclude-dir=.git") {
		t.Error("git's index carries the needle because the fixture is committed; it must be excluded at the source")
	}
}
