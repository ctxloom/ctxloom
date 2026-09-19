package agent

import (
	"strconv"
	"strings"
	"testing"
)

// TestNewToolReflectHook_CarriesTheResolvedThreshold pins that the threshold
// the caller resolved from config reaches the installed command line. The hook
// binary defaults --min-output-bytes to 0, which DISABLES it, so a constructor
// that dropped the flag would install a hook that never fires -- present in
// settings.json, listed by `manage hooks list`, and silently inert.
func TestNewToolReflectHook_CarriesTheResolvedThreshold(t *testing.T) {
	const threshold = 4096

	h := NewToolReflectHook(threshold)

	if !strings.Contains(h.Command, "hook tool-reflect") {
		t.Fatalf("command does not invoke the callback: %q", h.Command)
	}
	if !strings.Contains(h.Command, "--min-output-bytes "+strconv.Itoa(threshold)) {
		t.Fatalf("threshold %d absent from command; the hook would install inert: %q", threshold, h.Command)
	}
	if h.Type != "command" {
		t.Fatalf("hook type %q, want command", h.Type)
	}
	if h.Timeout != ToolReflectTimeout {
		t.Fatalf("timeout %d, want %d -- this hook runs on EVERY tool call", h.Timeout, ToolReflectTimeout)
	}
}

// TestNewToolReflectHook_QuotesTheBinaryPath pins that the self-exec path is
// shell-quoted. The command string is interpolated into one /bin/sh line, so an
// unquoted path containing a space would split into a bad argv and the hook
// would fail on every tool call.
func TestNewToolReflectHook_QuotesTheBinaryPath(t *testing.T) {
	h := NewToolReflectHook(1)
	if !strings.HasPrefix(h.Command, "'") {
		t.Fatalf("binary path is not shell-quoted: %q", h.Command)
	}
}
