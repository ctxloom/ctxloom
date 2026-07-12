package main

import "testing"

// TestNewRootCmd pins the command surface the CLI reference is generated from.
// The root lives in a factory (not inline in main) so the doc generator can
// walk the same tree the binary runs.
func TestNewRootCmd(t *testing.T) {
	root := newRootCmd()

	if root.Use != progName {
		t.Errorf("root Use = %q, want %q", root.Use, progName)
	}
	if root.Short == "" || root.Long == "" {
		t.Error("root has no Short/Long — its reference page would be empty")
	}

	got := map[string]bool{}
	for _, c := range root.Commands() {
		got[c.Name()] = true
		if c.Short == "" {
			t.Errorf("command %q has no Short — it would render blank in the reference", c.Name())
		}
	}
	for _, want := range []string{"evaluate", "check", "manage", "version"} {
		if !got[want] {
			t.Errorf("command %q missing from the root tree", want)
		}
	}
}
