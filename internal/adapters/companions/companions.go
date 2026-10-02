// Package companions is ctxloom's side of the companion contract: discover
// companion binaries on PATH, admit them against the trust root, exec each
// admitted one's loadout, and hand the result to a generation as a bundle
// reader. The companion side — the `loadout` subcommand a companion binary
// wires in — is the loadout subpackage, so a lean binary links nothing of
// the bundle model.
package companions

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/companions/loadout"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// companionProbeTimeout bounds the `<bin> loadout --format json` exec at
// boot. A wedged companion must degrade to a warning, never a stalled
// startup. companionProbeWaitDelay bounds how long Output keeps waiting for
// the stdout pipe to close after the direct child is dead — without it, a
// companion that spawned a grandchild inheriting stdout would stall startup
// forever despite the context kill. Vars (not consts) so tests can shrink them.
// The VERSION probe's equivalents live with that probe, in
// cliversion.ProbeTimeout / cliversion.ProbeWaitDelay.
var (
	companionProbeTimeout   = 3 * time.Second
	companionProbeWaitDelay = time.Second
)

// SetCompanionVersionOutputForTesting overrides the version-probe exec seam
// and returns a restore function. Companion of SetLookPathForTesting.
//
// The seam lives with the probe, in cliversion — the owner of the
// cross-binary `version --format json` contract, and the ONE implementation
// the agent image's version key (internal/adapters/isolation) reads through as
// well. Two probes could disagree about what a companion's version IS, and
// the disagreement would surface as an image that never rebuilds.
func SetCompanionVersionOutputForTesting(fn func(string) ([]byte, error)) func() {
	return cliversion.SetOutputForTesting(fn)
}

// CompanionStatus is one boot-time probe of a companion binary — a standalone
// tool (taskloom, ltk) whose built-in bundle wires it into the agent session.
type CompanionStatus struct {
	Bin     string
	Path    string // resolved PATH location; empty when not installed
	Version string // self-reported via `<bin> version --format json`
	Err     error  // version-probe failure for a present binary
	// Admission is why this companion was or was not executed. A present
	// binary that was NOT admitted has a Path, no Version and no Err — without
	// this field that state is indistinguishable from "probe returned nothing",
	// which is precisely the silent no-op a reader must not have to guess at.
	Admission CompanionAdmissionReason
}

// Executed reports whether this companion was actually run. Reason-aware so
// callers stop inferring it from an empty Version.
func (s CompanionStatus) Executed() bool {
	// One reason admits now. It was two — a location exemption and a recorded
	// consent — and both were replaced by the signature this names.
	return s.Admission == CompanionAdmissionSigned
}

// sortedBins renders a companion-name set as the sorted slice every discovery
// function here returns.

// ProbeCompanions resolves each discovered companion on PATH and asks it for
// its version. Missing binaries yield Path == "" (their bundle entries are
// skipped by the resolvers, which also emit the install hint); a present
// binary whose probe fails carries the error. Reporting only — never fatal.
//
// ADMISSION applies here too, not only to the loadout probe: `<bin> version
// --format json` is an exec of a foreign binary exactly like `<bin> loadout` is,
// and this loop runs unconditionally from reportCompanions on `ctxloom run` /
// `ctxloom mcp`. Gating only the loadout probe would have left the auto-exec
// hole wide open through the version probe. An admitted-but-unapproved
// companion reports its Path with Admission naming the refusal, so a caller
// renders "found, not approved" rather than the untrue "not installed".
//
// Probes run concurrently: each is bounded by companionProbeTimeout, so a
// sequential loop would add that bound per wedged companion to startup. Running
// them in parallel keeps the worst-case wall-clock to ~one timeout (CLAUDE.md:
// never block startup). Admission runs before the fan-out, so no unadmitted
// binary is ever exec'd. Output order is preserved (sorted by bin) since each
// goroutine writes its own slot.
func (p Prober) ProbeCompanions(root trust.TrustRoot) []CompanionStatus {
	// Enforced at the exec boundary, not only at each caller: a report path
	// that forgets the switch must still never exec a companion binary.
	if p.Disabled {
		return nil
	}
	admissions := companionAdmission(DiscoverCompanions(), root)
	out := make([]CompanionStatus, len(admissions))
	var wg sync.WaitGroup
	for i, adm := range admissions {
		st := CompanionStatus{Bin: adm.Bin, Path: adm.Path, Admission: adm.Reason}
		if !adm.Allow {
			// Present but refused: keep the Path so the report can say WHICH
			// file was refused, and never exec it.
			out[i] = st
			continue
		}
		wg.Add(1)
		go func(i int, st CompanionStatus) {
			defer wg.Done()
			st.Version, st.Err = cliversion.Probe(st.Path)
			out[i] = st
		}(i, st)
	}
	wg.Wait()
	return out
}

