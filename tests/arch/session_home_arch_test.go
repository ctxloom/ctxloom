//go:build arch

// The per-session engine config-home instance has two structural properties
// nothing inside a single package can guard, because both are statements about
// what the WHOLE tree does NOT do:
//
//  1. Layout() carries no harp-keyed row (the retired durable per-project
//     engine home had one, and doctor walked it).
//  2. No caller anywhere resolves an instance path without a session.
//
// Property 2 is the one that keeps this design from quietly regrowing a durable
// project home: a resolver that tolerated an empty or absent harp would let a
// static writer, a doctor check or a future materialize path land on some
// shared directory and call it "the project's engine home" again.
package arch

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

const (
	archHarpA = "ugly-icy-squid"
	archHarpB = "brave-warm-otter"
)

// TestArch_LayoutHasNoHarpKeyedRows pins the deliberate ABSENCE of a Layout row
// for state/<harp>. (The name carries the TestArch_ prefix because that is what
// `just test-arch` selects with -run; paths' own
// TestLayout_HasNoHarpKeyedRows is the same claim inside the package, where it
// rides the default suite.)
//
// Layout() enumerates paths whose absence doctor REPORTS
// (doctorCheckLocalTierState), and a per-session directory's absence is the
// normal case — it is created at instance time and reaped at session end — so a
// row would report a loss that is not one. A row also cannot name a harp that
// does not exist yet.
//
// Written as "no row is per-session, and no row is the retired durable engine
// home under any spelling" rather than as an equality against today's table, so
// re-introducing a shared project-wide engine home trips it.
func TestArch_LayoutHasNoHarpKeyedRows(t *testing.T) {
	sep := string(filepath.Separator)
	statePrefix := filepath.Join(paths.AppDirName, paths.StateDir) + sep

	for _, e := range paths.Layout() {
		if strings.Contains(e.Rel, sep+paths.SessionHomeDirName) {
			t.Errorf("Layout row %q names an engine config-home instance; instances are per-session and disposable, so they get no row", e.Rel)
		}
		if e.Rel == filepath.Join(paths.AppDirName, paths.StateDir, "engines") {
			t.Errorf("Layout row %q is the retired durable per-project engine home — the per-session instance replaced it", e.Rel)
		}
		if !strings.HasPrefix(e.Rel, statePrefix) {
			continue
		}
		// Every state/<x> row must be a FIXED project-scoped resident, never a
		// session key. The known residents are enumerated on purpose: a new one
		// is added here deliberately, and a harp can never be.
		head := strings.SplitN(strings.TrimPrefix(e.Rel, statePrefix), sep, 2)[0]
		switch head {
		case paths.TrustFileName, paths.LocksDir:
		default:
			t.Errorf("Layout row %q sits under state/%s, which is neither a known fixed resident nor allowed to be a per-session key", e.Rel, head)
		}
	}
}

// TestArch_SessionHomeResolversRequireHarp is gate (b): NO harpless caller can
// resolve an instance path, enforced where it cannot be forgotten — in the
// resolvers themselves. Every one of them takes a harp and REFUSES an empty or
// traversing one, returning no path at all, so there is no expression a
// harpless caller could even write.
//
// The roster is every function that resolves an instance: the two shared joins
// plus each engine's own leaf-owning helper. A new home-controlled engine adds
// a row here.
func TestArch_SessionHomeResolversRequireHarp(t *testing.T) {
	const workDir = "/proj"
	app := filepath.Join(workDir, paths.AppDirName)

	resolvers := []struct {
		name string
		fn   func(harp string) (string, error)
	}{
		{"paths.SessionStatePath", func(h string) (string, error) { return paths.SessionStatePath(app, h) }},
		{"paths.SessionHomePath", func(h string) (string, error) { return paths.SessionHomePath(app, h) }},
		{"claude.SessionConfigDir", func(h string) (string, error) { return claude.SessionConfigDir(workDir, h) }},
	}

	for _, r := range resolvers {
		t.Run(r.name, func(t *testing.T) {
			for _, bad := range []string{"", "..", "../..", "a/b"} {
				got, err := r.fn(bad)
				if err == nil {
					t.Errorf("%s(harp=%q) = %q with no error; a harpless or traversing caller must not resolve an instance path", r.name, bad, got)
				}
				if got != "" {
					t.Errorf("%s(harp=%q) returned %q alongside its error; a refused resolution must name nothing", r.name, bad, got)
				}
			}
			// And the positive control, so the refusal above is not vacuous.
			good, err := r.fn(archHarpA)
			if err != nil {
				t.Fatalf("%s(%q) error = %v", r.name, archHarpA, err)
			}
			if !strings.Contains(good, archHarpA) {
				t.Errorf("%s(%q) = %q, which does not contain the harp — the instance must be keyed by session", r.name, archHarpA, good)
			}
		})
	}
}

