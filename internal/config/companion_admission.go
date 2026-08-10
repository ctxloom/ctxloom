package config

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/ctxloom/ctxloom/internal/shared/admission"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// CompanionAdmissionReason names WHY a companion was or was not admitted to
// execution. It exists so no caller has to report a refusal as an absence:
// "found on PATH but never confirmed" and "not installed" are different facts
// about the user's machine, and collapsing them is the silent no-op this
// codebase's characteristic bug is made of.
type CompanionAdmissionReason string

const (
	// CompanionAdmissionNotInstalled: the name resolves to nothing on $PATH.
	// Ordinary and silent — most machines have no reprise.
	CompanionAdmissionNotInstalled CompanionAdmissionReason = "not-installed"
	// CompanionAdmissionFirstParty: a shipped companion (ltk, taskloom,
	// reprise) resolving from the expected install location — the directory the
	// running ctxloom binary itself lives in. Automatic, never prompted.
	CompanionAdmissionFirstParty CompanionAdmissionReason = "first-party"
	// CompanionAdmissionConsented: a recorded approval covers this exact
	// (path, sha256).
	CompanionAdmissionConsented CompanionAdmissionReason = "consented"
	// CompanionAdmissionDeclined: a recorded DENIAL covers this path. Beats
	// the first-party exemption.
	CompanionAdmissionDeclined CompanionAdmissionReason = "declined"
	// CompanionAdmissionUnconfirmed: no record, and nothing could ask — a
	// non-interactive session, or a caller that does not prompt. Fail-closed.
	CompanionAdmissionUnconfirmed CompanionAdmissionReason = "unconfirmed"
	// CompanionAdmissionUnreadable: the binary is present but could not be
	// resolved or hashed, so it cannot be identified. Fail-closed.
	CompanionAdmissionUnreadable CompanionAdmissionReason = "unreadable"
	// CompanionAdmissionStoreFault: the consent record exists but cannot be
	// read. Denies EVERY companion, first-party included.
	CompanionAdmissionStoreFault CompanionAdmissionReason = "consent-store-fault"
)

// CompanionAdmission is the decision about whether ctxloom may EXECUTE one
// discovered companion binary — the gate that sits between DiscoverCompanions
// (which only lists candidates) and the two probes that shell out to them.
//
// Its two halves are the shared admission shape's: WHAT was decided about
// (CompanionKey — the discovered name, the resolved absolute path, and the
// binary's hash when one was computed) and WHAT was decided
// (admission.Decision — Allow, Reason, Detail). Both are embedded, so a
// caller still reads a.Bin, a.Path, a.Allow and a.Reason directly.
//
// Path is empty when the name is not installed. SHA256 is empty for a
// first-party admission (decided by location, which never hashes — hashing
// three ~60MB binaries on every startup would be a cost bought for nothing)
// and for every pre-hash refusal.
type CompanionAdmission struct {
	CompanionKey
	admission.Decision[CompanionAdmissionReason]
}

// newCompanionAdmission is the constructor the cascade's arms return through.
// Embedded fields cannot be named in a composite literal by their promoted
// spelling, and spelling both wrappers out at every arm would bury the one
// thing each arm is actually saying.
func newCompanionAdmission(k CompanionKey, allow bool, reason CompanionAdmissionReason) CompanionAdmission {
	return CompanionAdmission{
		CompanionKey: k,
		Decision:     admission.Decision[CompanionAdmissionReason]{Allow: allow, Reason: reason},
	}
}

// companionPromptOut is where the consent question is asked. STDERR, not
// stdout: stdout may be carrying a JSON payload a caller is parsing, and a
// question written into it would corrupt the answer the user actually wanted.
var companionPromptOut io.Writer = os.Stderr

// companionPromptIn is where the answer is read from.
var companionPromptIn io.Reader = os.Stdin

