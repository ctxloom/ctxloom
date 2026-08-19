// Package acceptance: the capability-probe nonce mint and shared verdict
// vocabulary.
//
// UNTAGGED ON PURPOSE, exactly like live_engine_registry.go. Every capability
// probe's verdict is the one part of a @live cell a hermetic test can reach —
// the cells themselves need real engines, real credentials and paid turns — so
// the verdict functions and the nonce mint live where plain `go test ./...`
// (`just test`) compiles and runs them. A trust anchor that only the live lane
// can execute is a trust anchor nobody checks.
//
// TWO THINGS LIVE HERE.
//
//  1. THE MINT. Human ruling: a probe's nonce is a FRESHLY MINTED
//     HARP per probe per cell — harp.GenerateName(), the "swift-amber-falcon"
//     form — and never the session's own harp. The distinction is the whole
//     honesty argument. The session's harp is AMBIENT: it is in
//     CTXLOOM_SESSION_HARP, in the session directory path, in banners and
//     spool files, so an engine that echoed it back would have proved nothing
//     about the channel the fixture claims to be testing. A fresh mint exists
//     only where the fixture put it, which makes "the engine said it" and "the
//     channel delivered it" the same statement. A harp rather than a hex blob
//     because the value has a second job: it is a CROSS-CELL LEAK TRACER. Cell
//     A's harp turning up in cell B's stdout, delivered files or transcript is
//     an isolation leak, and a pronounceable three-word phrase is greppable by
//     a human reading a spool by eye in a way that 16 hex characters is not.
//
//     probeHarps is the process-wide ledger of what was minted for which cell.
//     It exists for three reasons and each is load-bearing: UNIQUENESS (two
//     cells sharing a harp would make the leak scanner below report a leak that
//     is really a collision), the CELL→HARP MAP the leak scanner needs, and
//     the EVIDENCE the sidecar records so a red cell's payload keys back to its
//     cell. The ambient session harp is seeded into the used-set at
//     construction, so the mint can never hand back the one value that would
//     be false-green by construction.
//
//  2. THE VERDICT VOCABULARY. matrixAssert (steps_engine_isolation_matrix.go)
//     established the pattern the ladder's other probes must follow: a named,
//     hermetically unit-tested function that distinguishes failure SHAPES
//     rather than reporting a bare mismatch, because on this suite the shape IS
//     the finding — a format-red and a delivery-red are bugs in different
//     subsystems and must never be blurred. The checks below are that
//     vocabulary extracted, so P2–P7's verdicts COMPOSE it instead of
//     re-deriving it (and drifting apart in the failure taxonomy, which is the
//     one thing that would make the matrix unreadable as a matrix).
//
//     probeVerdict carries the three things every check needs: which probe
//     family is speaking, which cell it is about, and WHERE THE NONCE WAS
//     PLANTED. That last one is a required field rather than a convenience:
//     the design rule is that every probe states its planting channel, and a
//     channel the code has to name in its own failure message cannot be left
//     vague in the way a comment can.
package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/harp"
)

// --- cell identity ----------------------------------------------------------

// probeCellID names one addressable cell of the capability matrix: probe ×
// engine × runtime × workspace, plus an optional intra-cell variant for the
// probes whose cells differ by something other than the two isolation axes
// (P1's pinned context approach, P4's plan-vs-bypass control run).
//
// It is both the ledger key and the failure message's cell stamp, deliberately
// the same type in both roles: a leak scan that looked up `self` under a
// different key shape than the mint recorded would silently scan for a cell's
// own harp and report nothing, which is precisely the vacuous green this suite
// exists to refuse.
type probeCellID struct {
	Probe     string // registry probe name ("p0", "p2", ...); the @probe-<name> tag suffix
	Engine    string // backend type as written in the Examples table ("claude-code")
	Runtime   string // host | container | container-rootless | container-rootful (P0 is on the ownership split; other probes are not yet)
	Workspace string // none | worktree
	Variant   string // optional intra-cell discriminator ("system-prompt", "control")
}

