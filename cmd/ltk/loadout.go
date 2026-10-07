package main

import (
	_ "embed"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/companions/loadout"
)

// loadoutYAML is ltk's own ctxloom loadout — the bundle content ltk
// contributes to a ctxloom session. It is the
// single source of truth for what ltk tells ctxloom about itself — ctxloom
// discovers this binary on PATH and execs `ltk loadout --format yaml` rather
// than vendoring this content.
//
//go:embed loadout.yaml
var loadoutYAML []byte

func newLoadoutCmd() *cobra.Command {
	return loadout.NewCommand(progName, loadoutYAML)
}
