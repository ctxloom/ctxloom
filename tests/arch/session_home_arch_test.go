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

// parked_engines: codex/kiro are out of the default build. Every resolver
// roster below drops their row; claude alone still proves each structural
// property, though TestArch_EngineInstanceLeavesArePairwiseDistinct's
// pairwise half needs at least two engines to mean anything and is
// currently vacuous — see that test's own note. grep -rn parked_engines
// finds every parked site.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/claude"
	// "github.com/ctxloom/ctxloom/internal/codex"
	// "github.com/ctxloom/ctxloom/internal/kiro"
	"github.com/ctxloom/ctxloom/internal/paths"
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
		// {"kiro.SessionHome", func(h string) (string, error) { return kiro.SessionHome(workDir, h) }},
		// {"codex.SessionHome", func(h string) (string, error) { return codex.SessionHome(workDir, h) }},
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

// TestArch_EngineInstanceLeavesArePairwiseDistinct is what lets ONE session
// root host every engine: claude's "claude", kiro's "kiro" and codex's ".codex"
// hang off the same <harp>/home directory, so two engines in one session can
// never read each other's config or credentials.
// parked_engines: kiro/codex are out of the default build, leaving only
// claude-code's leaf to check — the PAIRWISE half of this test's claim is
// vacuous until a second engine returns (nothing to be pairwise distinct
// FROM); the "hangs directly off the session home root" half still holds
// real content.
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
	// kiroDir, err := kiro.SessionHome(workDir, archHarpA)
	// if err != nil {
	// 	t.Fatalf("kiro.SessionHome() error = %v", err)
	// }
	// codexRoot, err := codex.SessionHome(workDir, archHarpA)
	// if err != nil {
	// 	t.Fatalf("codex.SessionHome() error = %v", err)
	// }
	// codex's own leaf is appended by cellScopedCodexHome; ConfigDirName is the
	// package's exported statement of what that leaf is.
	// codexDir := filepath.Join(codexRoot, codex.ConfigDirName)

	dirs := map[string]string{"claude-code": claudeDir}
	for engine, dir := range dirs {
		if filepath.Dir(dir) != root {
			t.Errorf("%s's instance %q does not hang directly off the session home root %q", engine, dir, root)
		}
	}
	seen := map[string]string{}
	for engine, dir := range dirs {
		if other, dup := seen[dir]; dup {
			t.Errorf("%s and %s share the instance directory %q; the leaves must be pairwise distinct", engine, other, dir)
		}
		seen[dir] = engine
	}
}

// TestArch_SessionInstancesDoNotShareAcrossSessions is the per-session property
// asserted across the three engine packages at once: session A's instance and
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
		// {"kiro.SessionHome", func(h string) (string, error) { return kiro.SessionHome(workDir, h) }},
		// {"codex.SessionHome", func(h string) (string, error) { return codex.SessionHome(workDir, h) }},
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
