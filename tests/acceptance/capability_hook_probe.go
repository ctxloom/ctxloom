// Package acceptance: P3, the hook-FIRING probe's fixture material and verdict.
//
// UNTAGGED ON PURPOSE, like probe_assert.go and live_engine_registry.go beside
// it. Every P3 cell is @live — it needs a real vendor binary, a real
// subscription and a paid turn — so the only part of this probe a hermetic gate
// can execute is the fixture it writes and the verdict it reaches. Both live
// here, where plain `go test ./...` compiles and runs them. The godog steps that
// drive a cell live in steps_capability_hook_firing.go, which IS
// acceptance-tagged; the split is the point, not an accident of layout.
//
// WHAT THIS PROBE PROVES, AND WHY NOTHING ELSE DOES. The capability inventory's
// row 6 (agent.SettingsWriter / agentDescriptor.newWriter) is proven: the golden
// and settings-io tests, and tests/integration's
// TestDeliveryApproach_HookCarriageMatchesDeclaration, show that ctxloom writes
// the right hook bytes into each engine's own native hook surface. Row 7 —
// bundles.HookEvent*, the hooks actually FIRING — is proven NOWHERE, hermetic or
// live. Those two rows are one step apart and the step is a vendor binary we do
// not control: ctxloom writes .claude/settings.json / $CODEX_HOME/config.toml /
// .kiro/agents/<name>.json, and then CLAUDE, CODEX or KIRO decides whether to
// read that file and exec the command in it. Carriage is our behaviour; firing
// is theirs. A suite that only ever checks carriage is measuring the half it
// already controls.
//
// THE ASSERTION IS A FILE, NOT A SENTENCE. The hook's whole job is one line:
// append the harp it was handed on ITS OWN ARGV to a stamp file beside the
// script itself, inside the cell's workspace. Then the verdict reads that
// file's BYTES. Nothing about this can flake on an engine's prose habits, on
// markdown fences, or on a model deciding to be chatty — the three things that
// red a cell for reasons unrelated to the capability under test (the design's
// own §5.2 counter). And nothing about it can be satisfied by an engine that
// merely READ the settings file it was given: the harp is sitting in that file
// in plain text, so an engine could always quote it back, but only an engine
// that actually EXECUTED the command can make the stamp file exist. That gap —
// quotable versus executable — is the entire probe.
//
// EXIT CODES ARE NOT THE ASSERTION, AND THE EMPTY FILE IS NOT A PASS. Two
// refusals encode this project's characteristic bug (exit 0, success-shaped,
// zero bytes). An unreadable stamp file is a failure naming the path it looked
// at, never a silently-skipped check; a stamp file that EXISTS and is EMPTY is
// called out as a silent no-op by name, because "the hook ran and wrote
// nothing" and "the hook never ran" are different findings about different
// subsystems and a bare `err == nil` would blur them into one.
//
// TWO HARPS, TWO CHANNELS, DELIBERATELY NOT ONE. Stage (a) — firing — plants
// its harp in the hook command's ARGV. Stage (b) — output ingestion — plants a
// DIFFERENT harp in the hook's STDOUT, and only for an engine whose declared
// context approach IS the hook (agent.ApproachHook first in its ApproachTable:
// codex, and codex alone at this base). One harp used for both stages could be
// satisfied in stage (b) by an engine that had reached the stamp file on disk,
// and the whole ladder rests on a planted value being reachable through exactly
// one channel. Two mints cost nothing and close that door.
package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// --- what each engine's hook surface can be asked to prove --------------------

// hookProbeIngestsHookStdout reports whether ENGINE ingests a session-start
// hook's stdout as conversation context — the condition for stage (b).
//
// It is not a guess and not a preference: it mirrors production's own declared
// dispatch. codex's ApproachTable (codexApproaches, internal/codex/surfaces.go)
// lists agent.ApproachHook FIRST for agent.SurfaceContext, which makes the hook
// codex's DEFAULT context route — ctxloom really does deliver a codex agent's
// composed context by writing a SessionStart hook and letting codex ingest what
// it prints. claude declares ApproachHook too, but claude's SurfaceFor resolves
// that pair to noopContextDelivery — the documented no-op that never carries —
// so ctxloom does not deliver claude's context that way and this probe must not
// pretend it can observe it. kiro reads steering files instead and its
// ApproachTable has no hook entry at all; opencode has no hook mechanism.
//
// Getting this wrong in the permissive direction is the expensive mistake: it
// would red claude and kiro cells for failing to do something ctxloom never
// asked them to do, and a red that blames the wrong subsystem is worse than no
// cell at all.
func hookProbeIngestsHookStdout(engine string) bool {
	return engine == "codex"
}