// ===== Companion LOADOUT discovery (signature-envelope spec §4.3, §6) =====
//
// A companion loadout is a bundle a binary on PATH advertises about itself
// (`<bin> loadout --format json`), distinct from the built-in bundles above
// (which ship INSIDE the ctxloom binary).
//
// THE CONTROL POINT IS EXEC, NOT CONTENT. Reading a loadout means RUNNING the
// companion binary, so by the time any content exists that binary has already
// executed arbitrary code with the user's privileges. Reviewing the content
// afterwards buys ~nothing and costs a review prompt for content the user
// deliberately installed, so the decision the human is asked to make is moved
// to where it has purchase: may ctxloom EXECUTE this file (see
// companion_admission.go's trust-on-first-use, keyed on absolute path + binary
// hash). Content that survives that gate is LOCAL-EQUIVALENT and allowed at
// EffectiveTrust's companion step — it is NOT reviewed like a remote bundle,
// and its SIGNATURE does not gate it either (a loadout's bytes cross no
// intermediary, so a publisher signature has nothing to protect them from
// here; signature facts are reported as diagnostics — see
// ProbeCompanionLoadouts). Rejection still reaches it (step 1) and an
// unreadable approvals store still denies it along with everything else. See
// docs/trust-model.md, "Companion loadouts".
//
// Discovery here only finds candidate binaries; admission decides which get
// exec'd, and every surviving loadout is seeded into Config.SeededBundleLoader
// under the ctxloom:companion@<bin> source ref. See config.go's
// companionBundleSeed / SeededBundleLoader wiring.

// firstPartyCompanions are the shipped, first-class companions that do NOT
// match the ctxloom-companion-* PATH convention below (their names predate
// it) but are still discovered unconditionally. reprise is listed for
// completeness (per the agreed discovery contract, "first-party list UNION
// ctxloom-companion-* on PATH") even though it does not implement `loadout`
// yet — its loadout is a separate-repo follow-up; a probe of it degrades
// exactly like any other companion whose loadout subcommand is absent
// (silently skipped, never an error).
var firstPartyCompanions = []string{"ltk", "taskloom", "reprise"}

// SelfCompanion is the companion identity ctxloom probes ITSELF under —
// ctxloom:companion@ctxloom. It is deliberately NOT in firstPartyCompanions:
// those are discovered on PATH and admitted by a signature beside the binary,
// whereas ctxloom's own loadout comes from the RUNNING binary (Prober.Self —
// never a PATH lookup, which could answer with a stale install, and never
// a raw os.Executable, which goes stale after an in-place upgrade) and needs
// no exec consent, because the process is already executing.
const SelfCompanion = agent.CtxloomBinary

// FirstPartyCompanionNames returns the shipped companion names. Exported so a
// test harness can scrub them from a scenario's PATH by asking the list rather
// than keeping a second copy of it — a copy would silently stop matching the
// day a companion is added here.
func FirstPartyCompanionNames() []string {
	return append([]string(nil), firstPartyCompanions...)
}

// companionPathPrefix is the PATH-naming convention a THIRD-PARTY companion
// opts into so ctxloom discovers it without a hardcoded name: any executable
// on PATH named ctxloom-companion-<name> is a discovery candidate.
const companionPathPrefix = "ctxloom-companion-"

