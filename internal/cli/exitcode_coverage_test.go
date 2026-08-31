// This gate enforces that a management command must not exit 0 on a real
// failure ("exit-0-on-failure", the project's signature bug family — see the
// silent-no-op standing note: exit 0, success message, nothing actually
// done). `strictness` is deliberately LAUNCH-ONLY (it records fatal startup
// findings that OpenEngineSession/the agent spawner turn into an error via
// strictness.FindingsError), so it gives ordinary management commands no
// policy at all. The actual mechanism management commands need already
// exists one layer up: cli.Run (root.go) turns any non-nil RunE error
// into os.Exit(1) (or an *ExitError's own code) — that seam works fine
// whenever a RunE actually RETURNS its error.
//
// The bug is narrower than "no policy exists": several RunE bodies collect
// per-item failures into a result.Errors slice, print each one via
// clidiag.Warn/Fwarn (or a bare fmt.Fprint), and then `return nil` anyway —
// discarding a genuine failure right before the one seam that would have
// turned it into a non-zero exit. clidiag.WarnErrors (added alongside this
// gate) is the shared replacement: `return clidiag.WarnErrors(prog,
// result.Errors)` prints identically but returns non-nil when errs is
// non-empty, letting cli.Run do its job.
//
// This test is the enforcement half, shaped like format_coverage_test.go's
// formatDebtAllowlist gate: it statically detects the anti-pattern (a
// `for range X.Errors` loop whose body only warns/prints) in every non-test
// .go file directly in this package, and requires each detected site to
// carry a silentFailureAllowlist entry naming the required fix. A detected
// site with no entry fails the build (new debt introduced silently); an
// allowlist entry whose site no longer matches also fails the build (paid
// down but the ledger line was never removed) — so the debt can only shrink
// visibly, never drift stale in either direction.
//
// Fixing at most zero commands here is deliberate, and within this gate's
// stated scope: this test only proves the gate is enforcing, not that the
// six known sites are fixed.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// silentFailureLoopRE finds the anti-pattern: a `for _, x := range
// <expr>.Errors` loop whose very first (and typically only) statement is a
// warn/print call. It does not itself prove the enclosing function then
// `return nil`s — that per-site confirmation is a one-time human read, on
// record in the silentFailureAllowlist description below — it only locates
// candidates cheaply enough to run on every build. A command that fixes its
// site by routing through clidiag.WarnErrors replaces this whole loop with
// a single call, so the fixed site stops matching automatically.
//
// The print-call alternation must track the package's DIAGNOSTIC VOCABULARY,
// not just the two spellings that existed when the gate was written: a fix
// rewrote bundle_distill.go's loop body from `fmt.Fprintln(os.Stderr, e)` to
// `errw.Println(e)` (an iox.ErrWriter), and the detector silently stopped
// seeing that site — a warn-only loop that had merely changed writers read to
// the gate as "debt paid down". `\w+\.Print(ln|f)` covers the ErrWriter form
// (and any other receiver with the same method names) so a loop cannot escape
// detection by switching writers. Note `fmt.Fprintln` does NOT match that
// third alternative (`.Fprintln` is not `.Println`), so both are needed.
var silentFailureLoopRE = regexp.MustCompile(
	`for\s+_,\s*\w+\s*:=\s*range\s+\w+(?:\.\w+)*\.Errors\s*\{\s*\n\s*` +
		`(?:clidiag\.(?:Warn|Fwarn)|fmt\.Fprint(?:ln|f)|\w+\.Print(?:ln|f))\(`,
)

// findSilentFailureSites scans every non-test .go file directly in
// internal/cli (the package this gate scopes: cobra command wiring) for
// silentFailureLoopRE, returning "file.go:line" sites in sorted order so
// the test's output — and the allowlist keyed from it — is stable.
func findSilentFailureSites(t *testing.T) []string {
	t.Helper()
	// Absolute, from this package's compiled-in source path — not "." (see
	// pkgSourceDir): TestMain sandboxes the binary into a temp cwd, where a
	// "." scan finds no sites and this sweep silently reports no debt.
	dir := pkgSourceDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read internal/cli: %v", err)
	}
	var sites []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, loc := range silentFailureLoopRE.FindAllIndex(src, -1) {
			sites = append(sites, fmt.Sprintf("%s:%s", name, enclosingFunc(src, loc[0])))
		}
	}
	sort.Strings(sites)
	return sites
}

