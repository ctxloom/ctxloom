package cli

import (
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/companions/loadout"
	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// EmbeddedLoadout is ctxloom's own loadout as the composition root hands it
// to the CLI: the loadout document bytes and the OPTIONAL detached publish
// signature over them, both embedded beside cmd/ctxloom's main.
//
// ctxloom is its own companion. Everything it delivers into an engine on its
// own behalf — its MCP server entry, its always-on guidance — is declared in
// that loadout and reaches a session by the same probe, reader and gate every
// companion's content does (companions.Prober's self-probe), so a profile can
// exclude it and a human can reject it like any other companion content.
//
// The CLI owns the COMMAND (so the documented tree carries `loadout` without
// a composition) and the composition root owns the CONTENT: go:embed cannot
// reach outside the embedding package, so the bytes live in cmd/ctxloom and
// arrive here through Composition.
type EmbeddedLoadout struct {
	YAML []byte
	Sig  []byte
}

func init() {
	cmd := loadout.NewDeferredCommand(agent.CtxloomBinary, func() ([]byte, []byte) {
		return theComposition.Loadout.YAML, theComposition.Loadout.Sig
	})
	// The command carries its OWN --format (yaml|json, the envelope formats)
	// that shadows the root's persistent one, and renders through it on
	// every successful run — so the root's honour-guard (checkFormatWasHonored)
	// is satisfied by construction here rather than by emit(). The companion
	// package cannot mark it itself: the guard is this tree's.
	emitThrough := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if err := emitThrough(c, args); err != nil {
			return err
		}
		formatWasHonored = true
		return nil
	}
	rootCmd.AddCommand(cmd)
}