// --- the fixture --------------------------------------------------------------

// hookProbeAgent is the one agent binding every cell configures, matching the
// matrix floor's idiom: the only things that vary across cells are the engine
// and the axes.
const hookProbeAgent = "hookprobe"

// hookProbeEchoKey is the ONE key a stage-(b) cell's stdout must parse to an
// object of. Named once so the prompt and the verdict cannot drift apart.
const hookProbeEchoKey = "hook"

// hookProbeFixtureDirName is the fixture's directory, INSIDE the cell's
// workspace. See the siting argument at the Given step that creates it: the
// probe's guarantee is hookProbeAssertStamp's demand that the file EXIST with
// the argv harp in it, which reading a path cannot satisfy, so the directory can
// live where a container cell and a per-agent worktree can both reach it.
const hookProbeFixtureDirName = "p3-hook-probe"

// hookProbeScript renders the stamp script: the simplest thing that can prove
// an engine exec'd it.
//
// Line by line, because every line is load-bearing:
//
//   - `"$1"` is the harp, handed to the script on its argv by the hook COMMAND
//     ctxloom wrote into the engine's own settings surface. Reading it from
//     argv rather than baking it into the script body is what makes the planted
//     channel "the hook's argv" rather than "a file the fixture wrote".
//   - the append is `>>`, not `>`: a second firing must ADD a line rather than
//     silently replace one, so an engine that fires the hook twice is visible
//     in the evidence instead of being flattened into a single-line pass.
//   - the stamp path is single-quoted and resolved against `dirname "$0"` — the
//     script's OWN directory — never against cwd. A hook may be exec'd from a
//     cwd nobody promised us (claude documents exactly that), so a cwd-relative
//     path would land the proof somewhere the verdict never looks. This was an
//     absolute path baked in by the fixture, which satisfied the same
//     requirement until the probe gained container and worktree axes: a
//     host-absolute path exists in NEITHER a container's filesystem namespace
//     nor a per-agent checkout. $0 satisfies it on every axis at once, because
//     the script travels with the workspace.
//   - the exit is explicit `exit 0`. A hook that exits non-zero is, on some
//     engines, a hook that BLOCKS the turn; the probe must observe firing
//     without altering the run it is observing.
//
// echoLine is emitted only for a stage-(b) cell, and it is the engine's own
// hook-output protocol (the same hookSpecificOutput/additionalContext envelope
// ctxloom's real context-injection hook emits from `ctxloom hook
// inject-context`), so the probe rides the vendor contract production already
// depends on rather than inventing a second one.
func hookProbeScript(stampPath, echoHarp string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# ctxloom capability probe P3 (hook firing) — fixture-written, single purpose.\n")
	b.WriteString("# Appends the harp it was handed on argv to a stamp file BESIDE ITSELF.\n")
	b.WriteString("# Its EXISTENCE with those bytes is the proof that the vendor binary read\n")
	b.WriteString("# the settings ctxloom wrote and exec'd the command in them.\n")
	b.WriteString("#\n")
	b.WriteString("# The stamp resolves from $0, not from cwd and not from an absolute path\n")
	b.WriteString("# baked in when the fixture was written. Both alternatives break a cell on\n")
	b.WriteString("# an axis this probe now runs: cwd is whatever the vendor binary chose to\n")
	b.WriteString("# exec the hook from, and a host-absolute path does not exist inside a\n")
	b.WriteString("# container or inside a per-agent worktree. $0 is the script's own\n")
	b.WriteString("# location, so the stamp lands next to it wherever the workspace was\n")
	b.WriteString("# mounted or checked out — and that is a host-readable path by\n")
	b.WriteString("# construction, because the workspace is what gets bind-mounted.\n")
	fmt.Fprintf(&b, "printf '%%s\\n' \"$1\" >> \"$(dirname \"$0\")\"/%s\n", shellQuoteForProbe(stampPath))
	if echoHarp != "" {
		// The engine's own SessionStart hook-output envelope. additionalContext
		// is the field the harness folds into the conversation, so a harp that
		// comes back in the turn's JSON came through the hook's STDOUT and
		// through nothing else — it is written nowhere on disk the engine reads.
		echo := fmt.Sprintf(
			`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"The ctxloom hook probe phrase for this session is %s"}}`,
			echoHarp)
		fmt.Fprintf(&b, "printf '%%s\\n' %s\n", shellQuoteForProbe(echo))
	}
	b.WriteString("exit 0\n")
	return b.String()
}

