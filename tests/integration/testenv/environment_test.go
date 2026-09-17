package testenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── binary freshness ──────────────────────────────────────────────────────
//
// These pin the guard that stops this package's own suites from measuring a
// binary that predates the source under edit. The defect they exist for
// (taskloom pregnant-laurel) was invisible precisely BECAUSE it was green:
// `just test-pkg ./tests/integration/ -tags integration` execed whatever
// ctxloom was lying around, so a change to cmd/ or internal/ could be verified
// green having never been executed. Anything that quietly turns
// checkBinaryFreshness back into "return nil" must be caught here, because by
// construction no integration test can catch it — the whole failure mode is
// that they keep passing.

// fakeSourceTree writes a minimal project root: one compiled source file, one
// test file, and go.mod, each stamped with an explicit mtime so the comparison
// under test is deterministic rather than a race with the filesystem clock.
func fakeSourceTree(t *testing.T, srcMod, testMod time.Time) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel string, mod time.Time) string {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte("package x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatalf("chtimes %s: %v", rel, err)
		}
		return path
	}
	write("internal/thing/thing.go", srcMod)
	write("internal/thing/thing_test.go", testMod)
	write("go.mod", srcMod)
	return root
}

// fakeBinary writes an executable stamped with mod.
func fakeBinary(t *testing.T, mod time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ctxloom")
	if err := os.WriteFile(path, []byte("#!/bin/false\n"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes binary: %v", err)
	}
	return path
}

func TestCheckBinaryFreshness_RefusesABinaryOlderThanTheSourceCompiledIntoIt(t *testing.T) {
	built := time.Date(2026, 8, 18, 18, 53, 17, 0, time.UTC)
	edited := built.Add(72 * time.Hour)
	root := fakeSourceTree(t, edited, built)
	bin := fakeBinary(t, built)

	err := checkBinaryFreshness(bin, root)
	if err == nil {
		t.Fatal("checkBinaryFreshness accepted a binary three days older than its source; " +
			"that is the blind gate this guard exists to close")
	}
	msg := err.Error()
	// The refusal has to be actionable cold, at 3am, in a CI log: WHICH binary,
	// WHICH file outran it, and what to do. A bare "stale binary" sends the
	// reader hunting.
	for _, want := range []string{
		bin,
		filepath.Join(root, "internal", "thing", "thing.go"),
		// .Local(): the refusal formats os.FileInfo.ModTime(), which the
		// filesystem hands back in the machine's zone whatever zone the
		// fixture named.
		built.Local().Format(time.RFC3339),
		edited.Local().Format(time.RFC3339),
		"just build",
		allowStaleBinaryEnv,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q; got:\n%s", want, msg)
		}
	}
}

func TestCheckBinaryFreshness_AcceptsABinaryNewerThanItsSource(t *testing.T) {
	edited := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	root := fakeSourceTree(t, edited, edited)
	bin := fakeBinary(t, edited.Add(time.Second))

	if err := checkBinaryFreshness(bin, root); err != nil {
		t.Fatalf("refused a binary built AFTER its source: %v", err)
	}
}

func TestCheckBinaryFreshness_IgnoresTestFileEditsBecauseTheyNeverEnterTheBinary(t *testing.T) {
	built := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	// Only the _test.go file is newer than the binary. A test file is compiled
	// into the TEST process, never into the subprocess under test, so treating
	// it as staleness would refuse every run where someone edited a test —
	// which is every run.
	root := fakeSourceTree(t, built.Add(-time.Hour), built.Add(time.Hour))
	bin := fakeBinary(t, built)

	if err := checkBinaryFreshness(bin, root); err != nil {
		t.Fatalf("a newer _test.go must not count as staleness: %v", err)
	}
}

func TestCheckBinaryFreshness_WaivedByTheOptOutEnvVar(t *testing.T) {
	built := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	root := fakeSourceTree(t, built.Add(72*time.Hour), built)
	bin := fakeBinary(t, built)

	if err := checkBinaryFreshness(bin, root); err == nil {
		t.Fatal("precondition: this pair must be judged stale without the waiver")
	}
	t.Setenv(allowStaleBinaryEnv, "1")
	if err := checkBinaryFreshness(bin, root); err != nil {
		t.Fatalf("%s did not waive the refusal: %v", allowStaleBinaryEnv, err)
	}
}

func TestCheckBinaryFreshness_HasNoOpinionOnATreeWithNoCompiledSource(t *testing.T) {
	// A checkout with none of binaryFreshnessDirs (or one this process cannot
	// read) must degrade to silence, not fail suites for a reason unrelated to
	// what they test.
	bin := fakeBinary(t, time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC))
	if err := checkBinaryFreshness(bin, t.TempDir()); err != nil {
		t.Fatalf("empty source tree must yield no verdict: %v", err)
	}
}

func TestFindAppBinary_AppliesTheFreshnessVerdictToWhateverItResolved(t *testing.T) {
	// End-to-end through the real resolution path against the REAL repo root,
	// so a future refactor that resolves a binary without consulting
	// checkBinaryFreshness fails here. CTXLOOM_BINARY is resolveAppBinary's
	// first branch, which makes the binary under judgement deterministic.
	env := &TestEnvironment{}

	t.Run("stale", func(t *testing.T) {
		t.Setenv("CTXLOOM_BINARY", fakeBinary(t, time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)))
		if got, err := env.findAppBinary(); err == nil {
			t.Fatalf("findAppBinary returned %q for a binary from 1990", got)
		}
	})

	t.Run("fresh", func(t *testing.T) {
		bin := fakeBinary(t, time.Now().Add(time.Hour))
		t.Setenv("CTXLOOM_BINARY", bin)
		got, err := env.findAppBinary()
		if err != nil {
			t.Fatalf("findAppBinary refused a binary newer than every source file: %v", err)
		}
		if got != bin {
			t.Fatalf("findAppBinary returned %q, want %q", got, bin)
		}
	})
}
