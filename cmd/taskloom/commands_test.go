package main

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
)

// TestNoteHiddenMatches pins the anti-silent-truncation hint: a --term or
// --tag-query listing whose matches were partly suppressed by the default
// active-only view says so (with per-kind counts and the flag that reveals
// them), while an unfiltered listing or one with nothing hidden stays quiet.
func TestNoteHiddenMatches(t *testing.T) {
	cases := []struct {
		name      string
		res       operations.TaskListResult
		filtered  bool
		wantParts []string // all must appear; empty = no output at all
	}{
		{
			name:      "filtered with completed and deferred hidden",
			res:       operations.TaskListResult{HiddenCompleted: 37, HiddenDeferred: 1},
			filtered:  true,
			wantParts: []string{"38 more matching task(s)", "37 completed", "1 deferred", "--all"},
		},
		{
			name:      "filtered with only completed hidden omits the deferred clause",
			res:       operations.TaskListResult{HiddenCompleted: 2},
			filtered:  true,
			wantParts: []string{"2 more matching task(s)", "2 completed", "--all"},
		},
		{
			name:     "unfiltered listing stays quiet even with hidden tasks",
			res:      operations.TaskListResult{HiddenCompleted: 40, HiddenDeferred: 3},
			filtered: false,
		},
		{
			name:     "filtered with nothing hidden stays quiet",
			res:      operations.TaskListResult{},
			filtered: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf strings.Builder
			noteHiddenMatches(&buf, &c.res, c.filtered)
			out := buf.String()
			if len(c.wantParts) == 0 {
				if out != "" {
					t.Fatalf("expected no hint, got %q", out)
				}
				return
			}
			for _, part := range c.wantParts {
				if !strings.Contains(out, part) {
					t.Fatalf("hint %q missing %q", out, part)
				}
			}
			if !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
				t.Fatalf("hint must be exactly one line, got %q", out)
			}
			if c.res.HiddenDeferred == 0 && strings.Contains(out, "deferred") {
				t.Fatalf("zero deferred must not be mentioned: %q", out)
			}
			if c.res.HiddenCompleted == 0 && strings.Contains(out, "completed") {
				t.Fatalf("zero completed must not be mentioned: %q", out)
			}
		})
	}
}
