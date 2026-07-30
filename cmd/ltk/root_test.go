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
	// `loadout` belongs in this list as much as the others, and more urgently:
	// it is a CROSS-PROCESS wire contract. ctxloom's companion discovery execs
	// `ltk loadout --format json` (companionloadout.Subcommand/FormatFlag/
	// FormatJSON) and a probe that finds no such subcommand contributes
	// nothing, silently. loadout_test exercises the emitter, but nothing pinned
	// that newRootCmd still REGISTERS the command the probe invokes.
	for _, want := range []string{"evaluate", "check", "manage", "version", "loadout"} {
		if !got[want] {
			t.Errorf("command %q missing from the root tree", want)
		}
	}
}
