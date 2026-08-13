// Package acceptance: P6 — the steer echo. The verdict half.
//
// UNTAGGED, like probe_assert.go and live_engine_registry.go beside it, so
// plain `go test ./...` compiles and runs every check below without a built
// binary, a real engine or a paid turn. P6's cells need all three; its
// JUDGEMENT needs none of them, and a trust anchor only the live lane can
// execute is a trust anchor nobody checks.
//
// WHAT P6 MEASURES, AND WHY IT NEEDED ITS OWN RUNG. j002300 already proves that
// a delegated child can get a word back to its coordinator on every engine
// ctxloom 0.7 drives: its per-engine floor asserts a marker that exists only in
// the child's own composed context arrives in the coordinator's mailbox. That
// is the CHILD→PARENT direction. The other direction — the coordinator reaching
// INTO a live session mid-flight and the child acting on what it was handed —
// was proven for claude-code alone, by the J002300-LIVE-ECHO-TOKEN step of the
// cross-engine scenario. codex, kiro and opencode had it claimed and unproven
// (capability inventory row 13).
//
// THE CHANNEL IS THE BUS MESSAGE BODY AND NOTHING ELSE (channelBusMessage). The
// value the child must produce is a harp minted per cell at fixture time and
// handed to the child in ONE place: the body of an agent_send the coordinator
// makes after the child is already running. It is not in the child's composed
// context, not in its spawn prompt, not in its environment. So a child that
// echoes it has necessarily received a mid-session message — which is the
// entire claim — and no amount of prompt compliance or context delivery can
// fake it. That separation is why the cell ALSO plants a context marker: the
// marker (a static per-row string, deliberately NOT minted through the ledger,
// see p6ContextMarkerIsNotLedgered) proves the child launched and the
// child→parent half works before a single steer is spent, so a red on the
// steer cannot be confused with a child that never woke up.
//
// THE SPOOL EVIDENCE. Under the mail-plane cutover (config
// delegation.spool_delivery, coord/spooldelivery.go) the coordinator's steer is
// no longer a queue fact — THE FILE IS THE MESSAGE, written into the child's
// own ~/.ctxloom/sessions/<harp>/persist/spool/in and renamed into in/consumed
// when the child's runner accepts it. p6AssertSpoolEvidence asserts that
// substrate on PAYLOAD BYTES: a file, on disk, carrying the minted harp. It
// exists because "the echo came back" is compatible with the spool having done
// nothing at all (the mailbox would have carried it either way), and because
// the characteristic failure of every writer in this project is exit 0 with
// zero bytes — which a delivery assertion made on outcomes alone cannot see.
package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// p6SteerAgent is the ONE delegated child every P6 row configures. A fixed name
// (rather than "<engine>-child") for the same reason j002300PerEngineAgent is
// fixed: the outline reuses the harp-remembering and mailbox steps verbatim,
// which address a child by agent name, so a single name keeps the Gherkin
// identical across rows and leaves the ENGINE as the only thing that varies.
const p6SteerAgent = "delegate"

// p6Verdict is P6's judging context: the family stamp every failure carries,
// the cell it is about, and the planting channel — channelBusMessage, the same
// value capability_probe_registry.go declares for probeP6, referenced rather
// than restated so the registry and the failure message cannot disagree about
// what this probe was testing.
func p6Verdict(cell probeCellID) probeVerdict {
	return probeVerdict{Family: "p6-steer-echo", Cell: cell, Channel: channelBusMessage}
}

// p6Cell is a P6 row's ledger key and failure stamp.
func p6Cell(engine, runtime, workspace string) probeCellID {
	return probeCellID{Probe: probeP6, Engine: engine, Runtime: runtime, Workspace: workspace}
}

