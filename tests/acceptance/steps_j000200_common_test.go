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

// TestEnsureProjectWithEngine_WritesBuildJ000200ConfigVerbatim pins that
// ensureProjectWithEngine, a pass-through kept because it names the common
// case, composes its two named callees (buildJ000200Config,
// scaffoldProjectWithConfig) correctly — the written .ctxloom/config.yaml is
// exactly buildJ000200Config's rendered output, byte for byte — and that
// scaffoldProjectWithConfig's own idempotency contract ("a second call on an
// already-initialized World is a no-op") survives the wrapper: calling it
// twice does not re-render or truncate the config.
func TestEnsureProjectWithEngine_WritesBuildJ000200ConfigVerbatim(t *testing.T) {
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
	want := buildJ000200Config("mylabel", "mock")
	if got != want {
		t.Fatalf("ensureProjectWithEngine wrote a config that does not match buildJ000200Config's output:\ngot:\n%s\nwant:\n%s", got, want)
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
