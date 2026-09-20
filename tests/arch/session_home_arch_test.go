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

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

const (
	archHarpA = "ugly-icy-squid"
	archHarpB = "brave-warm-otter"
)

// TestArch_LayoutHasNoHarpKeyedRows pins the deliberate ABSENCE of a Layout row
// for any per-session path. (The name carries the TestArch_ prefix because that
// is what `just test-arch` selects with -run; paths' own
// TestLayout_HasNoHarpKeyedRows is the same claim inside the package, where it
// rides the default suite.)
//
// Layout() enumerates paths whose absence doctor REPORTS
// (doctorCheckLocalTierState); a session's members (paths.HarpMembers) live
// under the home-rooted sessions store and are created at instance time, so a
// row would report a loss that is not one. A row also cannot name a harp that
// does not exist yet, and state/ holds only fixed project-local residents.
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
// The roster is every function that resolves an instance: the session dir
// and the home member under it. An engine contributes only a leaf (its HomeVar.Subdir), never a
// resolver of its own, so there is no per-engine row to add.
func TestArch_SessionHomeResolversRequireHarp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	resolvers := []struct {
		name string
		fn   func(harp string) (string, error)
	}{
		{"paths.HarpDir", paths.HarpDir},
		{"paths.HarpSessionHome", paths.HarpSessionHome},
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
// instance directories collide: each engine's leaf (its HomeVar.Subdir)
// hangs off the same <harp>/home directory, so two engines in one session
// must never resolve to the same directory. A pure function so the
// synthetic fixture below can drive its collision branch before a second
// home-controlled engine exists.
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

// TestArch_EngineInstanceLeavesPairwiseDistinct_SyntheticFixture exercises the
// pairwise-collision branch without waiting for a second home-controlled
// engine to be registered. It
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
			"synthetic-engine-a": "/h/.ctxloom/sessions/harp/home/synthetic-a",
			"synthetic-engine-b": "/h/.ctxloom/sessions/harp/home/synthetic-b",
		}
		if violations := pairwiseDistinctViolations(dirs); len(violations) != 0 {
			t.Errorf("distinct synthetic leaves flagged as colliding: %v", violations)
		}
	})

	t.Run("colliding synthetic leaves are caught", func(t *testing.T) {
		const collided = "/h/.ctxloom/sessions/harp/home/synthetic-shared"
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