// p6SteerBody is the message the coordinator sends mid-session. The harp is
// stated ONCE and framed as data to be returned verbatim; the instruction
// around it is deliberately explicit and redundant, because a vaguely worded
// steer would let a red be blamed on the wording rather than on the channel.
//
// It refuses an empty harp rather than composing a body with a hole in it: the
// assertion downstream is strings.Contains(body, harp), and every string
// contains the empty one, so a steer built from an unminted harp would produce
// a cell that passes without the bus ever delivering anything.
func p6SteerBody(harp string) (string, error) {
	if strings.TrimSpace(harp) == "" {
		return "", fmt.Errorf("p6: refusing to compose a steer with an EMPTY echo phrase — every reply contains the empty string, so the cell would pass without the message ever arriving")
	}
	return "Call the MCP tool agent_send with to=\"parent\" and body set to EXACTLY this phrase, " +
		"verbatim and in full, nothing else: " + harp +
		". Do this now, then stop. Do not paraphrase it, do not translate it, do not add commentary around it.", nil
}

// p6RefuseEmptyMarker guards a P6 row's WAKE-UP marker, and its doc is where
// the second half of the nonce story lives: why that marker is a static
// Examples-table string and not a second minted harp.
//
// probeHarps is the ledger PX scans against: every harp in it that is not the
// cell under test is FOREIGN, and a foreign harp found in a cell's output is
// reported as cross-cell contamination. A wake-up marker minted under, say,
// probeCellID{Probe: probeP6, Variant: "context-marker"} would therefore be a
// different ledger cell from the steer it accompanies — so the child echoing
// its own context marker, which is precisely what the wake-up step asks for,
// would read as an isolation leak. The marker is fixture-private either way (the
// gate step writes it into the child's own bundle and it exists nowhere else),
// which is all the wake-up assertion needs; only the STEER's value has to be
// ledgered, because only the steer is P6's channel.
//
// The emptiness guard itself is j002300's own, for its own reason: the wake-up
// assertion is strings.Contains(body, marker), so a blank Examples cell would
// turn it into a tautology satisfied by a runner-exit report.
func p6RefuseEmptyMarker(engine, marker string) error {
	if strings.TrimSpace(marker) == "" {
		return fmt.Errorf("p6: the row for %q carries an EMPTY wake-up marker — every body contains the empty string, so the child would be declared awake without ever having run", engine)
	}
	return nil
}

// --- the echo verdict --------------------------------------------------------

// p6CredentialFailureMarkers are the substrings that identify a body as an
// ENGINE AUTH failure rather than an answer. Measured, not guessed: every one
// of them appeared in a runner-exit report this suite has actually seen (see
// j002300's codex row — `codex login status` is a local read of auth.json and
// cannot tell AUTHENTICATED from STILL-VALID, so a consumed refresh token
// arrives as a red row rather than a named skip).
//
// They are used ONLY to classify a body that has already failed the harp check,
// never to pass one: a child that genuinely echoed the harp is green even if it
// also said the word "401".
var p6CredentialFailureMarkers = []string{
	"refresh_token_reused",
	"could not be refreshed",
	"401",
	"Unauthorized",
	"not logged in",
	"login status",
}

// p6LooksLikeCredentialFailure reports whether body reads as an engine
// authentication failure, and which marker said so.
func p6LooksLikeCredentialFailure(body string) (string, bool) {
	for _, m := range p6CredentialFailureMarkers {
		if strings.Contains(body, m) {
			return m, true
		}
	}
	return "", false
}