// shellQuoteForProbe single-quotes s for interpolation into a /bin/sh command
// string, escaping embedded single quotes the standard way. Single quotes
// neutralize spaces, $, backticks and backslashes, so a temp path or a JSON
// body containing any of those cannot break the command split.
//
// It mirrors agent.shellSingleQuote, which is unexported in its own package;
// this file is deliberately import-light (stdlib only) so that the hermetic
// gate can run it with no engine packages compiled in.
func shellQuoteForProbe(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hookProbeBundleYAML declares the session_start hook as a BUNDLE hook, which
// is the production authoring surface for one (bundles.BundleHooks.SessionStart
// → config.ResolveBundleHooks → the per-engine SettingsWriter). Deliberately
// not a config.yaml `hooks:` block and emphatically not a hand-written
// settings.json: the probe's claim is about the path a real user's profile
// takes, and a fixture that wrote the engine's native file itself would prove
// the engine execs files WE did not write the way production writes them.
//
// The command carries the harp as its single argument — the planted channel.
func hookProbeBundleYAML(scriptPath, stampHarp string) string {
	return fmt.Sprintf("version: \"1.0.0\"\nhooks:\n  session_start:\n    - command: %q\n      type: command\n      timeout: 30\n",
		scriptPath+" "+stampHarp)
}

// hookProbePrompt is the turn the cell buys.
//
// For a stage-(a)-only cell it is as SMALL as a turn can be. The assertion is a
// file on disk; the model's words are not read at all beyond "the run produced
// something", so asking for anything elaborate would buy tokens to inspect
// output nobody inspects — and would import the output-contract flakiness that
// belongs to P0, not here. The live example of why that matters is kiro, whose
// host/none P0 cell is red-mapped for terminal decoration leaking into stdout:
// a P3 kiro cell that parsed stdout would inherit that red and report a
// hook-firing failure that is nothing of the kind.
//
// For a stage-(b) cell the prompt asks for the phrase the hook's own output put
// into context, in the same strict one-object form the floor uses, because
// there the model's answer IS the observation.
func hookProbePrompt(echo bool) string {
	if !echo {
		return "Reply with the single word OK and nothing else. Do not use any tools. " +
			"Do not read or write any files. Do not explain."
	}
	return "Output a single JSON object and nothing else. The object has exactly one key, \"hook\", " +
		"and its value is the ctxloom hook probe phrase that appears in the additional context provided " +
		"to you at the start of this session (not in this message). Rules, all mandatory: output JSON only; " +
		"no preamble; no postamble; no explanation; no markdown code fences; no backticks. Your entire " +
		"response must be exactly one line of the form {\"hook\":\"THE_PHRASE\"}."
}

// --- the cell's captured state -------------------------------------------------

// hookProbeState is one P3 cell's fixture and its captured result.
//
// The stamp is carried as READ BYTES PLUS THE READ ERROR rather than as a bool.
// "The file was not there", "the file was there and empty" and "the file was
// there with the wrong bytes" are three different findings — no hook fired, a
// hook fired and wrote nothing, a hook fired with the wrong argv — and a
// boolean would collapse all three into the same red.
type hookProbeState struct {
	engine    string // backend type as written in the Examples table
	runtime   string // host | container
	workspace string // none | worktree

	stampHarp string // planted in the hook command's ARGV
	echoHarp  string // planted in the hook's STDOUT; empty for a stage-(a)-only cell

	scriptPath string
	scriptAbs  string
	relDir     string
	stampRel   string
	stampPath  string
	stampBody  string // the stamp file's bytes, read after the run
	stampErr   error  // why the stamp file could not be read; nil once read

	// authored are the ABSOLUTE paths the fixture itself wrote. The carriage
	// scan excludes them: the bundle YAML declares the hook command in plain
	// text, so counting it would report a delivery that never happened.
	authored []string

	// carriage is EVIDENCE, never a gate: where the hook command turned up
	// after the run, so a red says whether ctxloom failed to WRITE the hook or
	// the engine failed to RUN it. Not an assertion, because an engine may
	// legitimately be handed its settings through an out-of-cwd scratch file on
	// a launch flag, and a check that reddened on that would be asserting a
	// delivery route rather than a capability.
	carriage string

	// carriageRoots is every tree the carriage scan actually walked. It is
	// reported alongside a NOT SEEN so the reader can tell "we looked there and
	// it was absent" from "that path was never searched" — the distinction a
	// container cell turned on before RootGlobs existed.
	carriageRoots []string

	stdout   string
	stderr   string
	exitCode int
	runErr   error
}

// cell is this cell's identity in the ladder's vocabulary — the ledger key its
// harps were minted under and the stamp every failure carries.
func (h *hookProbeState) cell() probeCellID {
	return probeCellID{Probe: probeP3, Engine: h.engine, Runtime: h.runtime, Workspace: h.workspace}
}

// echoCell is the SEPARATE ledger key stage (b)'s harp is minted under. A
// distinct Variant rather than a second value under one key: the mint is keyed
// by cell, and two harps sharing a key would make the ledger — and therefore
// the foreign-harp leak scanner that reads it — describe a cell that does not
// exist.
func (h *hookProbeState) echoCell() probeCellID {
	c := h.cell()
	c.Variant = "echo"
	return c
}

// --- the verdict ----------------------------------------------------------------

// hookProbeAssert is P3's whole verdict, in the matrixAssert mold: named,
// hermetically unit-tested, and composing probe_assert.go's shared failure
// taxonomy rather than re-deriving one. The ORDER is the diagnostic — each
// check is only meaningful once the one before it has passed:
//
//  1. did the run complete at all (a dead run explains everything downstream);
//  2. STAGE (a), the mandatory half: does the stamp file exist, does it carry
//     bytes, and are those bytes the harp the hook was handed on its argv. This
//     is the firing proof and it is asserted on EVERY cell, including the ones
//     where stage (b) is impossible;
//  3. STAGE (b), only where the engine's own ApproachTable says it ingests hook
//     stdout: the turn's JSON carries the OTHER harp, the one that existed
//     nowhere but the hook's standard output.
//
// A cell that reaches stage (b) has already proven firing. That ordering is
// what lets stage (b)'s red be read as "the hook fired but its output never
// reached the model" — a statement about codex's ingestion, not about hooks.
func hookProbeAssert(h *hookProbeState) error {
	v := probeVerdict{Family: "hook-probe", Cell: h.cell(), Channel: channelHookStamp}

	if _, err := v.ran(probeRun{Stdout: h.stdout, Stderr: h.stderr, ExitCode: h.exitCode, Err: h.runErr}); err != nil {
		return err
	}
	if err := hookProbeAssertStamp(v, h); err != nil {
		return err
	}
	if h.echoHarp == "" {
		return nil
	}
	return hookProbeAssertEcho(h)
}

// hookProbeAssertStamp is stage (a): the filesystem half, and the only half
// every cell has.
func hookProbeAssertStamp(v probeVerdict, h *hookProbeState) error {
	evidence := fmt.Sprintf("\nstamp path: %s\ncarriage evidence: %s\nstdout:\n%s\nstderr:\n%s",
		h.stampPath, hookProbeCarriageOrUnknown(h), h.stdout, h.stderr)

	// An empty harp would make every containment check below vacuously true
	// (strings.Contains(anything, "") is true), so a cell that forgot to mint
	// would sail through the one check that proves its channel works. Refused
	// as a caller bug, in the same posture probeVerdict.carriesNonce takes.
	if h.stampHarp == "" {
		return v.fail(shapeDelivery,
			"the probe minted no argv harp, so there is nothing to look for in the stamp file — every file contains the empty string, and this check would pass without proving anything",
			evidence)
	}

	if h.stampErr != nil {
		return v.fail(v.Channel.Shape,
			fmt.Sprintf("%s — the hook's stamp file could not be read (%v). ctxloom wrote a session_start hook into this engine's own hook surface and the engine did not produce the file that hook exists to write, so on this evidence the hook DID NOT FIRE. That is a finding about the engine, not a harness fault: check the carriage evidence below to tell a missing WRITE from a missing RUN.",
				v.Channel.Shape, h.stampErr),
			evidence)
	}
	if strings.TrimSpace(h.stampBody) == "" {
		return v.fail(shapeSilentNoOp,
			"the hook's stamp file EXISTS and is EMPTY — the hook was exec'd and wrote zero bytes. That is a different finding from a hook that never ran, and it is this project's characteristic failure shape (success-shaped, no payload); it must never be read as an ordinary mismatch.",
			evidence)
	}
	if !strings.Contains(h.stampBody, h.stampHarp) {
		return v.fail(shapeValue,
			fmt.Sprintf("%s — the stamp file carries bytes but not the harp %q the hook command was given on its argv. Something exec'd and wrote here; it was not this cell's hook with this cell's argument.",
				shapeValue, h.stampHarp),
			evidence+fmt.Sprintf("\nstamp file contents:\n%s", h.stampBody))
	}
	return nil
}

// hookProbeAssertEcho is stage (b): the harp the hook printed on STDOUT reached
// the model. Only reached for an engine whose declared context approach is the
// hook, so a red here is about that engine's ingestion of hook output and
// nothing else.
//
// It gets its OWN probeVerdict, carrying the echo cell and a channel that names
// stdout rather than argv, so the failure message cannot claim the argv channel
// failed when what failed was ingestion. Reusing stage (a)'s verdict would have
// been one line shorter and would have mislabelled every stage-(b) red.
func hookProbeAssertEcho(h *hookProbeState) error {
	v := probeVerdict{
		Family:  "hook-probe",
		Cell:    h.echoCell(),
		Channel: channelHookStdout,
	}
	trimmed, err := v.ran(probeRun{Stdout: h.stdout, Stderr: h.stderr, ExitCode: h.exitCode, Err: h.runErr})
	if err != nil {
		return err
	}
	got, err := v.jsonObject(trimmed)
	if err != nil {
		return err
	}
	if err := v.carriesNonce(trimmed, h.echoHarp); err != nil {
		return err
	}
	return v.exactObject(got, trimmed, hookProbeEchoKey, h.echoHarp)
}

// hookProbeCarriageOrUnknown renders the carriage evidence, and says so plainly
// when there is none. "" in a failure message reads as "we checked and found
// nothing", which is a stronger claim than the scan can make: the hook may
// legitimately ride an out-of-cwd scratch settings file handed to the engine on
// a launch flag, which this scan does not walk.
func hookProbeCarriageOrUnknown(h *hookProbeState) string {
	if h.carriage != "" {
		return h.carriage
	}
	where := "NO ROOT AT ALL — the scan searched nothing, so this says nothing"
	if len(h.carriageRoots) > 0 {
		where = "under " + strings.Join(h.carriageRoots, ", ")
	}
	return "NOT SEEN at any point during the run, " + where + ". The scan watches DURING the run precisely because ctxloom scrubs delivered settings at teardown (measured 2026-08-13: claude's delivered settings.json is `{}` immediately after a run whose hook demonstrably fired), so this is meaningful — but it is still not proof of absence: an engine handed its settings through a path none of those roots covers would look the same"
}

// --- the carriage evidence scan --------------------------------------------------

// hookProbeContainerOverlayScratchPrefix is the name isolation gives every
// per-run container scratch root: isolation.prepareContainerScratch calls
// os.MkdirTemp with this prefix, and isolation.containerConfigOverlay puts the
// engine's managed-config overlay inside it as cfg0, cfg1, …
//
// Cited by PREFIX rather than restated as a rule, and the test below is what
// keeps the citation honest: if isolation renames it, the glob quietly matches
// nothing and a container cell's carriage goes back to being unobservable
// while still reporting a confident NOT SEEN. That is the failure this whole
// field exists to end, so it gets an assertion rather than a comment.
const hookProbeContainerOverlayScratchPrefix = "ctxloom-iso-"

// hookProbeContainerOverlayGlobs returns the patterns that reach a container
// cell's managed-config overlay — where a containerized engine's settings are
// actually delivered, as opposed to the project tree the host cells watch.
//
// WHY BOTH BASES. isolation.containerScratchBase returns "" on linux, so
// os.MkdirTemp falls back to os.TempDir(); on darwin it returns "/tmp"
// explicitly, because there os.TempDir() is a per-user /var/folders path the
// scratch never lands in. Emitting both costs one glob that matches nothing on
// the platform it does not apply to; guessing wrong costs a silently blind
// cell, which is strictly worse.
func hookProbeContainerOverlayGlobs() []string {
	pattern := hookProbeContainerOverlayScratchPrefix + "*"
	globs := []string{filepath.Join(os.TempDir(), pattern)}
	if os.TempDir() != "/tmp" {
		globs = append(globs, filepath.Join("/tmp", pattern))
	}
	return globs
}

// hookProbeCarriage describes one carriage scan: what to look for, where to
// look, what to ignore, and how recent a file has to be to count.
//
// EVIDENCE, NOT A GATE. Its entire job is to split one red into two: "ctxloom
// never wrote the hook" (carriage — our bug, inventory row 6) versus "ctxloom
// wrote it and the engine never ran it" (firing — the vendor's behaviour,
// inventory row 7). Those are the two subsystems P3 sits between, and a red
// that cannot say which one it found sends the next person to the wrong file.
//
// AUTHORED IS THE FIELD THAT MAKES IT HONEST, and it was added because the scan
// lied without it. The fixture DECLARES the hook in a bundle YAML it writes
// itself, so the hook command is trivially present in the project tree whether
// or not any settings writer ever ran. The first version of this scan reported
// that file as carriage evidence — "hook command found in
// .ctxloom/content/bundles/bundle-hookprobe.yaml" — which reads as "ctxloom
// delivered the hook" and means nothing of the sort. A diagnostic that
// confidently points at the wrong subsystem is worse than one that says
// nothing, so the fixture's own files are excluded by exact path.
//
// ROOTS IS PLURAL FOR A MEASURED REASON. claude is launched with its settings
// on a flag pointing OUT of the project — an ephemeral per-session directory
// under the real home — so a project-only walk finds no delivered hook even on
// a cell where delivery worked perfectly. The session root is scanned too, and
// NotBefore keeps that from turning into a walk of every session ever recorded:
// a directory untouched since the run began cannot contain this run's delivery.
type hookProbeCarriage struct {
	// Needle is the stamp script's absolute path — unique per cell (it carries
	// the scenario's own temp root), so a hit is unambiguous.
	Needle string
	// Roots are the trees to walk, in report order.
	Roots []string
	// RootGlobs are filepath.Glob patterns re-expanded on EVERY scan, for a
	// root that DOES NOT EXIST when the watch starts.
	//
	// MEASURED 2026-08-27, and this is why the field is a pattern rather than
	// another entry in Roots. A container cell delivers claude's settings into
	// the per-run scratch OVERLAY that isolation.containerConfigOverlay mounts
	// over the project's .claude — a directory created after the run begins,
	// under a randomised name. A fixed Roots entry cannot name it, so a
	// container cell's carriage was structurally unobservable and every such
	// cell reported NOT SEEN whether ctxloom delivered the hook or not.
	//
	// TWO TRAPS THIS FIELD IS SHAPED AROUND, both hit while measuring by hand:
	//   - one run creates MORE THAN ONE scratch root (the shared-FS probe's and
	//     the run's), so anything that resolves a single directory picks the
	//     wrong one. A glob walks every match and lets Needle decide.
	//   - stale scratch roots persist for DAYS when teardown is skipped
	//     (taskloom sultry-harmonica), so "the newest match" is not the run's.
	//     NotBefore is what excludes them, which is why a glob is safe here and
	//     a bare "newest directory wins" would not be.
	RootGlobs []string
	// Authored are ABSOLUTE paths the fixture itself wrote. Their contents are
	// the probe's own declaration, never evidence of delivery.
	Authored []string
	// NotBefore bounds the walk to this run: files and directories untouched
	// since then belong to somebody else's session.
	NotBefore time.Time
}

// resolveRoots returns every tree this scan walks: the fixed Roots first, then
// RootGlobs expanded FRESH on this call, deduplicated and in a stable order.
//
// Expanding per call is the whole point — the watcher polls, and the container
// overlay appears mid-run. A glob resolved once at construction would match
// nothing and the scan would silently walk only the fixed roots, which is the
// blind spot RootGlobs exists to close.
func (q hookProbeCarriage) resolveRoots() []string {
	out := make([]string, 0, len(q.Roots)+len(q.RootGlobs))
	seen := make(map[string]bool, len(q.Roots)+len(q.RootGlobs))
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, r := range q.Roots {
		add(r)
	}
	for _, pattern := range q.RootGlobs {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			// A malformed pattern is a harness bug, never a cell failure: this
			// scan is evidence and must not redden a cell it cannot inform.
			continue
		}
		sort.Strings(matches)
		for _, m := range matches {
			add(m)
		}
	}
	return out
}

