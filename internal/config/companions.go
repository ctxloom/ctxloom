package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
	"github.com/ctxloom/ctxloom/internal/shared/companionloadout"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// companionProbeTimeout bounds the `<bin> version --format json` exec at
// boot. A wedged companion must degrade to a warning, never a stalled
// startup. companionProbeWaitDelay bounds how long Output keeps waiting for
// the stdout pipe to close after the direct child is dead — without it, a
// companion that spawned a grandchild inheriting stdout would stall startup
// forever despite the context kill. Vars (not consts) so tests can shrink them.
var (
	companionProbeTimeout   = 3 * time.Second
	companionProbeWaitDelay = time.Second
)

// companionVersionOutput runs a companion's version probe; seam for tests.
var companionVersionOutput = func(path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), companionProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version", "--format", "json")
	cmd.WaitDelay = companionProbeWaitDelay
	return cmd.Output()
}

// SetCompanionVersionOutputForTesting overrides the version-probe exec seam
// and returns a restore function. Companion of SetLookPathForTesting.
func SetCompanionVersionOutputForTesting(fn func(string) ([]byte, error)) func() {
	prev := companionVersionOutput
	companionVersionOutput = fn
	return func() { companionVersionOutput = prev }
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
	return s.Admission == CompanionAdmissionFirstParty || s.Admission == CompanionAdmissionConsented
}

// BuiltinCompanionBins returns the unique non-ctxloom companion executables
// ctxloom knows to look for at boot, sorted: the UNION of DiscoverCompanions
// (the loadout-protocol discovery set, S8 — first-party list ∪
// ctxloom-companion-* on PATH) and any companion still referenced by an
// embedded built-in bundle's hooks/MCP (now typically none — S8 moved
// ltk/taskloom off this path onto their own loadouts — kept for any FUTURE
// built-in bundle that wires in a companion directly). Both status reporting
// (printCompanionStatus) and the version-probe loop (ProbeCompanions) read
// this list, so a companion that adopts either mechanism is probed at boot
// automatically.
func BuiltinCompanionBins() []string {
	seen := make(map[string]bool)
	for _, bin := range DiscoverCompanions() {
		seen[bin] = true
	}
	eachBuiltinBundle(func(_ string, b *bundles.Bundle) {
		for _, hs := range [][]bundles.BundleHook{
			b.Hooks.PreTool, b.Hooks.PostTool, b.Hooks.SessionStart,
			b.Hooks.SessionEnd, b.Hooks.PreShell, b.Hooks.PostFileEdit,
		} {
			for _, h := range hs {
				if bin := companionBin(h.Command); bin != "" {
					seen[bin] = true
				}
			}
		}
		for _, m := range b.MCP {
			if bin := companionBin(m.Command); bin != "" {
				seen[bin] = true
			}
		}
	})
	return sortedBins(seen)
}

// sortedBins renders a companion-name set as the sorted slice every discovery
// function here returns. Three call sites spelled it out by hand.
func sortedBins(seen map[string]bool) []string {
	out := make([]string, 0, len(seen))
	for bin := range seen {
		out = append(out, bin)
	}
	sort.Strings(out)
	return out
}

// ProbeCompanions resolves each built-in companion on PATH and asks it for
// its version. Missing binaries yield Path == "" (their bundle entries are
// skipped by the resolvers, which also emit the install hint); a present
// binary whose probe fails carries the error. Reporting only — never fatal.
//
// EXEC CONSENT applies here too, not only to the loadout probe: `<bin> version
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
// never block startup). Admission runs SEQUENTIALLY before the fan-out, so two
// consent prompts can never interleave on one terminal. Output order is
// preserved (sorted by bin) since each goroutine writes its own slot.
func ProbeCompanions() []CompanionStatus {
	// Enforce the invariant at the exec boundary, not just at each caller —
	// companionBundleSeed and doctor already check CompanionsDisabled
	// themselves, but reportCompanions (cli/startup_helpers.go, called
	// unconditionally from `ctxloom run`/`ctxloom mcp`) did not, so
	// --no-companions/CTXLOOM_NO_COMPANIONS=1 still exec'd every companion
	// binary on PATH.
	if CompanionsDisabled() {
		return nil
	}
	admissions := AdmitCompanions(BuiltinCompanionBins(), true)
	out := make([]CompanionStatus, len(admissions))
	var wg sync.WaitGroup
	for i, adm := range admissions {
		st := CompanionStatus{Bin: adm.Bin, Path: adm.Path, Admission: adm.Reason}
		if !adm.Allowed {
			// Present but refused: keep the Path so the report can say WHICH
			// file was refused, and never exec it.
			out[i] = st
			continue
		}
		wg.Add(1)
		go func(i int, st CompanionStatus) {
			defer wg.Done()
			st.Version, st.Err = companionVersion(st.Path)
			out[i] = st
		}(i, st)
	}
	wg.Wait()
	return out
}