// p6AssertEcho is P6's verdict: among every message body the coordinator's
// mailbox received from this child, at least one must carry the minted harp
// VERBATIM.
//
// The failure taxonomy is the point, because on this suite the shape IS the
// finding:
//
//   - no bodies at all → SILENT NO-OP. The child was steered and the bus
//     returned nothing whatsoever. This project's characteristic failure, named
//     separately so it can never be read as an ordinary mismatch.
//   - bodies that read as an engine auth failure → RUN failure, with the
//     re-login precedent quoted. j002300 paid for this lesson twice: a
//     runner-exit report carrying a 401 is a credential to renew, not a
//     delegation regression.
//   - bodies present, no harp → BUS-DELIVERY failure (the channel's own shape).
//     The child ran and spoke, and what the coordinator handed it never arrived.
//
// Every body seen is echoed back verbatim in the evidence, because a live red
// has to be readable as "the engine said this instead" and never as a bare
// timeout.
func p6AssertEcho(v probeVerdict, harp string, bodies []string) error {
	if strings.TrimSpace(harp) == "" {
		return v.fail(shapeDelivery,
			"the cell has no minted steer harp to look for — every body contains the empty string, so this check would pass without proving anything",
			"")
	}
	if len(bodies) == 0 {
		return v.fail(shapeSilentNoOp,
			fmt.Sprintf("the coordinator steered %q into this child's live session and its mailbox received NOTHING back — not the echo, not a turn copy, not even a runner-exit report. That is a silent no-op on the bus, not a wrong answer.", harp),
			"")
	}
	for _, b := range bodies {
		if strings.Contains(b, harp) {
			return nil
		}
	}
	evidence := fmt.Sprintf("\n%d body/bodies received from this child:\n%s", len(bodies), strings.Join(bodies, "\n---\n"))
	for _, b := range bodies {
		if marker, ok := p6LooksLikeCredentialFailure(b); ok {
			return v.fail(shapeRunFailed,
				fmt.Sprintf("the bus worked — a message really did arrive from this child — but its body reads as an ENGINE AUTH failure (matched %q), not an answer. j002300's own precedent applies: re-authenticate the engine and re-run; only a body that is neither the echo nor a credential error is evidence against ctxloom.", marker),
				evidence)
		}
	}
	return v.fail(v.Channel.Shape,
		fmt.Sprintf("%s — this child spoke to its coordinator, but nothing it sent carries the steer harp %q, which was planted in %s. The child ran; the mid-session message it was supposed to act on did not reach it (or it reached the engine and the engine did not carry it into a turn).",
			v.Channel.Shape, harp, v.Channel.Where),
		evidence)
}

// --- the spool evidence ------------------------------------------------------

// p6SpoolCensus is what one child's spool directory actually held: how many
// message files sat in each plane, and which planes carried the steer harp's
// bytes. It is returned even on failure, because "which directory had the file"
// is the whole diagnostic when a delivery substrate misbehaves.
type p6SpoolCensus struct {
	Root string
	// Files maps a spool directory ("in", "in/consumed", "out", ...) to the
	// message files found directly in it, sorted.
	Files map[string][]string
	// HarpIn / HarpOut name the directories whose file BYTES carried the harp.
	HarpIn  []string
	HarpOut []string
	// Total is every message file across every plane.
	Total int
}

// p6SpoolDirs is the closed set this census walks, mirroring
// spool.Dirs()' own order (parents before children). Named locally rather than
// imported so this file keeps probe_assert.go's dependency posture — standard
// library only — and so the acceptance suite states, in its own words, exactly
// which directories it considers evidence.
var p6SpoolDirs = []string{"in", "in/consumed", "in/withdrawn", "out", "out/consumed"}

// p6SpoolRoot is one session's spool root under an isolated home:
// <home>/.ctxloom/sessions/<harp>/persist/spool. Built by joining rather than
// through internal/paths' resolver for the reason j002300TranscriptPath already
// documents — the resolver reads the OUTER test process's ambient HOME, not the
// isolated one the spawned coordinator actually wrote under.
func p6SpoolRoot(homeDir, harp string) string {
	return filepath.Join(homeDir, ".ctxloom", "sessions", harp, "persist", "spool")
}

