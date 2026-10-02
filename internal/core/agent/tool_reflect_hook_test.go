package agent

import (
	"slices"
	"strconv"
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

	if want := []string{"hook", "tool-reflect", "--min-output-bytes", strconv.Itoa(threshold)}; !slices.Equal(h.Args, want) {
		t.Fatalf("argv %q, want %q; without the threshold the hook would install inert", h.Args, want)
	}
	if h.Type != "command" {
		t.Fatalf("hook type %q, want command", h.Type)
	}
	if h.Timeout != ToolReflectTimeout {
		t.Fatalf("timeout %d, want %d -- this hook runs on EVERY tool call", h.Timeout, ToolReflectTimeout)
	}
}