// companionSessionInteractive reports whether there is a human who can be
// asked. BOTH ends are required: stdin must be a terminal for an answer to
// arrive, and stderr must be a terminal or the question is written somewhere
// nobody is reading while the session appears to hang. Agents, CI, `ctxloom
// mcp` over stdio and every piped invocation fail this and are never prompted.
var companionSessionInteractive = func() bool {
	inFile, ok := companionPromptIn.(*os.File)
	if !ok {
		return false
	}
	errFile, ok := companionPromptOut.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(inFile.Fd())) && term.IsTerminal(int(errFile.Fd()))
}

// AdmitCompanions decides, for each discovered companion name, whether ctxloom
// may execute it — trust-on-first-use, keyed on the resolved absolute path AND
// the binary's SHA-256.
//
// prompt selects whether an UNCONFIRMED companion may be put to the human.
// The two probes pass true; a reporting caller (status, doctor) passes false so
// merely LOOKING at companion state can never conjure a security question.
// Even with prompt true, a non-interactive session never asks: it refuses.
//
// Decisions are made SEQUENTIALLY and before any exec, which is what keeps two
// prompts from interleaving on one terminal — the probes' concurrency starts
// after admission, over the admitted set only.
func AdmitCompanions(bins []string, prompt bool) []CompanionAdmission {
	store := companionConsentStore()
	// ONE read for the whole pass: the pre-hash refusal arm needs the records
	// before any candidate has been hashed, and a per-candidate read there
	// would make the pass see the file change underneath it.
	snap, fault := store.Load()
	out := make([]CompanionAdmission, 0, len(bins))
	for _, bin := range bins {
		out = append(out, admitCompanion(bin, store, snap, fault, prompt))
	}
	return out
}

// companionAdmission is the seam the two probes consult, so a test can pin the
// exec-consent answer without building a real binary and a real home record for
// every loadout-parsing case. Production is AdmitCompanions itself.
var companionAdmission = AdmitCompanions

// SetCompanionAdmissionForTesting overrides the exec-consent gate the probes
// consult and returns a restore function. Companion of
// SetCompanionLoadoutOutputForTesting: those seams fake the probe's OUTPUT,
// this one fakes the decision to run it at all.
func SetCompanionAdmissionForTesting(fn func(bins []string, prompt bool) []CompanionAdmission) func() {
	prev := companionAdmission
	companionAdmission = fn
	return func() { companionAdmission = prev }
}

// AdmitEveryDiscoveredCompanionForTesting pins the exec-consent gate OPEN for
// tests whose subject is what a companion contributes once it runs, and returns
// a restore function. It admits whatever the (usually faked) lookPath resolves
// and reports everything else as not-installed.
//
// It exists because the real gate hashes a real file at a real path, which a
// test that faked PATH resolution to "/fake/ltk" cannot satisfy. Callers must
// ask for it EXPLICITLY — a SetLookPathForTesting that silently disabled the
// consent gate as a side effect would let a future regression in that gate go
// unnoticed by every test in the repo.
func AdmitEveryDiscoveredCompanionForTesting() func() {
	return SetCompanionAdmissionForTesting(func(bins []string, _ bool) []CompanionAdmission {
		out := make([]CompanionAdmission, 0, len(bins))
		for _, bin := range bins {
			path, err := lookPath(bin)
			if err != nil {
				out = append(out, newCompanionAdmission(
					CompanionKey{Bin: bin}, false, CompanionAdmissionNotInstalled))
				continue
			}
			out = append(out, newCompanionAdmission(
				CompanionKey{Bin: bin, Path: path}, true, CompanionAdmissionConsented))
		}
		return out
	})
}

// admittedCompanions is the probes' filter: the admitted subset of bins, in
// discovery order, as (bin, path) pairs ready to exec.
func admittedCompanions(bins []string) []CompanionAdmission {
	all := companionAdmission(bins, true)
	out := make([]CompanionAdmission, 0, len(all))
	for _, a := range all {
		if a.Allow {
			out = append(out, a)
		}
	}
	return out
}