// hookProbeCarriageScan runs the scan and returns a one-line summary, or "" when
// nothing was found.
//
// Errors are folded into the returned string rather than returned: a scan that
// could not walk a directory must not fail the cell, because the cell's verdict
// is the stamp file and this is only a note attached to it.
func hookProbeCarriageScan(q hookProbeCarriage) string {
	if q.Needle == "" {
		return "carriage scan skipped: no hook command to look for"
	}
	authored := make(map[string]bool, len(q.Authored))
	for _, p := range q.Authored {
		if abs, err := filepath.Abs(p); err == nil {
			authored[abs] = true
		}
	}

	var hits []string
	for _, root := range q.resolveRoots() {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable subtree: note nothing, fail nothing
			}
			info, ierr := d.Info()
			if ierr != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == ".git" || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				// The root itself is always descended: its own mtime says
				// nothing about the subtree we care about.
				if path != root && !q.NotBefore.IsZero() && info.ModTime().Before(q.NotBefore) {
					return filepath.SkipDir
				}
				return nil
			}
			if info.Size() > hookProbeCarriageMaxFileBytes {
				return nil
			}
			if !q.NotBefore.IsZero() && info.ModTime().Before(q.NotBefore) {
				return nil
			}
			if authored[path] {
				return nil // the probe's own declaration, not a delivery
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			if strings.Contains(string(b), q.Needle) {
				hits = append(hits, path)
			}
			return nil
		})
		if err != nil {
			hits = append(hits, fmt.Sprintf("(scan of %s failed: %v)", root, err))
		}
	}
	if len(hits) == 0 {
		return ""
	}
	return "hook command found in: " + strings.Join(hits, ", ")
}

