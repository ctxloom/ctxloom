//go:build mutation

package mutation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests are the ONLY cheap thing in this package. Everything else here
// rebuilds the ctxloom binary and drives a cucumber suite once per mutant
// (~28s each, hours for the whole table), so the parts of the harness most
// likely to be silently wrong — the (file -> features) pairings, and the
// "ignore everything except X" scoping — are pinned here as pure logic
// instead, and can be run on their own in under a second:
//
//	go test -tags mutation -run 'TestMutationTargets' ./tests/mutation/...
//
// Silently wrong is the operative risk. A typo'd feature path scopes the
// suite to nothing and every mutant survives; a broken ignore pattern scopes
// ooze to the whole module and the run never finishes. Neither announces
// itself — both look exactly like an ordinary, very long run.

// TestMutationTargets_TableIsWellFormed pins the shape of every entry: a
// non-empty single-token name, unique names (t.Run would otherwise
// disambiguate duplicates with a #01 suffix that -run cannot address), a
// unique target file (two entries mutating the same file is a duplicated
// multi-hour run, never intent), and at least one feature.
func TestMutationTargets_TableIsWellFormed(t *testing.T) {
	if len(mutationTargets) == 0 {
		t.Fatal("mutationTargets is empty — the harness would measure nothing and report success")
	}

	seenName := map[string]bool{}
	seenPath := map[string]bool{}
	for _, target := range mutationTargets {
		if target.Name == "" {
			t.Errorf("entry for %q has no Name — its subtest could not be addressed by -run", target.SourceRelPath)
		}
		if strings.ContainsAny(target.Name, "/ \t") {
			t.Errorf("entry name %q contains a slash or space — -run's grammar is slash-separated and would not address it", target.Name)
		}
		if seenName[target.Name] {
			t.Errorf("duplicate entry name %q — t.Run would suffix one of them and -run could not address it", target.Name)
		}
		seenName[target.Name] = true

		if seenPath[target.SourceRelPath] {
			t.Errorf("duplicate target file %q — that is the same multi-hour run twice", target.SourceRelPath)
		}
		seenPath[target.SourceRelPath] = true

		if len(target.Features) == 0 {
			t.Errorf("entry %q names no features — ACCEPTANCE_PATHS would be empty, the suite would run EVERYTHING, and the run would never finish", target.Name)
		}
	}
}

// TestMutationTargets_FilesExist checks that every path named in the table is
// a real file on disk. A typo in either column is the harness's quietest
// failure mode: a missing SOURCE file is caught loudly by buildIgnorePattern,
// but a missing FEATURE file just means godog runs zero scenarios, every
// mutant survives, and the run reports a score of 0.0 about nothing after an
// hour of rebuilding.
func TestMutationTargets_FilesExist(t *testing.T) {
	root := repoRoot(t)

	for _, target := range mutationTargets {
		t.Run(target.Name, func(t *testing.T) {
			src := filepath.Join(root, filepath.FromSlash(target.SourceRelPath))
			if info, err := os.Stat(src); err != nil {
				t.Errorf("SourceRelPath %q does not exist: %v", target.SourceRelPath, err)
			} else if info.IsDir() {
				t.Errorf("SourceRelPath %q is a directory; ooze mutates one FILE", target.SourceRelPath)
			}

			for _, feature := range target.Features {
				// Features are relative to tests/acceptance — that is the cwd
				// godog reads Paths from (see run_scoped_suite.sh's
				// `go test ./tests/acceptance/...`).
				path := filepath.Join(root, "tests", "acceptance", filepath.FromSlash(feature))
				if _, err := os.Stat(path); err != nil {
					t.Errorf("feature %q does not exist at %s: %v — the suite would run zero scenarios and every mutant would survive", feature, path, err)
				}
			}
		})
	}
}

