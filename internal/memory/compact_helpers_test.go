package memory

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// assembleBody re-attaches rendered plan blocks after the LLM summary. These
// cases pin the join behavior across the empty/non-empty combinations.
func TestAssembleBody(t *testing.T) {
	plan := PlanBlock{Content: "# Plan\n- step one"}

	t.Run("body and plans joined with a blank line", func(t *testing.T) {
		got := assembleBody("summary text", appendices{Plans: []PlanBlock{plan}})
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
		if got := assembleBody("just the body", appendices{}); got != "just the body" {
			t.Fatalf("got %q, want body unchanged", got)
		}
	})

	t.Run("empty body with plans has no leading blank line", func(t *testing.T) {
		got := assembleBody("", appendices{Plans: []PlanBlock{plan}})
		if strings.HasPrefix(got, "\n") {
			t.Fatalf("should not lead with a blank line:\n%q", got)
		}
		if !strings.Contains(got, "step one") {
			t.Fatalf("plan should be present:\n%s", got)
		}
	})

	t.Run("empty body and no plans is empty", func(t *testing.T) {
		if got := assembleBody("", appendices{}); got != "" {
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

	// An explicit SessionID naming a DIFFERENT, real harp than the
	// caller's own must route output to THAT harp, not the caller's — the
	// defect was: compact_session with an explicit session_id distilled
	// someone else's session but always wrote the essence, session bind, and
	// summary into the CALLER's own harp dir/index entry.
	t.Run("explicit SessionID naming a real, different harp wins over caller's own HarpName", func(t *testing.T) {
		testsupport.Isolate(t)
		mgr, err := sessions.Open("")
		if err != nil {
			t.Fatalf("open index: %v", err)
		}
		target, err := mgr.AssignHarp("/proj", "claude-code")
		if err != nil {
			t.Fatalf("assign harp: %v", err)
		}

		c := &Compactor{config: CompactionConfig{HarpName: "caller-own-harp", SessionID: target.HarpName}}
		if got := c.resolveHarpName(); got != target.HarpName {
			t.Fatalf("got %q, want the target harp %q — compacting someone else's session must not attribute output to the caller's own harp", got, target.HarpName)
		}
	})

	// A SessionID that does NOT resolve to any real harp (a bare engine-native
	// session id, a typo) must still fall back to the caller's own harp —
	// there is nowhere else to attribute output to.
	t.Run("explicit SessionID that is not a real harp falls back to caller's own HarpName", func(t *testing.T) {
		testsupport.Isolate(t)
		c := &Compactor{config: CompactionConfig{HarpName: "caller-own-harp", SessionID: "not-a-real-harp-anywhere"}}
		if got := c.resolveHarpName(); got != "caller-own-harp" {
			t.Fatalf("got %q, want caller-own-harp", got)
		}
	})
}

// updateSessionIndex is a no-op without a harp name; verify it doesn't panic or
// touch the index in that case (the common non-harp path).
func TestSessionIndexNoHarpIsNoop(t *testing.T) {
	c := &Compactor{config: CompactionConfig{}}
	c.updateSessionIndex("", "sess-id", "summary", nil, 0) // must not panic / open index
}