// companionVersion runs `<path> version --format json` and extracts the
// version. It decodes into cliversion.Info — the same struct every companion
// marshals to PRODUCE that output — so the cross-binary contract has exactly
// one declaration and cannot drift between its writer and its reader.
func companionVersion(path string) (string, error) {
	raw, err := companionVersionOutput(path)
	if err != nil {
		return "", fmt.Errorf("run version --format json: %w", err)
	}
	var info cliversion.Info
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", fmt.Errorf("parse version --format json output: %w", err)
	}
	if info.Version == "" {
		return "", errors.New("version --format json output has no version field")
	}
	return info.Version, nil
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
// companion_admission.go's trust-on-first-use). Content that survives that gate
// is LOCAL-EQUIVALENT and allowed at EffectiveTrust's companion step — it is
// NOT reviewed like a remote bundle. Rejection still reaches it (step 1), a
// signature that fails to verify is still tamper and still withholds (see
// ProbeCompanionLoadouts below), and an unreadable approvals store still denies
// it along with everything else. See docs/trust-model.md, "Companion loadouts".
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
	return sortedBins(seen)
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
	cmd := exec.CommandContext(ctx, path, companionloadout.Subcommand, "--"+companionloadout.FormatFlag, companionloadout.FormatJSON)
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

// companionsDisabled is the process-wide companion switch, set once at startup
// from CTXLOOM_NO_COMPANIONS and the --no-companions flag (the flag wins when
// both are set). It mirrors strictness.SetDegraded rather than living on Config
// because config.Load is called from ~10 places across the CLI: a per-Config
// toggle applied at one of them would silently miss the others.
//
// Deliberately NO config key: turning companions off is a per-invocation or CI
// decision, not project state someone can leave set and later wonder why their
// ltk commands vanished.
var (
	companionsMu       sync.Mutex
	companionsDisabled bool
)

// SetCompanionsDisabled turns companion-loadout discovery off (or back on) for
// the process. When off, no companion binary is executed and no companion
// commands/hooks/MCP/context are contributed.
func SetCompanionsDisabled(v bool) {
	companionsMu.Lock()
	defer companionsMu.Unlock()
	companionsDisabled = v
}

// CompanionsDisabled reports whether companion-loadout discovery is off.
func CompanionsDisabled() bool {
	companionsMu.Lock()
	defer companionsMu.Unlock()
	return companionsDisabled
}

// ProbeCompanionLoadouts discovers companions (DiscoverCompanions), ADMITS the
// ones this machine's human agreed ctxloom may execute (AdmitCompanions), and
// for each admitted one execs `<bin> loadout --format json`, verifies any
// signature against root, and parses the result into a bundles.Bundle keyed by
// its ctxloom:companion@<bin> canonical ref — ready to merge into a
// SeededBundleLoader seed map.
//
// A companion that is absent from PATH, not admitted for execution, whose probe
// fails or times out (including a first-party name that does not implement
// `loadout` yet, e.g. reprise today), or whose loadout is unparseable is
// SKIPPED with a warning — NEVER fatal, NEVER a crash, NEVER a stalled startup.
//
// A loadout whose signature FAILS to verify is skipped for a different and
// sharper reason: SIGNED-BUT-INVALID IS NOT UNSIGNED. Unsigned-and-installed is
// now trusted (see the section header above), but a signature that does not
// verify over the bytes it accompanies is TAMPER EVIDENCE, and tamper must
// withhold. That is why the skip lives HERE, at the envelope, rather than
// being left to the trust gate: the gate's companion step would allow the
// bundle, so a tampered loadout that reached it would be laundered into
// local-equivalent content. It never reaches it — it is absent from the
// returned map entirely.
//
// Probes run concurrently (mirrors ProbeCompanions), each bounded by
// companionProbeTimeout, so the worst-case wall-clock stays ~one timeout
// regardless of how many companions are admitted.
func ProbeCompanionLoadouts(root signing.TrustRoot) map[string]*bundles.Bundle {
	// See ProbeCompanions' identical guard.
	if CompanionsDisabled() {
		return nil
	}
	// EXEC CONSENT, resolved sequentially BEFORE the fan-out: a companion this
	// machine's human has not agreed to run is never exec'd, and two prompts
	// can never interleave on one terminal. See AdmitCompanions.
	admitted := admittedCompanions(DiscoverCompanions())
	type probed struct {
		ref string
		b   *bundles.Bundle
	}
	slots := make([]*probed, len(admitted))
	var wg sync.WaitGroup
	for i, adm := range admitted {
		wg.Add(1)
		go func(i int, bin, path string) {
			defer wg.Done()
			raw, err := companionLoadoutOutput(path)
			if err != nil {
				// An unknown `loadout` subcommand (a companion that hasn't
				// adopted the protocol yet) is the ordinary, silent case —
				// *exec.ExitError with no further wrapping. Anything else
				// (context.DeadlineExceeded from a wedged companion, or any
				// other exec failure) previously vanished with NO diagnostic
				// at all; the run reported success having delivered nothing
				// from that companion.
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					clidiag.Warn("ctxloom", "companion %q: loadout probe failed, withholding: %v", bin, err)
				}
				return
			}
			bundleBytes, signer, derr := signing.DecodeLoadoutEnvelope(raw, root, time.Now())
			if derr != nil {
				clidiag.Warn("ctxloom", "companion %q: invalid loadout envelope, withholding: %v", bin, derr)
				return
			}
			b, perr := bundles.ParseBundle(bundleBytes)
			if perr != nil {
				clidiag.Warn("ctxloom", "companion %q: unparseable loadout bundle, withholding: %v", bin, perr)
				return
			}
			ref := remote.CompanionSource + "@" + bin
			b.Name = ref
			b.StampSigner(signer) // "" for unsigned; the verified principal otherwise
			slots[i] = &probed{ref: ref, b: b}
		}(i, adm.Bin, adm.Path)
	}
	wg.Wait()

	out := make(map[string]*bundles.Bundle, len(admitted))
	for _, p := range slots {
		if p != nil {
			out[p.ref] = p.b
		}
	}
	return out
}
