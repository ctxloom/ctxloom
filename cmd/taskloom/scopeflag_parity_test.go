package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReadScopeFlags_EveryProjectScopedReadTakesGlobal pins the read
// subcommands' scope axes together. A read that resolves "the current
// project" and has no --global strands a task whose project cannot be
// resolved: `show` once lacked the flag `list` had, so the one command that
// reads a single task could not reach it.
//
// Every runnable command must be classified. A new command fails here until
// someone decides whether it is a project-scoped read (and so takes --global)
// or names why project scope does not apply to it.
func TestReadScopeFlags_EveryProjectScopedReadTakesGlobal(t *testing.T) {
	loadout := newLoadoutCmd()
	rootCmd.AddCommand(loadout)
	t.Cleanup(func() { rootCmd.RemoveCommand(loadout) })

	scopedReads := map[string]bool{
		"list": true, "tags": true, "show": true, "plan list": true,
	}
	exempt := map[string]string{
		"add":              "mutation: writes exactly one project's store",
		"edit":             "mutation: writes exactly one project's store",
		"status":           "mutation: writes exactly one project's store",
		"tag":              "mutation: writes exactly one project's store",
		"run":              "launches a task of the current project into a session",
		"summary":          "per-status counts of one store; a cross-project summary has no defined shape",
		"lint":             "checks one store file",
		"repair":           "rewrites one store file",
		"watch":            "follows one store file",
		"statuses":         "the status taxonomy: no store is read",
		"plan show":        "addressed by path, not by project",
		"mcp":              "a server; its tools carry their own scope parameters",
		"manage install":   "engine configuration, not a task read",
		"manage uninstall": "engine configuration, not a task read",
		"manage check":     "engine configuration, not a task read",
		"version":          "no store is read",
		"loadout":          "no store is read",
	}

	seen := map[string]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if !c.Runnable() || c == rootCmd {
			return
		}
		name := strings.TrimPrefix(c.CommandPath(), rootCmd.Name()+" ")
		// cobra adds help and completion to the tree on the first Execute,
		// so whether they are present depends on which test ran first; they
		// read no store either way.
		if name == "help" || strings.HasPrefix(name, "completion") {
			return
		}
		seen[name] = true
		if scopedReads[name] {
			f := c.LocalFlags().Lookup("global")
			if assert.NotNil(t, f, "%q is a project-scoped read but takes no --global", name) {
				assert.Equal(t, "bool", f.Value.Type(), "%q --global must be a plain switch", name)
				assert.Equal(t, "false", f.DefValue, "%q must default to the resolved project", name)
			}
			return
		}
		_, ok := exempt[name]
		assert.True(t, ok, "%q is unclassified: either it is a project-scoped read (add --global and list it in scopedReads) or name why scope does not apply in exempt", name)
	}
	walk(rootCmd)

	for name := range scopedReads {
		require.True(t, seen[name], "scopedReads names %q, which is not a runnable command", name)
	}
	for name := range exempt {
		require.True(t, seen[name], "exempt names %q, which is not a runnable command", name)
	}
}
