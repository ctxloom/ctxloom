//go:build !windows

package testenv

import "testing"

// TestArgvIsRunner pins the rule that keeps the reaper's SIGKILL aimed at the
// runner subprocess and nothing else. The negative cases are the point: each
// one carries "runner" or a subcommand-shaped token somewhere a loose matcher
// would accept, and a false positive kills an unrelated process.
func TestArgvIsRunner(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want bool
	}{
		{
			name: "a non-runner subcommand is not a target",
			argv: []string{"/usr/local/bin/ctxloom", "llm", "serve", "mock"},
			want: false,
		},
		{
			name: "the runner process",
			argv: []string{"/usr/local/bin/ctxloom", "runner", "mock"},
			want: true,
		},
		{
			name: "the runner process behind a global flag",
			argv: []string{"ctxloom", "-v", "runner", "claude-code"},
			want: true,
		},
		{
			name: "runner as a flag value is not the subcommand",
			argv: []string{"ctxloom", "agent", "set", "--runtime", "runner"},
			want: false,
		},
		{
			name: "a non-runner subcommand with flags is not a target",
			argv: []string{"/usr/local/bin/ctxloom", "llm", "serve", "mock", "--label", "t1"},
			want: false,
		},
		{
			name: "a non-runner subcommand behind a global flag is not a target",
			argv: []string{"ctxloom", "-v", "llm", "serve", "mock"},
			want: false,
		},
		{
			name: "both tokens present but not adjacent",
			argv: []string{"ctxloom", "llm", "chat", "--tool", "serve"},
			want: false,
		},
		{
			name: "wrong order",
			argv: []string{"ctxloom", "serve", "llm"},
			want: false,
		},
		{
			name: "a different subcommand serving something else",
			argv: []string{"ctxloom", "mcp", "serve", "--llm", "mock"},
			want: false,
		},
		{
			name: "tokens only as path components",
			argv: []string{"/opt/llm/bin/serve"},
			want: false,
		},
		{
			name: "llm is the final argument",
			argv: []string{"ctxloom", "serve", "--backend", "llm"},
			want: false,
		},
		{
			name: "empty argv",
			argv: nil,
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := argvIsRunner(tc.argv); got != tc.want {
				t.Errorf("argvIsRunner(%q) = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}