// pathDirs is the $PATH-scanning seam for tests.
var pathDirs = func() []string {
	return filepath.SplitList(os.Getenv("PATH"))
}

// readDir is the directory-listing seam for tests (companions-on-PATH scan).
var readDir = os.ReadDir

// DiscoverCompanions returns the deduplicated, sorted set of companion
// binary names to probe for a loadout: the shipped first-party list UNION
// every name on PATH matching the ctxloom-companion-* convention. First-
// party binaries do not match the glob (their names predate the
// convention), so both mechanisms are required — neither alone finds every
// companion (signature-envelope spec §6 discovery).
func DiscoverCompanions() []string {
	seen := make(map[string]bool, len(firstPartyCompanions))
	for _, bin := range firstPartyCompanions {
		seen[bin] = true
	}
	for _, bin := range companionsOnPathByConvention() {
		seen[bin] = true
	}
	return collections.SortedKeys(seen)
}

// companionsOnPathByConvention scans every $PATH directory for entries named
// ctxloom-companion-*, mirroring shell PATH resolution: the first directory
// containing a given name wins over a later duplicate. It does not check the
// executable bit (permission semantics differ by OS, and an attempted exec
// of a non-executable file degrades cleanly through the same "probe failed,
// skip it" path every other companion failure takes) — this is a candidate
// LIST, not a trust decision.
func companionsOnPathByConvention() []string {
	seen := make(map[string]bool)
	var out []string
	for _, dir := range pathDirs() {
		entries, err := readDir(dir)
		if err != nil {
			continue // unreadable/absent PATH entry — ordinary, not a warning
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasPrefix(name, companionPathPrefix) || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// companionLoadoutOutput runs a companion's loadout probe; seam for tests,
// mirrors companionVersionOutput exactly (same timeout/WaitDelay discipline
// — a wedged companion degrades to a warning, never a stalled startup).
var companionLoadoutOutput = func(path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), companionProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, loadout.Subcommand, "--"+loadout.FormatFlag, loadout.FormatJSON)
	cmd.WaitDelay = companionProbeWaitDelay
	return cmd.Output()
}

// SetCompanionLoadoutOutputForTesting overrides the loadout-probe exec seam
// and returns a restore function. Companion of SetCompanionVersionOutputForTesting.
func SetCompanionLoadoutOutputForTesting(fn func(string) ([]byte, error)) func() {
	prev := companionLoadoutOutput
	companionLoadoutOutput = fn
	return func() { companionLoadoutOutput = prev }
}

// Prober is the companion-probing adapter for one invocation. Disabled is
// the --no-companions / CTXLOOM_NO_COMPANIONS switch, a property of the value
// rather than a process global so every exec boundary honours it — no
// companion binary is executed and no companion commands/hooks/MCP/context
// are contributed. Deliberately NO config key: turning companions off is a
// per-invocation or CI decision, not project state someone can leave set and
// later wonder why their ltk commands vanished.
type Prober struct {
	Disabled bool
	// Self arms the self-probe: ctxloom probing ITSELF as a companion (see
	// SelfCompanion). It resolves the RUNNING binary's path at probe time —
	// the composition root injects selfexec.Path, so the answer survives an
	// in-place upgrade and is never a PATH lookup. Only a process composed
	// WITH an embedded loadout sets it (cmd/ctxloom's composition root),
	// because a process without one has nothing to emit, and exec'ing a test
	// binary as if it were ctxloom re-runs the test binary. nil is unarmed.
	// Disabled does NOT disarm it: that switch is about the host's installed
	// binaries, and this is the running one.
	Self func() string
}

// ReaderSource is the per-generation companion reader the composition root
// hands the config Sources: every discovered companion's loadout, seeded
// under its ctxloom:companion@<bin> ref. The reader owns the trust facts (it
// verifies any signature against the generation's full trust root); a
// generation resolves its catalog once, so each probe runs once per
// generation.
func (p Prober) ReaderSource() func(cfg *config.Config) []bundles.Reader {
	return func(cfg *config.Config) []bundles.Reader {
		if len(cfg.GetAppPaths()) == 0 {
			return nil
		}
		root := cfg.Trust().Root()
		probe := func(ctx context.Context) (bundles.CompanionProbe, error) {
			return p.ProbeCompanionLoadouts(ctx, root)
		}
		return []bundles.Reader{bundles.NewCompanionReader(probe, bundles.WithTrustRoot(root), bundles.WithReaderReporter(cfg.Reporter()))}
	}
}

// ProbeCompanionLoadouts is the companion reader's EXEC seam: it discovers
// companions (DiscoverCompanions), ADMITS the ones this machine's human agreed
// ctxloom may execute (AdmitCompanions), and for each admitted one execs
// `<bin> loadout --format json` and unwraps the envelope into the loadout
// document's raw bytes and detached signature.
//
// It stops at BYTES. Parsing them and establishing what their signature turned
// out to be belongs to bundles.NewCompanionReader — one place, shared with
// every other source — so that this function cannot quietly become a second
// notion of what a verified companion is.
//
// A companion that is absent from PATH, not admitted for execution, whose probe
// fails or times out (including a first-party name that does not implement
// `loadout` yet, e.g. reprise today), or whose loadout ENVELOPE is structurally
// unusable — unparseable envelope, unrecognized contract, non-base64 or empty
// document — is SKIPPED with a warning: NEVER fatal, NEVER a crash, NEVER a
// stalled startup. Those cases produced no content in the first place, so there
// is nothing to report.
//
// A SIGNATURE that does not verify is NOT one of those cases, and is not even
// looked at here: companion content is admitted at EXEC, not by signature (see
// docs/trust-model.md, "Companion loadouts"), so the bytes travel on and the
// reader says out loud what their signature was.
//
// Probes run concurrently (mirrors ProbeCompanions), each bounded by
// companionProbeTimeout, so the worst-case wall-clock stays ~one timeout
// regardless of how many companions are admitted.
func (p Prober) ProbeCompanionLoadouts(ctx context.Context, root trust.TrustRoot) (bundles.CompanionProbe, error) {
	if err := ctx.Err(); err != nil {
		return bundles.CompanionProbe{}, err
	}
	// Disabled (--no-companions) means NO DISCOVERED binary is executed — the
	// switch makes a run independent of what the host has installed. It does
	// not disarm the self-probe below: ctxloom's own loadout is the running
	// binary's, not something installed on the host, and a run without it
	// would lose ctxloom's MCP server and its always-on guidance.
	var decided []CompanionAdmission
	if !p.Disabled {
		// ADMISSION, resolved BEFORE the fan-out: a companion no trusted
		// publisher signed is never exec'd. See AdmitCompanions.
		//
		// The refused half is KEPT rather than filtered away. It is the only
		// place a "found on PATH, never allowed to run" companion exists at all
		// — it produces no loadout by definition — and reporting it costs
		// nothing here while reconstructing it later would cost a second
		// discovery pass.
		decided = companionAdmission(DiscoverCompanions(), root)
	}
	admitted, candidates := splitAdmissions(decided)
	// ctxloom ITSELF, first in the fan-out: admitted by identity (the running
	// binary needs no consent to run), probed through the same exec as every
	// other companion so its loadout takes exactly the path theirs does.
	if p.Self != nil {
		admitted = append([]CompanionAdmission{selfAdmission(p.Self())}, admitted...)
	}
	slots := make([]*bundles.CompanionLoadout, len(admitted))
	failed := make([]*bundles.CompanionCandidate, len(admitted))
	var wg sync.WaitGroup
	for i, adm := range admitted {
		wg.Add(1)
		go func(i int, bin, path string) {
			defer wg.Done()
			slots[i], failed[i] = probeLoadout(bin, path)
		}(i, adm.Bin, adm.Path)
	}
	wg.Wait()
	return collectProbes(slots, failed, candidates), nil
}

// splitAdmissions separates the admitted companions from the refused ones,
// which are kept as catalog candidates (with room for ctxloom itself among
// the admitted).
func splitAdmissions(decided []CompanionAdmission) ([]CompanionAdmission, []bundles.CompanionCandidate) {
	admitted := make([]CompanionAdmission, 0, len(decided)+1)
	var candidates []bundles.CompanionCandidate
	for _, a := range decided {
		if a.Allow {
			admitted = append(admitted, a)
			continue
		}
		candidates = append(candidates, bundles.CompanionCandidate{
			Bin: a.Bin, Path: a.Path, Reason: candidateReasonFor(a.Reason),
		})
	}
	return admitted, candidates
}

// probeLoadout execs one admitted companion's loadout probe: its loadout, or
// (warned unless it is the ordinary silent case) a probe-failed candidate.
func probeLoadout(bin, path string) (*bundles.CompanionLoadout, *bundles.CompanionCandidate) {
	raw, err := companionLoadoutOutput(path)
	if err != nil {
		// An unknown `loadout` subcommand (a companion that hasn't
		// adopted the protocol yet) is the ordinary, silent case —
		// *exec.ExitError with no further wrapping. Anything else
		// (context.DeadlineExceeded from a wedged companion, or any
		// other exec failure) is warned: the run would otherwise report
		// success having delivered nothing from that companion.
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			clidiag.Warn("ctxloom", "companion %q: loadout probe failed, withholding: %v", bin, err)
		}
		return nil, &bundles.CompanionCandidate{Bin: bin, Path: path, Reason: bundles.CandidateProbeFailed}
	}
	doc, sig, _, derr := signing.ParseLoadoutEnvelope(raw)
	if derr != nil {
		// STRUCTURAL failure — no content was produced at all. Nothing
		// to hand on, so this half still withholds.
		clidiag.Warn("ctxloom", "companion %q: unparseable loadout envelope, withholding: %v", bin, derr)
		return nil, &bundles.CompanionCandidate{Bin: bin, Path: path, Reason: bundles.CandidateProbeFailed}
	}
	return &bundles.CompanionLoadout{Bin: bin, Path: path, Document: doc, Signature: sig, Self: bin == SelfCompanion}, nil
}