// hookProbeCarriageMaxFileBytes bounds what the evidence scan will read. A
// settings file is kilobytes; anything megabyte-sized in a fixture project is a
// build artifact or a log, and reading it would turn a diagnostic note into the
// slowest step in the cell.
const hookProbeCarriageMaxFileBytes = 1 << 20

// hookProbeCarriagePollInterval is how often the watcher below re-scans while
// the engine runs. Fast enough to catch a settings file that exists only for
// the length of one short turn; slow enough that the watcher is not competing
// with the engine for the disk on a loaded box.
const hookProbeCarriagePollInterval = 200 * time.Millisecond

// hookProbeCarriageWatcher scans REPEATEDLY WHILE THE ENGINE RUNS, because a
// scan afterwards cannot see the answer.
//
// MEASURED, and this is the whole reason the type exists. ctxloom
// delivers claude's hooks into an ephemeral per-session directory and SCRUBS
// that file at session teardown: immediately after a run whose hook demonstrably
// fired, the delivered settings.json on disk is the three bytes `{}`. So a
// post-run scan reports "no carriage" on a cell where carriage worked perfectly
// — which is not merely useless, it is the exact wrong answer, and this scan's
// only job is to tell a carriage failure from a firing failure.
//
// The watcher removes that blind spot without asserting anything: it still
// produces EVIDENCE, and the cell's verdict is still the stamp file alone.
type hookProbeCarriageWatcher struct {
	query hookProbeCarriage
	done  chan struct{}
	found chan string

	// mu guards searched, which the polling goroutine writes and Searched
	// reads from the test's own goroutine after the run.
	mu sync.Mutex
	// searched is the UNION of every root walked across all passes — not the
	// roots at any single instant. A RootGlobs match exists only while the run
	// does, so asking "what did you search?" after teardown would answer with
	// the fixed roots alone and quietly understate the coverage. A NOT SEEN
	// that names the wrong search is worse than one that names none.
	searched map[string]bool
}

