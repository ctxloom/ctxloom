package memory

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// assembleBody re-attaches rendered plan blocks after the LLM summary. These
// cases pin the join behavior across the empty/non-empty combinations.
func TestAssembleBody(t *testing.T) {
	plan := PlanBlock{Content: "# Plan\n- step one"}

	t.Run("body and plans joined with a blank line", func(t *testing.T) {
		got := assembleBody("summary text", []PlanBlock{plan})
		if !strings.HasPrefix(got, "summary text") {
			t.Fatalf("body should lead:\n%s", got)
		}
		if !strings.Contains(got, "step one") {
			t.Fatalf("plan should be appended:\n%s", got)
		}
		if !strings.Contains(got, "summary text\n\n") {
			t.Fatalf("expected blank line between body and plans:\n%q", got)
		}
	})

	t.Run("no plans returns body unchanged", func(t *testing.T) {
		if got := assembleBody("just the body", nil); got != "just the body" {
			t.Fatalf("got %q, want body unchanged", got)
		}
	})

	t.Run("empty body with plans has no leading blank line", func(t *testing.T) {
		got := assembleBody("", []PlanBlock{plan})
		if strings.HasPrefix(got, "\n") {
			t.Fatalf("should not lead with a blank line:\n%q", got)
		}
		if !strings.Contains(got, "step one") {
			t.Fatalf("plan should be present:\n%s", got)
		}
	})

	t.Run("empty body and no plans is empty", func(t *testing.T) {
		if got := assembleBody("", nil); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}

// resolveHarpName prefers the config field over the env var.
func TestResolveHarpName(t *testing.T) {
	t.Run("config field wins over env", func(t *testing.T) {
		testsupport.Isolate(t)
		t.Setenv("CTXLOOM_SESSION_HARP", "from-env")
		c := &Compactor{config: CompactionConfig{HarpName: "from-config"}}
		if got := c.resolveHarpName(); got != "from-config" {
			t.Fatalf("got %q, want from-config", got)
		}
	})

	t.Run("falls back to env when config empty", func(t *testing.T) {
		testsupport.Isolate(t)
		t.Setenv("CTXLOOM_SESSION_HARP", "from-env")
		c := &Compactor{config: CompactionConfig{}}
		if got := c.resolveHarpName(); got != "from-env" {
			t.Fatalf("got %q, want from-env", got)
		}
	})

	t.Run("empty when neither set", func(t *testing.T) {
		testsupport.Isolate(t)
		c := &Compactor{config: CompactionConfig{}}
		if got := c.resolveHarpName(); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}

// updateSessionIndex is a no-op without a harp name; verify it doesn't panic or
// touch the index in that case (the common non-harp path).
func TestSessionIndexNoHarpIsNoop(t *testing.T) {
	updateSessionIndex("", "sess-id", "summary", nil) // must not panic / open index
}