// enclosingFuncRE finds a top-level function or method declaration.
var enclosingFuncRE = regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?(\w+)`)

// enclosingFunc names the function containing the byte at off, and is the
// ledger KEY below.
//
// The key used to be a LINE NUMBER, and that was a standing tax rather than a
// one-off annoyance: every entry in the ledger carries its own changelog of
// renumbering ("Line renumbered from :129 to :134", ":62 to :77 to :80 to
// :82"), each triggered by an edit that had nothing to do with the debt it
// tracks. Adding one line of unrelated HELP PROSE to manage.go shifted four
// entries at once and turned the gate red in both directions simultaneously —
// four "new undeclared site" errors and four "stale entry, debt paid down?"
// errors, for a change that touched no error handling at all. A gate that
// cries wolf on unrelated edits gets its ledger updated mechanically, which is
// exactly how a real new site would slip in disguised as renumbering.
//
// A function name moves only when someone renames or deletes the function,
// which is a change worth a ledger update. If a function ever holds TWO
// matching loops they collapse to one key; that is acceptable, because the
// debt this tracks ("can this command warn and still exit 0") is a property of
// the function, not of the individual loop.
func enclosingFunc(src []byte, off int) string {
	last := "<file scope>"
	for _, m := range enclosingFuncRE.FindAllSubmatchIndex(src, -1) {
		if m[0] > off {
			break
		}
		last = string(src[m[2]:m[3]])
	}
	return last
}

// silentFailureAllowlist is this gate's enforcement ledger, shaped like
// format_coverage_test.go's formatDebtAllowlist: every site
// findSilentFailureSites turns up must have
// exactly one entry here naming the fix, and every entry here must still
// match a real site (a fixed site stops matching silentFailureLoopRE, so a
// stale entry means "paid down — delete this line").
//
// TO PAY DOWN ONE ENTRY: replace the named loop with
// `return clidiag.WarnErrors(prog, result.Errors)` (or, if the loop isn't
// the function's tail statement, capture that call's result and return it
// instead of nil), then delete this line. Each entry is independent — fixing
// one never requires touching another.
//
// Confirmed by RUNNING the command against an induced failure, not just by
// reading the source: runManageInstall (`manage install`) and
// runManageHooksInstall (`manage hooks install`) both exit 0 with "permission
// denied" writing backend settings. Their uninstall counterparts and
// runRemoteDiscover share the identical shape by source inspection.
//
// Keys are file:FUNCTION, never file:line. The 23 lines of renumbering
// archaeology this comment replaced are the argument: every entry had been
// re-pointed repeatedly by edits that touched no error handling at all, most
// recently by a one-line help-prose change that shifted four entries at once
// and reported them simultaneously as new debt AND as paid-down debt. The
// underlying anti-pattern (per-backend errors warned, then `return nil`) was
// unchanged through every one of those churns.
var silentFailureAllowlist = map[string]string{

	"bundle_distill.go:runBundleDistill": "the print-only `for _, e := range result.Errors` loop inside emit()'s text closure stays (it must — this same result.Errors also rides the --format json payload, so deleting it would drop the JSON error detail); the actual T9/R1 bug (U034-F02) is FIXED by a check placed AFTER emit() returns, `if len(result.Errors) > 0 { return ... }`, in runBundleDistill itself — that path covers text AND structured formats alike, which folding the fix into this closure's return value could not (Emit only calls the closure for --format text; a json/yaml/toml run never executes it, so an error returned from inside it would still be silently lost for every non-text format). Kept allowlisted because the regex keys on this loop's SHAPE, not on whether the surrounding function still swallows it.",

	"remote_discover.go:runRemoteDiscover": "`remote discover`: per-source discovery errors are only warned by this loop, but U040-F01 already added a check just below it (`if result.Count == 0 && len(result.Errors) > 0 { return ... }`) so a total search failure does exit non-zero; kept allowlisted because the regex keys on this loop's SHAPE (warn-only, no return), not on whether the surrounding function still swallows the failure.",
}

// TestExitCodePolicy_SilentFailureSitesAreAllowlisted is this gate's enforcing
// half. Run with an empty silentFailureAllowlist, it fails once per detected
// site, listing every management command that can currently warn a real
// failure and still exit 0 — that failure listing is the audit trail for
// what's now allowlisted above.
func TestExitCodePolicy_SilentFailureSitesAreAllowlisted(t *testing.T) {
	sites := findSilentFailureSites(t)
	for _, s := range sites {
		if _, ok := silentFailureAllowlist[s]; !ok {
			t.Errorf("management command source at %s can warn a real failure and still exit 0 (T9/R1, the exit-0-on-failure family) — either fix it to `return clidiag.WarnErrors(...)`, or add a silentFailureAllowlist entry naming the required fix", s)
		}
	}
	for s, reason := range silentFailureAllowlist {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("silentFailureAllowlist[%q] needs a non-empty reason", s)
			continue
		}
		found := false
		for _, real := range sites {
			if real == s {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("silentFailureAllowlist entry %q no longer matches any detected site in internal/cli — debt paid down? remove this line", s)
		}
	}
}