// collectProbes gathers the probes' loadouts, in admission order, and adds
// each failed probe to the candidates.
func collectProbes(slots []*bundles.CompanionLoadout, failed []*bundles.CompanionCandidate, candidates []bundles.CompanionCandidate) bundles.CompanionProbe {
	out := make([]bundles.CompanionLoadout, 0, len(slots))
	for _, lo := range slots {
		if lo != nil {
			out = append(out, *lo)
		}
	}
	for _, c := range failed {
		if c != nil {
			candidates = append(candidates, *c)
		}
	}
	return bundles.CompanionProbe{Loadouts: out, Candidates: candidates}
}

// candidateReasonFor translates a refusal to execute into the reason a catalog
// candidate carries.
//
// Everything except "nothing on this machine answers to that name" is
// UNCONSENTED: unsigned, signed by an untrusted key, a signature that does not
// cover the bytes, or a binary that cannot be read all mean the same thing to a
// reader — the binary is here and ctxloom was not allowed to run it. The
// specific refusal is already announced by admitCompanion itself, which is
// where the distinction has purchase.
func candidateReasonFor(r CompanionAdmissionReason) bundles.CandidateReason {
	if r == CompanionAdmissionNotInstalled {
		return bundles.CandidateAbsent
	}
	return bundles.CandidateUnconsented
}

// selfAdmission is the running binary's own admission: allowed, at the path
// the composition root resolved (Prober.Self), with no signature consulted —
// the process IS executing. Its reason is CompanionAdmissionSelf so a report
// can say which arm allowed it rather than presenting it as signed.
func selfAdmission(path string) CompanionAdmission {
	return newCompanionAdmission(CompanionKey{Bin: SelfCompanion, Path: path}, true, CompanionAdmissionSelf)
}