// String renders the cell the way every failure message stamps it. Probe and
// Variant are omitted when empty so a probe that has only one dimension does
// not stamp empty fields into its own evidence.
func (c probeCellID) String() string {
	parts := make([]string, 0, 5)
	if c.Probe != "" {
		parts = append(parts, "probe="+c.Probe)
	}
	parts = append(parts,
		"engine="+c.Engine,
		"runtime="+c.Runtime,
		"workspace="+c.Workspace,
	)
	if c.Variant != "" {
		parts = append(parts, "variant="+c.Variant)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// --- the mint ---------------------------------------------------------------

// probeHarpLedger records every nonce this process minted, keyed by cell, and
// guarantees no two cells get the same one.
type probeHarpLedger struct {
	mu     sync.Mutex
	byCell map[probeCellID]string
	used   map[string]struct{}
}

// newProbeHarpLedger seeds the used-set with the ambient session harp so the
// mint can never return it. That value is readable by anything running inside
// the suite — CTXLOOM_SESSION_HARP is in the environment — so a cell whose
// "secret" was the session harp would be satisfiable through a channel the
// probe is not testing. Excluding it costs one map entry and removes an entire
// class of false green.
func newProbeHarpLedger() *probeHarpLedger {
	l := &probeHarpLedger{
		byCell: map[probeCellID]string{},
		used:   map[string]struct{}{},
	}
	if ambient := os.Getenv("CTXLOOM_SESSION_HARP"); ambient != "" {
		l.used[ambient] = struct{}{}
	}
	return l
}

// probeHarps is the process-wide mint. One ledger per suite run, so the
// cell→harp map a leak scan reads covers every cell that ran in this process.
var probeHarps = newProbeHarpLedger()

// Mint returns cell's nonce, generating one on first ask and returning the same
// value on every later ask for that cell (a cell's fixture and its assertion
// must agree on the value, and they are separate steps).
//
// Collision avoidance is harp.UniqueFrom against the ledger's used-set, not a
// bare GenerateName: two cells holding the same harp would make the leak
// scanner call a name collision an isolation leak, and an isolation leak is
// the most expensive kind of false alarm this suite can raise.
func (l *probeHarpLedger) Mint(cell probeCellID) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, ok := l.byCell[cell]; ok {
		return existing, nil
	}
	name, err := harp.UniqueFrom(l.used, harp.GenerateName)
	if err != nil {
		return "", fmt.Errorf("capability probe: minting a nonce for cell %s: %w", cell, err)
	}
	l.used[name] = struct{}{}
	l.byCell[cell] = name
	return name, nil
}

// Snapshot returns a copy of the cell→harp map recorded so far — the input a
// leak scan needs, and the evidence a sweep report renders.
func (l *probeHarpLedger) Snapshot() map[probeCellID]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[probeCellID]string, len(l.byCell))
	for k, v := range l.byCell {
		out[k] = v
	}
	return out
}

// --- the planting channel ---------------------------------------------------

// probeShape is the failure taxonomy: which KIND of thing went wrong, carried
// on the error so a caller (a sweep report, a red-map diff) can branch on it
// without matching prose. The string values are the labels the messages use,
// so a grep for the label and a switch on the constant find the same cells.
type probeShape string

