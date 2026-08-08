//go:build acceptance

package acceptance

import (
	"testing"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// TestPromptSection_MissingMarkerFailsLoud pins that promptSection used
// to return "" when the "=== Prompt ===" marker was absent from the mock's
// recorded input — a mock that recorded something other than a prompt (or a
// record-file format change) then looked identical to "the prompt really is
// empty", and every caller's assertion failed with a confusing "prompt does
// not contain X; prompt:" dump of nothing. The marker's absence is a
// harness/product break, not an empty prompt, so it must fail loud instead.
func TestPromptSection_MissingMarkerFailsLoud(t *testing.T) {
	_, err := promptSection("no marker anywhere in this recorded text")
	if err == nil {
		t.Fatal("expected an error when the \"=== Prompt ===\" marker is absent, got nil")
	}
}

// TestPromptSection_ExtractsAfterMarker is the ordinary success path.
func TestPromptSection_ExtractsAfterMarker(t *testing.T) {
	got, err := promptSection("=== Env ===\nFOO=bar\n=== Prompt ===\nhello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("promptSection = %q, want %q", got, "hello world")
	}
}

// TestEnsureProjectWithEngine_WritesBuildJ2ConfigVerbatim pins that
// ensureProjectWithEngine, once flagged by review as "a single pass-through
// expression" but explicitly kept — it names the common case and is used at
// 9 call sites across steps_j2_setup.go, steps_j3.go, steps_j15.go,
// steps_j17.go, and steps_trust_surface.go. This confirms the pass-through
// actually composes its two named callees (buildJ2Config,
// scaffoldProjectWithConfig) correctly — the written .ctxloom/config.yaml is
// exactly buildJ2Config's rendered output, byte for byte — and that
// scaffoldProjectWithConfig's own idempotency contract ("a second call on an
// already-initialized World is a no-op") survives the wrapper: calling it
// twice does not re-render or truncate the config.
//
// Confirmed test-only-vs-live-caller via `go vet -tags acceptance`: renaming
// ensureProjectWithEngine to a bogus name produced RED at all 9 call sites
// (e.g. "undefined: ensureProjectWithEngine" in steps_j2_setup.go,
// steps_j3.go, steps_j15.go, steps_j17.go, steps_trust_surface.go); restored,
// vet is green.
func TestEnsureProjectWithEngine_WritesBuildJ2ConfigVerbatim(t *testing.T) {
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

	w := &World{env: env}
	if err := ensureProjectWithEngine(w, "mylabel", "mock"); err != nil {
		t.Fatalf("ensureProjectWithEngine: %v", err)
	}

	got, err := env.ReadFile(".ctxloom/config.yaml")
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	want := buildJ2Config("mylabel", "mock")
	if got != want {
		t.Fatalf("ensureProjectWithEngine wrote a config that does not match buildJ2Config's output:\ngot:\n%s\nwant:\n%s", got, want)
	}

	// scaffoldProjectWithConfig's documented idempotency: a second call must
	// not touch the already-initialized project (it short-circuits on
	// .ctxloom/config.yaml already existing).
	if err := ensureProjectWithEngine(w, "different-label", "different-engine"); err != nil {
		t.Fatalf("second ensureProjectWithEngine call: %v", err)
	}
	after, err := env.ReadFile(".ctxloom/config.yaml")
	if err != nil {
		t.Fatalf("read config.yaml after second call: %v", err)
	}
	if after != got {
		t.Fatalf("second ensureProjectWithEngine call was not a no-op; config.yaml changed:\nbefore:\n%s\nafter:\n%s", got, after)
	}
}