// TestMutationTargets_IgnorePatternScopesToTheEntry is the load-bearing one.
// For every entry it rebuilds the ignore pattern and asserts, against the
// real repo walk, that the pattern matches every non-test .go file in the
// module EXCEPT that entry's target — which is the only thing standing
// between a scoped 132-mutant run and ooze mutating all ~830 files.
//
// It walks the tree itself, with the same rules buildIgnorePattern and ooze's
// fsrepository.ListGoSourceFiles use, rather than trusting the count the
// function returns about itself.
func TestMutationTargets_IgnorePatternScopesToTheEntry(t *testing.T) {
	root := repoRoot(t)
	all := listGoSourceFiles(t, root)

	if len(all) < minIgnoredFiles {
		t.Fatalf("walk found only %d non-test .go files under %s — repoRoot() is wrong, and every scoping assertion below would be vacuous", len(all), root)
	}

	for _, target := range mutationTargets {
		t.Run(target.Name, func(t *testing.T) {
			pattern, ignoredCount := buildIgnorePattern(t, root, target.SourceRelPath)

			// The count is exactly "everything the walk found, minus this
			// one target". Anything else means the walk and the pattern
			// disagree about what a source file is.
			if want := len(all) - 1; ignoredCount != want {
				t.Errorf("ignored count = %d, want %d (every non-test .go file except %s)", ignoredCount, want, target.SourceRelPath)
			}

			if pattern.MatchString(target.SourceRelPath) {
				t.Fatalf("ignore pattern MATCHES the target %q — ooze would skip the only file this entry exists to mutate and report a clean run having mutated nothing", target.SourceRelPath)
			}

			var unignored []string
			for _, rel := range all {
				if rel == target.SourceRelPath {
					continue
				}
				if !pattern.MatchString(rel) {
					unignored = append(unignored, rel)
				}
			}
			if len(unignored) != 0 {
				show := unignored
				if len(show) > 10 {
					show = show[:10]
				}
				t.Errorf("%d file(s) besides %s are NOT ignored, e.g. %v — ooze would mutate them too", len(unignored), target.SourceRelPath, show)
			}
		})
	}
}

// TestBuildIgnorePattern_AnchorsWholePaths pins that the pattern is anchored
// and matches whole relative paths, not substrings: an unanchored alternation
// would let a path that merely CONTAINS another path's text be ignored, and
// the target itself is the likeliest victim (internal/operations/trust.go is
// a substring of nothing today, but internal/operations/sign.go is a
// substring of no path only by luck of naming).
func TestBuildIgnorePattern_AnchorsWholePaths(t *testing.T) {
	root := repoRoot(t)
	pattern, _ := buildIgnorePattern(t, root, trustCascadeTarget.SourceRelPath)

	// A real ignored file matches; the same path with anything appended or
	// prepended does not.
	const ignored = "internal/operations/sign.go"
	if !pattern.MatchString(ignored) {
		t.Fatalf("expected %q to be ignored when the target is %q", ignored, trustCascadeTarget.SourceRelPath)
	}
	for _, near := range []string{
		"x/" + ignored,
		ignored + "x",
		"prefix" + ignored,
	} {
		if pattern.MatchString(near) {
			t.Errorf("pattern matched %q — it is not anchored to whole paths", near)
		}
	}
}

// TestBuildIgnorePattern_RefusesAnUnknownTarget pins the refusal that keeps a
// typo'd or moved source path from producing a pattern that ignores
// everything: with no target found, ooze would have nothing left to mutate
// and would report a clean, fast, entirely empty run.
func TestBuildIgnorePattern_RefusesAnUnknownTarget(t *testing.T) {
	root := repoRoot(t)

	fake := &testing.T{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		// buildIgnorePattern calls t.Fatalf, which runtime.Goexit()s this
		// goroutine; the deferred close still runs.
		buildIgnorePattern(fake, root, "internal/operations/no_such_file.go")
	}()
	<-done

	if !fake.Failed() {
		t.Fatal("buildIgnorePattern accepted a target that does not exist — a typo'd SourceRelPath would ignore every file in the module and mutate nothing")
	}
}

// listGoSourceFiles mirrors ooze's fsrepository.ListGoSourceFiles (and
// buildIgnorePattern's own walk): every file ending ".go" but not
// "_test.go", as slash-separated paths relative to root.
func listGoSourceFiles(t *testing.T, root string) []string {
	t.Helper()

	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return files
}