const (
	// shapeRunFailed: the run itself did not complete — an engine or
	// isolation failure, before any question of what was said.
	shapeRunFailed probeShape = "RUN failure"
	// shapeSilentNoOp: exit 0 and ZERO output. This project's characteristic
	// bug, named separately because it is success-shaped and would otherwise
	// read as an ordinary mismatch.
	shapeSilentNoOp probeShape = "SILENT NO-OP"
	// shapeOutputFormat: output exists but is not the requested form (fences,
	// preamble, terminal decoration).
	shapeOutputFormat probeShape = "OUTPUT-FORMAT failure"
	// shapeDelivery: well-formed output carrying nothing of the nonce — the
	// planted channel did not deliver. The message's label comes from the
	// channel itself (see probeChannel.Shape), because "which channel failed"
	// is the whole diagnostic.
	shapeDelivery probeShape = "DELIVERY failure"
	// shapeShape: right form, wrong structure (extra keys, missing key).
	shapeShape probeShape = "SHAPE failure"
	// shapeValue: right structure, wrong value.
	shapeValue probeShape = "VALUE failure"
	// shapeLeak: another cell's minted harp appeared here. An isolation
	// failure, and the only shape that is about a cell OTHER than its own.
	shapeLeak probeShape = "ISOLATION-LEAK failure"
)

// probeChannel names where a probe planted its nonce, and what to call a
// failure to deliver it. Every probe declares one in the registry
// (capability_probe_registry.go), and every verdict carries it: a probe that
// cannot say which channel it is testing is not testing a channel.
type probeChannel struct {
	// Shape is the failure label used when the nonce never arrives — per
	// channel, so a missing fragment (CONTEXT-DELIVERY) and a missing MCP
	// tool result (MCP-DELIVERY) are never the same red.
	Shape probeShape
	// Where is the planting site in prose, spliced into the failure message
	// after "planted in": "the agent's own composed context".
	Where string
}

// --- the verdict ------------------------------------------------------------

// probeRun is one cell's raw outcome. stdout and stderr are kept SEPARATE
// because ctxloom's own human-readable diagnostics go to stderr by contract, so
// a combined capture would red every cell for reasons that have nothing to do
// with the engine.
type probeRun struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// probeFailure is every probe's failure, carrying its shape as data.
type probeFailure struct {
	Family   string
	Cell     probeCellID
	Shape    probeShape
	Detail   string
	Evidence string
}

func (f *probeFailure) Error() string {
	return fmt.Sprintf("%s %s: %s%s", f.Family, f.Cell, f.Detail, f.Evidence)
}

// probeShapeOf reports err's failure shape, and whether err was a probe verdict
// at all. A sweep's red-map diff branches on this rather than on message text.
func probeShapeOf(err error) (probeShape, bool) {
	var f *probeFailure
	if ok := asProbeFailure(err, &f); ok {
		return f.Shape, true
	}
	return "", false
}