// pairwiseDistinctViolations reports one message per pair of engines whose
// instance directories collide. It is the collision-detection half of
// TestArch_EngineInstanceLeavesArePairwiseDistinct, pulled out to a pure
// function so it can be driven by a synthetic fixture
// (TestArch_EngineInstanceLeavesPairwiseDistinct_SyntheticFixture) as well as
// by the real engine registry — the synthetic fixture is what proves this
// logic actually catches a collision instead of only ever seeing the
// one-element map today's single real engine produces.
func pairwiseDistinctViolations(dirs map[string]string) []string {
	var violations []string
	seen := map[string]string{}
	for engine, dir := range dirs {
		if other, dup := seen[dir]; dup {
			violations = append(violations, fmt.Sprintf("%s and %s share the instance directory %q; the leaves must be pairwise distinct", engine, other, dir))
		}
		seen[dir] = engine
	}
	return violations
}

// TestArch_EngineInstanceLeavesArePairwiseDistinct is what lets ONE session
// root host every engine: each engine's own leaf hangs off the same
// <harp>/home directory, so two engines in one session can never read each
// other's config or credentials.
//
// NOTE ON REACH: only one home-controlled engine is registered today, so this
// test's own map has one element and its call into pairwiseDistinctViolations
// can find no pair to compare. That is a property of the real registry, not
// of the collision-detection logic itself — see
// TestArch_EngineInstanceLeavesPairwiseDistinct_SyntheticFixture, which drives
// that same helper with synthetic engine leaves so the pairwise branch is
// actually exercised, and demonstrably fails on a collision, before a second
// real engine ever exists. Adding a second home-controlled engine here is what
// lets THIS test's own map start exercising it too.
func TestArch_EngineInstanceLeavesArePairwiseDistinct(t *testing.T) {
	const workDir = "/proj"
	root, err := paths.SessionHomePath(filepath.Join(workDir, paths.AppDirName), archHarpA)
	if err != nil {
		t.Fatalf("paths.SessionHomePath() error = %v", err)
	}

	claudeDir, err := claude.SessionConfigDir(workDir, archHarpA)
	if err != nil {
		t.Fatalf("claude.SessionConfigDir() error = %v", err)
	}
	dirs := map[string]string{"claude-code": claudeDir}
	for engine, dir := range dirs {
		if filepath.Dir(dir) != root {
			t.Errorf("%s's instance %q does not hang directly off the session home root %q", engine, dir, root)
		}
	}
	for _, v := range pairwiseDistinctViolations(dirs) {
		t.Error(v)
	}
}

// TestArch_EngineInstanceLeavesPairwiseDistinct_SyntheticFixture exercises the
// pairwise-collision branch of TestArch_EngineInstanceLeavesArePairwiseDistinct
// without waiting for a second home-controlled engine to be registered. It
// drives pairwiseDistinctViolations directly with synthetic engine leaves:
// one fixture with distinct leaves (must report nothing) and one with a
// deliberate collision (must be caught). The collision case is the one that
// matters — it is what proves the detection logic actually fires rather than
// merely existing unexercised, so a real second engine inherits a gate that
// has been SEEN to work rather than one that has only ever run against a
// map with nowhere to find a pair.
func TestArch_EngineInstanceLeavesPairwiseDistinct_SyntheticFixture(t *testing.T) {
	t.Run("distinct synthetic leaves report nothing", func(t *testing.T) {
		dirs := map[string]string{
			"synthetic-engine-a": "/proj/.ctxloom/state/harp/home/synthetic-a",
			"synthetic-engine-b": "/proj/.ctxloom/state/harp/home/synthetic-b",
		}
		if violations := pairwiseDistinctViolations(dirs); len(violations) != 0 {
			t.Errorf("distinct synthetic leaves flagged as colliding: %v", violations)
		}
	})

	t.Run("colliding synthetic leaves are caught", func(t *testing.T) {
		const collided = "/proj/.ctxloom/state/harp/home/synthetic-shared"
		dirs := map[string]string{
			"synthetic-engine-a": collided,
			"synthetic-engine-b": collided,
		}
		violations := pairwiseDistinctViolations(dirs)
		if len(violations) == 0 {
			t.Fatal("two synthetic engines sharing an instance directory produced no violation; the pairwise branch did not fire")
		}
		wantSubstr := fmt.Sprintf("share the instance directory %q", collided)
		if !strings.Contains(violations[0], wantSubstr) {
			t.Errorf("violation message = %q, want substring %q", violations[0], wantSubstr)
		}
	})
}

// TestArch_SessionInstancesDoNotShareAcrossSessions is the per-session property
// asserted across every home-controlled engine package at once: session A's instance and
// session B's instance are different directories for every engine, so a
// coordinator and a concurrent second session in the same checkout cannot
// clobber each other's engine config or read each other's copied credentials.
func TestArch_SessionInstancesDoNotShareAcrossSessions(t *testing.T) {
	const workDir = "/proj"
	for _, r := range []struct {
		name string
		fn   func(harp string) (string, error)
	}{
		{"claude.SessionConfigDir", func(h string) (string, error) { return claude.SessionConfigDir(workDir, h) }},
	} {
		a, err := r.fn(archHarpA)
		if err != nil {
			t.Fatalf("%s(A) error = %v", r.name, err)
		}
		b, err := r.fn(archHarpB)
		if err != nil {
			t.Fatalf("%s(B) error = %v", r.name, err)
		}
		if a == b {
			t.Errorf("%s resolves ONE directory (%q) for two sessions; instances are per-session", r.name, a)
		}
	}
}
