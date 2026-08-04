package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

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
type CompanionAdmission struct {
	// Bin is the discovered name.
	Bin string
	// Path is the resolved absolute path, empty when the name is not installed.
	Path string
	// SHA256 is the binary's hash when one was computed. Empty for a
	// first-party admission (which is decided by location and never hashes —
	// hashing three ~60MB binaries on every startup would be a startup cost
	// bought for nothing) and for every pre-hash refusal.
	SHA256 string
	// Allowed is the decision.
	Allowed bool
	// Reason names which rule decided.
	Reason CompanionAdmissionReason
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
	consent := loadCompanionConsent()
	out := make([]CompanionAdmission, 0, len(bins))
	for _, bin := range bins {
		out = append(out, admitCompanion(bin, &consent, prompt))
	}
	return out
}

// admittedCompanions is the probes' filter: the admitted subset of bins, in
// discovery order, as (bin, path) pairs ready to exec.
func admittedCompanions(bins []string) []CompanionAdmission {
	all := AdmitCompanions(bins, true)
	out := make([]CompanionAdmission, 0, len(all))
	for _, a := range all {
		if a.Allowed {
			out = append(out, a)
		}
	}
	return out
}

// admitCompanion is the per-binary decision cascade. The ORDER is the security
// content, and it mirrors EffectiveTrust's: the fail-closed store gate first,
// then the human's "no", then the exemptions, then the recorded "yes", then the
// question. Nothing short-circuits ahead of a refusal.
func admitCompanion(bin string, consent *companionConsent, prompt bool) CompanionAdmission {
	a := CompanionAdmission{Bin: bin, Reason: CompanionAdmissionNotInstalled}

	raw, err := lookPath(bin)
	if err != nil {
		return a // not installed — ordinary, not a warning
	}
	resolved, rerr := resolveCompanionPath(raw)
	if rerr != nil {
		a.Path = raw
		a.Reason = CompanionAdmissionUnreadable
		clidiag.Warn("ctxloom", "companion %q: cannot resolve %s, withholding: %v", bin, raw, rerr)
		return a
	}
	a.Path = resolved

	// 0. CONSENT RECORD UNREADABLE. Above every exemption, for the same reason
	//    EffectiveTrust's approvals gate is: a record we cannot read may hold a
	//    DENIAL, and treating that silence as permission re-opens a door a
	//    human closed. Denies first-party companions too.
	if consent.fault != nil {
		a.Reason = CompanionAdmissionStoreFault
		clidiag.WarnOnce("ctxloom",
			"companion consent record unreadable, refusing to execute any companion: %v "+
				"(fix or remove it, then re-decide with 'ctxloom trust companion allow <path>')", consent.fault)
		return a
	}

	// 1. DECLINED. Path-wide and hash-blind, checked ahead of the first-party
	//    exemption: a human's "no" is supreme, and must survive the binary
	//    being rebuilt or it only ever meant "no, until one byte changes".
	if consent.deniedPath(resolved) {
		a.Reason = CompanionAdmissionDeclined
		clidiag.WarnOnce("ctxloom",
			"companion %q at %s: execution declined by a recorded decision, skipping "+
				"(undo with 'ctxloom trust companion forget %s')", bin, resolved, resolved)
		return a
	}

	// 2. FIRST-PARTY, PINNED BY LOCATION. The name alone exempts nothing —
	//    firstPartyCompanions is a list of three guessable names discovered
	//    unconditionally, so a bare-name exemption would hand automatic
	//    execution to anything that shadows one from earlier in $PATH.
	//    Deliberately BEFORE the hash lookup: this arm never hashes, which is
	//    what keeps `just install` silent at no startup cost.
	if firstPartyPinned(bin, resolved) {
		a.Allowed = true
		a.Reason = CompanionAdmissionFirstParty
		return a
	}

	// 3. RECORDED APPROVAL, bound to the exact bytes. Any change at the
	//    approved path fails this match and falls through to the question.
	sum, herr := companionBinarySHA256(resolved)
	if herr != nil {
		a.Reason = CompanionAdmissionUnreadable
		clidiag.Warn("ctxloom", "companion %q: %v, withholding", bin, herr)
		return a
	}
	a.SHA256 = sum
	if consent.approved(resolved, sum) {
		a.Allowed = true
		a.Reason = CompanionAdmissionConsented
		return a
	}

	// 4. ASK — or, with nobody to ask, REFUSE.
	if !prompt || !companionSessionInteractive() {
		a.Reason = CompanionAdmissionUnconfirmed
		clidiag.WarnOnce("ctxloom",
			"companion %q at %s: never confirmed for execution, skipping "+
				"(no terminal to ask on — run ctxloom interactively once, or 'ctxloom trust companion allow %s')",
			bin, resolved, resolved)
		return a
	}
	answer := askCompanionConsent(bin, resolved, sum)
	rec := CompanionConsentRecord{
		Bin:        bin,
		Path:       resolved,
		SHA256:     sum,
		Approved:   answer,
		RecordedAt: time.Now().UTC(),
	}
	if serr := saveCompanionConsent(rec); serr != nil {
		// The decision still holds for THIS session — refusing to honor a "yes"
		// the human just typed because the record could not be written would be
		// its own silent no-op — but say so, or the next session asks again
		// with no explanation.
		clidiag.Warn("ctxloom", "companion %q: could not record your decision, it will be asked again: %v", bin, serr)
	} else {
		consent.records = append(consent.records, rec)
	}
	a.Allowed = answer
	if answer {
		a.Reason = CompanionAdmissionConsented
	} else {
		a.Reason = CompanionAdmissionDeclined
	}
	return a
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
// answer.
func askCompanionConsent(bin, path, sha string) bool {
	fmt.Fprintf(companionPromptOut, companionConsentPrompt, bin, path, sha)
	line, err := bufio.NewReader(companionPromptIn).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(companionPromptOut, "no answer read — not running it")
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		fmt.Fprintf(companionPromptOut, "not running %s (undo with 'ctxloom trust companion forget %s')\n", bin, path)
		return false
	}
}
