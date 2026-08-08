//go:build acceptance

package acceptance

import (
	"testing"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// TestJ4FileContains_UnderliesCarriesTheMarkerStep pins that the
// `^"([^"]*)" carries the marker "([^"]*)"$` step (steps_skill.go:289,
// `return j4FileContains(worldFrom(c), rel, marker)`), once flagged by
// review as "a one-line pass-through to j4FileContains" but explicitly kept --
// the step text gives the trust/skill features their own attribution for the
// living-docs generator's evidence gate, and it is not a duplicate
// implementation. It is also not merely a doc fixture: skill.feature:38,40,51
// and j6_agent_skill.feature:78 all drive real scenarios through it.
//
// Since the step registration itself is an inline closure (not independently
// callable), this exercises the one real function it delegates every byte of
// behaviour to -- j4FileContains (steps_j4.go) -- against a real
// testenv.TestEnvironment, confirming both the success path (file exists,
// contains the marker) and the failure path (marker absent fails loud with
// the file's real content, not a silent pass).
func TestJ4FileContains_UnderliesCarriesTheMarkerStep(t *testing.T) {
	env, err := testenv.NewTestEnvironment()
	if err != nil {
		t.Fatalf("test env: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Cleanup(); err != nil {
			t.Errorf("test environment cleanup: %v", err)
		}
	})
	if err := env.Setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}

	const rel = "SKILL.md"
	const marker = "SKILL-MARKER-pin-test-1a2b3c"
	if err := env.WriteFile(rel, "---\nname: pin-test\n---\nbody carrying "+marker+" inline\n"); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}

	w := &World{env: env}
	if err := j4FileContains(w, rel, marker); err != nil {
		t.Fatalf("j4FileContains: expected marker %q to be found in %s, got: %v", marker, rel, err)
	}

	if err := j4FileContains(w, rel, "MARKER-NOT-PRESENT-ANYWHERE"); err == nil {
		t.Fatal("j4FileContains: expected an error when the marker is absent, got nil")
	}
}