// admitCompanion is the per-binary decision cascade. The ORDER is the security
// content, and it mirrors EffectiveTrust's: the fail-closed store gate first,
// then the human's "no", then the exemptions, then the recorded "yes", then the
// question. Nothing short-circuits ahead of a refusal.
//
// Arms 0–2 are companion-specific and stay here: they are about how a name
// resolves to a file, and about an exemption that must be decided BEFORE
// anything pays to hash. Arms 3 and 4 are the shared trust-on-first-use flow
// and are delegated to the store's Decide, which is where "recorded yes",
// "recorded no", "nobody could be asked" and "the store is unreadable" are one
// implementation for every consumer.
func admitCompanion(
	bin string,
	store *admission.Store[CompanionKey, CompanionAdmissionReason],
	consent *admission.Snapshot[CompanionKey],
	fault error,
	prompt bool,
) CompanionAdmission {
	raw, err := lookPath(bin)
	if err != nil {
		// not installed — ordinary, not a warning
		return newCompanionAdmission(CompanionKey{Bin: bin}, false, CompanionAdmissionNotInstalled)
	}
	resolved, rerr := resolveCompanionPath(raw)
	if rerr != nil {
		clidiag.Warn("ctxloom", "companion %q: cannot resolve %s, withholding: %v", bin, raw, rerr)
		return newCompanionAdmission(CompanionKey{Bin: bin, Path: raw}, false, CompanionAdmissionUnreadable)
	}
	key := CompanionKey{Bin: bin, Path: resolved}

	// 0. CONSENT RECORD UNREADABLE. Above every exemption, for the same reason
	//    EffectiveTrust's approvals gate is: a record we cannot read may hold a
	//    DENIAL, and treating that silence as permission re-opens a door a
	//    human closed. Denies first-party companions too.
	if fault != nil {
		clidiag.WarnOnce("ctxloom",
			"companion consent record unreadable, refusing to execute any companion: %v "+
				"(fix or remove it, then re-decide with 'ctxloom companion trust <path>')", fault)
		return newCompanionAdmission(key, false, CompanionAdmissionStoreFault)
	}

	// 1. DECLINED. Path-wide and hash-blind, checked ahead of the first-party
	//    exemption: a human's "no" is supreme, and must survive the binary
	//    being rebuilt or it only ever meant "no, until one byte changes". This
	//    is the arm the store's scope/key split exists for, and the arm that
	//    must answer before the hash is computed.
	if consent.Declined(key) {
		clidiag.WarnOnce("ctxloom",
			"companion %q at %s: execution declined by a recorded decision, skipping "+
				"(undo with 'ctxloom companion untrust %s')", bin, resolved, resolved)
		return newCompanionAdmission(key, false, CompanionAdmissionDeclined)
	}

	// 2. FIRST-PARTY, PINNED BY LOCATION. The name alone exempts nothing —
	//    firstPartyCompanions is a list of three guessable names discovered
	//    unconditionally, so a bare-name exemption would hand automatic
	//    execution to anything that shadows one from earlier in $PATH.
	//    Deliberately BEFORE the hash lookup: this arm never hashes, which is
	//    what keeps `just install` silent at no startup cost.
	if firstPartyPinned(bin, resolved) {
		return newCompanionAdmission(key, true, CompanionAdmissionFirstParty)
	}

	// 3+4. The shared flow, bound to the exact bytes: a recorded approval
	//      admits, a recorded refusal denies, and anything else is put to a
	//      human — or, with nobody to ask, refused. Any change at an approved
	//      path fails the key match and reaches the question again.
	sum, herr := companionBinarySHA256(resolved)
	if herr != nil {
		clidiag.Warn("ctxloom", "companion %q: %v, withholding", bin, herr)
		return newCompanionAdmission(key, false, CompanionAdmissionUnreadable)
	}
	key.SHA256 = sum

	// A nil Ask is the non-interactive case: never a prompt written into a
	// pipe, never an assumed yes. A REPORTING caller (status, doctor) passes
	// prompt false so merely LOOKING at companion state cannot conjure a
	// security question.
	var ask admission.Ask[CompanionKey]
	if prompt && companionSessionInteractive() {
		ask = askCompanionConsent
	}
	// context.Background(): the prompt reads one line from a shared stdin and
	// has no cancellation to honor. The ctx is on Ask for the publish gate,
	// which is driven from a cobra command that has one.
	d, derr := store.Decide(context.Background(), key, ask)
	if derr != nil && d.Allow {
		// The human said yes and the record could not be written. The decision
		// still holds for THIS session — refusing to honor a "yes" just typed
		// would be its own silent no-op — but say so, or the next session asks
		// again with no explanation.
		clidiag.Warn("ctxloom", "companion %q: could not record your decision, it will be asked again: %v", bin, derr)
	}
	if d.Reason == CompanionAdmissionConsented || d.Reason == CompanionAdmissionDeclined {
		// Whatever was just decided is visible to the rest of THIS pass without
		// a re-read — two names can resolve to one file, and a human must not
		// be asked about the same file twice in one session.
		consent.Note(CompanionConsentRecord{Key: key, Approved: d.Allow})
	}
	switch d.Reason {
	case CompanionAdmissionUnconfirmed:
		// Only the "nobody could be asked" arm is announced. A question that
		// WAS put and came back empty already printed its own line, and a
		// second warning about it would say the same thing twice.
		if ask == nil {
			clidiag.WarnOnce("ctxloom",
				"companion %q at %s: never confirmed for execution, skipping "+
					"(no terminal to ask on — run ctxloom interactively once, or 'ctxloom companion trust %s')",
				bin, resolved, resolved)
		}
	case CompanionAdmissionStoreFault:
		clidiag.WarnOnce("ctxloom",
			"companion consent record unreadable, refusing to execute any companion: %v "+
				"(fix or remove it, then re-decide with 'ctxloom companion trust <path>')", d.Detail)
	}
	return CompanionAdmission{CompanionKey: key, Decision: d}
}