// Searched returns every root the watcher walked, sorted. It is what makes a
// NOT SEEN interpretable: without it the report has to assert where it looked
// in prose, and that sentence goes false the moment a root is added.
func (w *hookProbeCarriageWatcher) Searched() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.searched))
	for r := range w.searched {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// record notes the roots one pass walked.
func (w *hookProbeCarriageWatcher) record(roots []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.searched == nil {
		w.searched = map[string]bool{}
	}
	for _, r := range roots {
		w.searched[r] = true
	}
}

// hookProbeWatchCarriage starts a watcher and returns it. Call Stop exactly
// once, after the run, to collect what it saw.
func hookProbeWatchCarriage(q hookProbeCarriage) *hookProbeCarriageWatcher {
	w := &hookProbeCarriageWatcher{
		query: q,
		done:  make(chan struct{}),
		found: make(chan string, 1),
	}
	go func() {
		ticker := time.NewTicker(hookProbeCarriagePollInterval)
		defer ticker.Stop()
		for {
			w.record(w.query.resolveRoots())
			if hit := hookProbeCarriageScan(w.query); hit != "" {
				// FIRST hit wins and the watcher stops looking. The question
				// is binary — was the hook ever delivered — so continuing to
				// poll would only accumulate the same answer while the engine
				// is trying to use the disk.
				w.found <- hit
				return
			}
			select {
			case <-w.done:
				return
			case <-ticker.C:
			}
		}
	}()
	return w
}

// Stop ends the watch and returns the carriage evidence, running one final scan
// so a delivery that appeared between the last tick and the process exiting is
// not missed. Empty means nothing was seen at any point during the run.
func (w *hookProbeCarriageWatcher) Stop() string {
	close(w.done)
	select {
	case hit := <-w.found:
		return hit
	default:
	}
	w.record(w.query.resolveRoots())
	return hookProbeCarriageScan(w.query)
}

// --- the IN-CONTAINER carriage scan ----------------------------------------

// WHY A SECOND SCAN EXISTS AT ALL, measured 2026-08-27. A host-side walk can
// never answer carriage for a container cell, and RootGlobs reaching the
// managed-config overlay did not change that — it only proved the overlay is
// the wrong place to look. claude is handed its settings on claude.flagSettings
// ("--settings") naming a file in the session's EPHEMERAL directory. On a host
// cell that path is under the real home, which the session root already covers.
// On a container cell the engine gets a FRESH HOME and
// isolation.Container.sessionStateMounts binds only the PERSIST directory back
// to the host — there is no ephemeral bind — so the file claude actually reads
// exists ONLY inside the container. The bytes are not on the host, so no
// host-side root list, however long, can reach them.

// hookProbeContainerExec runs one command inside a named container and returns
// its combined stdout.
//
// A SEAM, so the scan's logic is exercised hermetically. The real
// implementation shells out to `docker exec`; the tests substitute a function
// and never need a container, which is the only way any of this is testable in
// the untagged file that `just test` runs.
type hookProbeContainerExec func(container string, env map[string]string, argv ...string) ([]byte, error)

// hookProbeContainerCarriageScript greps the container's OWN filesystem for the
// hook command.
//
// The needle rides an ENVIRONMENT VARIABLE rather than the argv this script is
// built from: the needle is a filesystem path chosen by the harness, and
// interpolating it into a shell string is how a path containing a quote turns a
// diagnostic into arbitrary shell. $HOME is expanded INSIDE the container
// because that is the whole point — the container's home is not the host's, and
// hardcoding isolation's defaultContainerHome here would be a second copy of a
// value that package already owns.
//
// -F is literal matching: the needle is a path, and an unanchored regex over
// paths matches things it should not. Errors are swallowed because an
// unreadable subtree must not redden a cell over EVIDENCE.
const hookProbeContainerCarriageScript = `grep -rlF --exclude-dir=.git --exclude-dir=node_modules -- "$CTXLOOM_PROBE_NEEDLE" "$HOME/.ctxloom" $CTXLOOM_PROBE_DIRS 2>/dev/null | head -40`

// hookProbeContainerScan looks for the hook command inside a running container
// and returns a one-line summary, or "" when nothing was found.
//
// Like the host scan this is EVIDENCE, never a gate: every error folds into the
// empty string, because a cell's verdict is the stamp file and a scan that
// could not run must not turn a firing failure into a harness failure.
func hookProbeContainerScan(run hookProbeContainerExec, container, needle string, dirs, authored []string) string {
	if run == nil || container == "" || needle == "" {
		return ""
	}
	env := map[string]string{
		"CTXLOOM_PROBE_NEEDLE": needle,
		"CTXLOOM_PROBE_DIRS":   strings.Join(dirs, " "),
	}
	out, err := run(container, env, "sh", "-c", hookProbeContainerCarriageScript)
	if err != nil {
		return ""
	}
	excluded := make(map[string]bool, len(authored))
	for _, p := range authored {
		excluded[p] = true
	}
	var hits []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || excluded[line] {
			continue
		}
		hits = append(hits, line)
	}
	if len(hits) == 0 {
		return ""
	}
	return "hook command found INSIDE the container " + container + " at: " + strings.Join(hits, ", ")
}

// hookProbeIsContainerAxis reports whether a cell's runtime axis names a
// container runtime.
//
// PREFIX, not equality: the axis carries the OWNERSHIP too
// ("container-rootless", "container-rootful"), and both need the in-container
// vantage point for the same reason. Matching either value exactly would leave
// the other silently on the host-only path — a cell that still reports a
// confident NOT SEEN while never looking where the bytes are.
func hookProbeIsContainerAxis(runtime string) bool {
	return strings.HasPrefix(runtime, "container")
}
