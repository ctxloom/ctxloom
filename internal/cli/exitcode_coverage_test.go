// T9/R1 gate: a management command must not exit 0 on a real failure
// ("exit-0-on-failure", the project's signature bug family — see the
// silent-no-op standing note: exit 0, success message, nothing actually
// done). `strictness` is deliberately LAUNCH-ONLY (it records fatal startup
// findings that OpenEngineSession/the agent spawner turn into an error via
// strictness.FindingsError), so it gives ordinary management commands no
// policy at all. The actual mechanism management commands need already
// exists one layer up: cli.Execute (root.go) turns any non-nil RunE error
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
// non-empty, letting cli.Execute do its job.
//
// This test is the enforcement half, shaped like format_coverage_test.go's
// T19 formatDebtAllowlist gate: it statically detects the anti-pattern (a
// `for range X.Errors` loop whose body only warns/prints) in every non-test
// .go file directly in this package, and requires each detected site to
// carry a silentFailureAllowlist entry naming the required fix. A detected
// site with no entry fails the build (new debt introduced silently); an
// allowlist entry whose site no longer matches also fails the build (paid
// down but the ledger line was never removed) — so the debt can only shrink
// visibly, never drift stale in either direction.
//
// Fixing at most zero commands here is deliberate (T9/R1's stated scope):
// this test only proves the gate is enforcing, not that the six known sites
// are fixed.
package cli

import (
	"bytes"
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
var silentFailureLoopRE = regexp.MustCompile(
	`for\s+_,\s*\w+\s*:=\s*range\s+\w+(?:\.\w+)*\.Errors\s*\{\s*\n\s*` +
		`(?:clidiag\.(?:Warn|Fwarn)|fmt\.Fprint(?:ln|f))\(`,
)

// findSilentFailureSites scans every non-test .go file directly in
// internal/cli (the package T9/R1 scopes: cobra command wiring) for
// silentFailureLoopRE, returning "file.go:line" sites in sorted order so
// the test's output — and the allowlist keyed from it — is stable.
func findSilentFailureSites(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/cli: %v", err)
	}
	var sites []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, loc := range silentFailureLoopRE.FindAllIndex(src, -1) {
			line := 1 + bytes.Count(src[:loc[0]], []byte("\n"))
			sites = append(sites, fmt.Sprintf("%s:%d", name, line))
		}
	}
	sort.Strings(sites)
	return sites
}

// silentFailureAllowlist is T9/R1's enforcement ledger, shaped like T19's
// formatDebtAllowlist: every site findSilentFailureSites turns up must have
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
// Confirmed by RUNNING the command against an induced failure (not just
// reading the source): manage.go:117 (`manage install`) and manage.go:266
// (`manage hooks install`) both exit 0 with "permission denied" writing
// backend settings; bundle_distill.go:129 (`bundle distill`) exits 0 against
// a malformed bundle YAML. manage.go:154/287 (uninstall counterparts) and
// remote_discover.go:62 share the identical shape by source inspection.
var silentFailureAllowlist = map[string]string{
	"manage.go:117": "runManageInstall (`manage install`): ApplyHooks' per-backend errors are only warned, then the function unconditionally `return nil`s — confirmed by running against a permission-denied backend write (exit 0)",
	"manage.go:154": "runManageUninstall (`manage uninstall`): RemoveHooks' per-backend errors are only warned, then `return nil` — same shape as manage.go:117",
	"manage.go:266": "`manage hooks install` inline RunE: ApplyHooks' per-backend errors are only warned, then `return nil` — confirmed by running against a permission-denied backend write (exit 0)",
	"manage.go:287": "`manage hooks uninstall` inline RunE: RemoveHooks' per-backend errors are only warned, then `return nil` — same shape as manage.go:266",

	"bundle_distill.go:129": "`bundle distill`: per-file distill errors are appended to result.Errors and printed inside emit()'s text closure, which then `return nil`s unconditionally — confirmed by running against a malformed bundle YAML (exit 0)",

	"remote_discover.go:62": "`remote discover`: per-source discovery errors are only warned; the RunE `return nil`s even when every source failed and result.Count is 0",
}

// TestExitCodePolicy_SilentFailureSitesAreAllowlisted is T9/R1's enforcing
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
