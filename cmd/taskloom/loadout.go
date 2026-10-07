package main

import (
	_ "embed"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/companions/loadout"
)

// loadoutYAML is taskloom's own ctxloom loadout — the content taskloom
// contributes to a ctxloom session. It is the
// single source of truth for what taskloom tells ctxloom about itself —
// ctxloom discovers this binary on PATH and execs
// `taskloom loadout --format yaml` rather than vendoring this content.
//
//go:embed loadout.yaml
var loadoutYAML []byte

// newLoadoutCmd is a factory (rather than a package-level *cobra.Command
// wired via this file's own init() convention) so registration has no
// hidden ordering dependency; main.go adds it explicitly.
func newLoadoutCmd() *cobra.Command {
	return loadout.NewCommand(progName, loadoutYAML)
}