// companionConsentPrompt is the question put to the human, verbatim. It names
// the three things a decision needs and nothing else: WHICH file (absolute
// path, because the name is the part an attacker chooses), WHAT ctxloom is
// about to do with it (execute, not read), and WHY the name proves nothing.
const companionConsentPrompt = `ctxloom found a companion it has not run before:

  binary: %s
  path:   %s
  sha256: %s

ctxloom EXECUTES this file to read the context it contributes, so allowing it
grants it everything you can do. Any program on your PATH can claim this name,
including a dependency you never chose to install.

Allow ctxloom to run it? [y/N]: `

// askCompanionConsent puts the question and reads one line. Anything that is
// not an explicit yes is a NO — including EOF, a read error, and an empty line
// — because the default of a security question must never be the permissive
// answer. answered distinguishes "the human said no" (record it) from "nothing
// came back" (do not).
//
// It never returns an error: a read failure is a non-answer, which is already
// one of the two outcomes it reports, and the store treats them identically.
// The ctx is unused because the prompt reads one line from a stdin it shares
// with whatever runs next and has no cancellation to honor.
func askCompanionConsent(_ context.Context, k CompanionKey) (allowed, answered bool, err error) {
	fmt.Fprintf(companionPromptOut, companionConsentPrompt, k.Bin, k.Path, k.SHA256)
	line, rerr := readCompanionAnswerLine(companionPromptIn)
	if rerr != nil && line == "" {
		fmt.Fprintln(companionPromptOut, "no answer read — not running it")
		return false, false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, true, nil
	default:
		fmt.Fprintf(companionPromptOut, "not running %s (undo with 'ctxloom companion untrust %s')\n", k.Bin, k.Path)
		return false, true, nil
	}
}

// readCompanionAnswerLine reads ONE line, one byte at a time, deliberately
// unbuffered.
//
// A bufio.Reader would be the obvious choice and would be wrong here: it reads
// ahead by up to its buffer size, and this prompt shares stdin with whatever
// runs next — a second consent question, and then the engine ctxloom is about
// to launch. Bytes swallowed into a buffer that is then discarded are input
// the user typed and nobody ever sees. Reading exactly to the newline leaves
// the rest of stdin untouched for its real owner.
func readCompanionAnswerLine(r io.Reader) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for len(line) < 64 { // an answer is "y" or "n"; anything longer is not one
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return string(line), nil
			}
			line = append(line, buf[0])
		}
		if err != nil {
			return string(line), err
		}
	}
	return string(line), nil
}