// p6ReadSpoolCensus walks root and reports what is there. Sub-directories are
// structure, never messages, so only regular files count. A missing root is NOT
// an empty census: it is an error, for the same reason probeFileArtifact
// refuses a missing artifact — a scan over a directory that was never created
// reports "no leak, no evidence, no problem" for the most literal reason
// possible.
func p6ReadSpoolCensus(root, harp string) (p6SpoolCensus, error) {
	c := p6SpoolCensus{Root: root, Files: map[string][]string{}}
	if _, err := os.Stat(root); err != nil {
		return c, fmt.Errorf("p6: spool root %s is not there: %w", root, err)
	}
	for _, dir := range p6SpoolDirs {
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return c, fmt.Errorf("p6: reading spool directory %s/%s: %w", root, dir, err)
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			names = append(names, e.Name())
			c.Total++
			body, rerr := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), e.Name()))
			if rerr != nil {
				return c, fmt.Errorf("p6: reading spool file %s/%s/%s: %w", root, dir, e.Name(), rerr)
			}
			if harp != "" && strings.Contains(string(body), harp) {
				if strings.HasPrefix(dir, "in") {
					c.HarpIn = append(c.HarpIn, dir+"/"+e.Name())
				} else {
					c.HarpOut = append(c.HarpOut, dir+"/"+e.Name())
				}
			}
		}
		sort.Strings(names)
		if len(names) > 0 {
			c.Files[dir] = names
		}
	}
	return c, nil
}

// String renders the census as the evidence line a human reads: every plane,
// its file count, and which files carried the harp.
func (c p6SpoolCensus) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "spool %s — %d message file(s)", c.Root, c.Total)
	for _, dir := range p6SpoolDirs {
		if names := c.Files[dir]; len(names) > 0 {
			fmt.Fprintf(&b, "\n  %-14s %d: %s", dir, len(names), strings.Join(names, ", "))
		}
	}
	fmt.Fprintf(&b, "\n  steer harp in the IN plane:  %v", c.HarpIn)
	fmt.Fprintf(&b, "\n  steer harp in the OUT plane: %v", c.HarpOut)
	return b.String()
}

// p6AssertSpoolEvidence is the soak's behavioural proof, asserted on PAYLOAD
// BYTES rather than on a flag being set or a run exiting 0.
//
// WHAT IT CLAIMS, precisely, and no more: with delegation.spool_delivery on,
// the coordinator's mid-session steer is a FILE in the child's own spool IN
// plane, and that file carries the minted harp. The in/ → in/consumed rename is
// the child runner's acknowledgement, so BOTH count as the in plane — asserting
// on in/ alone would red a cell for the child having done its job promptly.
//
// WHAT IT DELIBERATELY DOES NOT CLAIM: anything about the OUT plane. The child's
// reply reaches the coordinator through its forwarder MCP server, and whether
// that direction also lands as a file is a property of the cutover's scope, not
// of P6's claim. The census records the out plane in full either way, so the
// answer is measured and reported rather than assumed in either direction.
//
// Three shapes, because they have three causes:
//
//   - no spool root, or a root with zero files → SILENT NO-OP. The switch was
//     on, the run succeeded, and the substrate wrote nothing. Exactly the
//     exit-0-and-zero-bytes failure that only a payload assertion catches.
//   - files, but none in the in plane carrying the harp → BUS-DELIVERY failure:
//     the spool ran, and the steer is not in it.
//   - otherwise green, with the census as evidence.
func p6AssertSpoolEvidence(v probeVerdict, census p6SpoolCensus, harp string) error {
	if strings.TrimSpace(harp) == "" {
		return v.fail(shapeDelivery,
			"the cell has no minted steer harp to look for in the spool — every file contains the empty string, so this check would pass over an empty directory",
			"")
	}
	if census.Total == 0 {
		return v.fail(shapeSilentNoOp,
			"delegation.spool_delivery is on for this cell and the round trip completed, but the child's spool holds ZERO message files. The mail plane wrote nothing while every outcome looked healthy — the exit-0-with-zero-bytes failure that only a payload assertion can see.",
			"\n"+census.String())
	}
	if len(census.HarpIn) == 0 {
		return v.fail(v.Channel.Shape,
			fmt.Sprintf("%s — the spool ran (%d message file(s) on disk) but NO file in the child's IN plane carries the steer harp %q. Under the cutover the file IS the delivery, so a steer that is not on disk was not delivered by the substrate this cell claims to be exercising.",
				v.Channel.Shape, census.Total, harp),
			"\n"+census.String())
	}
	return nil
}
