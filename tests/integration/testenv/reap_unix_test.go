//go:build !windows

package testenv

import "testing"

// TestArgvIsLLMServe pins the adjacency rule that keeps the reaper's SIGKILL
// aimed at the plugin subprocess and nothing else. The negative cases are the
// point: each one carries both tokens, so a matcher that tested for them
// independently would pass every one of them and kill an unrelated process.
func TestArgvIsLLMServe(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want bool
	}{
		{
			name: "self-exec shape",
			argv: []string{"/usr/local/bin/ctxloom", "llm", "serve", "mock"},
			want: true,
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
			name: "self-exec shape with label",
			argv: []string{"/usr/local/bin/ctxloom", "llm", "serve", "mock", "--label", "t1"},
			want: true,
		},
		{
			name: "global flag before the subcommand",
			argv: []string{"ctxloom", "-v", "llm", "serve", "mock"},
			want: true,
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
			if got := argvIsLLMServe(tc.argv); got != tc.want {
				t.Errorf("argvIsLLMServe(%q) = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}
