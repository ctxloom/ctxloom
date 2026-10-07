package main

import (
	_ "embed"

	"github.com/ctxloom/ctxloom/internal/adapters/cli"
)

// loadoutYAML is ctxloom's own loadout — what ctxloom delivers into an engine
// on its own behalf: its MCP server entry and
// its always-on guidance. ctxloom is its own companion: this content reaches
// a session through the same self-probe and reader as every other
// companion's (see cli.EmbeddedLoadout), so a profile can exclude it.
//
//go:embed loadout.yaml
var loadoutYAML []byte

// embeddedLoadout is what the composition root hands the CLI's `loadout`
// command.
func embeddedLoadout() cli.EmbeddedLoadout {
	return cli.EmbeddedLoadout{YAML: loadoutYAML}
}