// asProbeFailure is errors.As specialised, kept local so this file has no
// dependency beyond the standard library and harp.
func asProbeFailure(err error, target **probeFailure) bool {
	for err != nil {
		if f, ok := err.(*probeFailure); ok {
			*target = f
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// probeVerdict is the context every check shares: who is speaking, about which
// cell, and where the nonce was planted.
type probeVerdict struct {
	Family  string // message prefix ("engine-matrix")
	Cell    probeCellID
	Channel probeChannel
}

// fail builds this probe's failure. The single formatter, so every probe's
// reds are shaped the same way for a human scanning a matrix of them.
func (v probeVerdict) fail(shape probeShape, detail, evidence string) error {
	return &probeFailure{Family: v.Family, Cell: v.Cell, Shape: shape, Detail: detail, Evidence: evidence}
}

// ran is the first check every probe runs: did the run complete, and did it
// produce anything at all. Returns the whitespace-trimmed stdout on success —
// whitespace ONLY, nothing else stripped, because everything else an engine
// might add is a finding rather than noise.
func (v probeVerdict) ran(r probeRun) (string, error) {
	if r.Err != nil {
		return "", v.fail(shapeRunFailed,
			fmt.Sprintf("the run itself failed (exit %d): %v", r.ExitCode, r.Err),
			fmt.Sprintf("\nstdout:\n%s\nstderr:\n%s", r.Stdout, r.Stderr))
	}
	trimmed := strings.TrimSpace(r.Stdout)
	if trimmed == "" {
		return "", v.fail(shapeSilentNoOp,
			"the run exited 0 and produced NO stdout at all — a silent no-op, not an output-format problem",
			fmt.Sprintf("\nstderr:\n%s", r.Stderr))
	}
	return trimmed, nil
}

// jsonObject requires the whole trimmed output to parse as a JSON object.
// A non-object (array, scalar) is a FORMAT failure, not a panic.
func (v probeVerdict) jsonObject(trimmed string) (map[string]any, error) {
	var got map[string]any
	if err := json.Unmarshal([]byte(trimmed), &got); err != nil {
		return nil, v.fail(shapeOutputFormat,
			fmt.Sprintf("%s — stdout is not a bare JSON object (%v). The prompt forbids preamble, postamble and code fences; this is a finding about the engine, not a reason to loosen the assertion.", shapeOutputFormat, err),
			fmt.Sprintf("\nstdout (verbatim):\n%s", trimmed))
	}
	return got, nil
}

// carriesNonce is the channel-delivery check: the minted harp must appear
// somewhere in the output, or the channel did not deliver and nothing
// downstream of it means anything.
//
// An EMPTY nonce is refused as a caller bug rather than passing vacuously:
// strings.Contains(anything, "") is true, so a probe that forgot to mint would
// otherwise sail through the one check that proves its channel works.
func (v probeVerdict) carriesNonce(trimmed, nonce string) error {
	if nonce == "" {
		return v.fail(shapeDelivery,
			"the probe has no minted nonce to look for — every value contains the empty string, so this check would pass without proving anything",
			"")
	}
	if !strings.Contains(trimmed, nonce) {
		return v.fail(v.Channel.Shape,
			fmt.Sprintf("%s — stdout is well-formed JSON but carries nothing of the nonce %q that was planted in %s, so the engine answered without ever seeing it.",
				v.Channel.Shape, nonce, v.Channel.Where),
			fmt.Sprintf("\nstdout:\n%s", trimmed))
	}
	return nil
}

// exactObject requires got to be exactly {key: want} — one key, that key,
// that value. Extra keys and a missing key are SHAPE failures; a present key
// holding the wrong thing is a VALUE failure. Two shapes because they have two
// causes: a chatty engine versus one that answered the wrong question.
func (v probeVerdict) exactObject(got map[string]any, trimmed, key, want string) error {
	evidence := fmt.Sprintf("\nstdout:\n%s", trimmed)
	if len(got) != 1 {
		return v.fail(shapeShape,
			fmt.Sprintf("%s — expected exactly one key %q, got %d key(s).", shapeShape, key, len(got)),
			evidence)
	}
	gotVal, ok := got[key]
	if !ok {
		return v.fail(shapeShape,
			fmt.Sprintf("%s — the object has no %q key.", shapeShape, key),
			evidence)
	}
	if gotVal != any(want) {
		return v.fail(shapeValue,
			fmt.Sprintf("%s — %q is %#v, want %q.", shapeValue, key, gotVal, want),
			evidence)
	}
	return nil
}

// --- PX: the foreign-harp scanner -------------------------------------------

// probeArtifact is one thing a leak scan reads: a name for the failure message
// and the bytes actually scanned. Named rather than bare bytes because "a harp
// leaked" is unactionable without "into what".
type probeArtifact struct {
	Name string
	Body string
}

// probeFileArtifact reads path into an artifact. A MISSING FILE IS AN ERROR,
// never an empty artifact: a scan over a file that was not there would report
// no leak for the most literal reason possible, and this suite's whole posture
// is that a well-formed report of nothing must never pass for a result.
func probeFileArtifact(name, path string) (probeArtifact, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return probeArtifact{}, fmt.Errorf("capability probe: cannot scan %s for foreign harps: %w — a scan that skips an unreadable artifact reports no leak for the wrong reason", name, err)
	}
	return probeArtifact{Name: name, Body: string(b)}, nil
}

// assertNoForeignHarps is PX, the ruling's free probe: in a cell that claims
// isolation, NO OTHER cell's minted harp may appear in this cell's output,
// delivered surface files, or canonical transcript. Because every planted nonce
// is a distinctive minted harp and the ledger holds the whole run's map,
// cross-cell contamination is one grep — no new mechanism, no paid turn.
//
// Three refusals, all of them because the alternative is a scan that passes
// without looking:
//
//   - ZERO artifacts is an error. A scan with nothing to scan finds nothing.
//   - A cell absent from the ledger is an error. It means the mint was never
//     wired for this cell, so `self` would not be excluded and the cell's OWN
//     harp would read as foreign — or, worse, the ledger is empty and the scan
//     is inert.
//   - A ledger holding only `self` finds no leak because there is nothing to
//     find, which is the normal single-cell case (`just capability-probe` runs
//     one cell) and NOT a failure — but it says so out loud, in the skip-with-a
//     -reason idiom, so nobody reads an inert scan as evidence of isolation.
func assertNoForeignHarps(self probeCellID, ledger map[probeCellID]string, artifacts ...probeArtifact) error {
	if len(artifacts) == 0 {
		return fmt.Errorf("capability probe %s: foreign-harp scan was given no artifacts to scan — a scan with nothing to read cannot find a leak", self)
	}
	if _, ok := ledger[self]; !ok {
		return fmt.Errorf("capability probe %s: this cell is not in the minted-harp ledger (%d cell(s) recorded) — its own nonce was never minted through the ledger, so a scan here would be measuring the wrong thing", self, len(ledger))
	}
	foreign := make(map[string]probeCellID, len(ledger))
	for cell, h := range ledger {
		if cell == self {
			continue
		}
		foreign[h] = cell
	}
	if len(foreign) == 0 {
		fmt.Printf("NOTE foreign-harp scan %s: only this cell has minted a harp in this run, so there is nothing foreign to look for — the scan is inert, not evidence of isolation\n", self)
		return nil
	}
	for _, a := range artifacts {
		for h, owner := range foreign {
			if strings.Contains(a.Body, h) {
				return &probeFailure{
					Family: "capability probe",
					Cell:   self,
					Shape:  shapeLeak,
					Detail: fmt.Sprintf("%s — the harp %q, minted for cell %s and planted nowhere near this one, appears in this cell's %s. That is cross-cell contamination: this cell's isolation claim is false.",
						shapeLeak, h, owner, a.Name),
					Evidence: fmt.Sprintf("\n%s:\n%s", a.Name, a.Body),
				}
			}
		}
	}
	return nil
}

// assertNoAmbientSessionHarp is PX's belt-and-braces half: the suite's OWN
// session harp (CTXLOOM_SESSION_HARP, scrubbed by testenv) must not appear in a
// child's composed context or output. If it ever does, every probe's
// channel-honesty argument weakens at once — an engine could satisfy a nonce
// check from a channel no probe declared.
//
// Silent when the var is unset: with no ambient harp there is no claim to
// check, and inventing one would make this assert its own fixture.
func assertNoAmbientSessionHarp(self probeCellID, artifacts ...probeArtifact) error {
	ambient := os.Getenv("CTXLOOM_SESSION_HARP")
	if ambient == "" {
		return nil
	}
	if len(artifacts) == 0 {
		return fmt.Errorf("capability probe %s: ambient-harp scan was given no artifacts to scan", self)
	}
	for _, a := range artifacts {
		if strings.Contains(a.Body, ambient) {
			return &probeFailure{
				Family: "capability probe",
				Cell:   self,
				Shape:  shapeLeak,
				Detail: fmt.Sprintf("%s — the suite's OWN session harp %q appears in this cell's %s. Every probe's nonce argument rests on the planted value being fixture-private; an ambient harp reaching a child means a nonce check here could be satisfied through a channel no probe declared.",
					shapeLeak, ambient, a.Name),
				Evidence: fmt.Sprintf("\n%s:\n%s", a.Name, a.Body),
			}
		}
	}
	return nil
}
