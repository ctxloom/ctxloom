// Package loadout is the companion SIDE of the companion contract: the shared
// `loadout` subcommand every in-repo companion binary wires in identically —
// print the companion's own ctxloom loadout document, the bytes ctxloom's
// companion discovery execs and parses (`<bin> loadout --format yaml`).
//
// docs/companion-loadout-standard.md is the contract this implements, stated
// once: what a companion emits, how ctxloom asks for it, what happens when the
// probe fails, and how a loadout fragment declares a premise.
//
// Only the DISPATCH logic lives here. The loadout content itself stays
// per-binary: go:embed can only embed a file that lives in the embedding
// file's own package directory, so each companion embeds its own
// loadout.yaml and hands the resulting bytes to NewCommand.
package loadout

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// This trio is a cross-process wire contract — ctxloom's own probe
// (companions.loadoutArgs) execs a companion binary as
// `<bin> Subcommand --FormatFlag FormatYAML`. Exporting them here and having
// the consumer build its argv from them makes a one-sided rename a compile
// error instead of a silent loss of every companion's contribution.
const (
	// Subcommand is the loadout probe's cobra command name.
	Subcommand = "loadout"
	// FormatFlag is the flag name selecting the output format.
	FormatFlag = "format"
	// FormatYAML is the FormatFlag value naming the loadout document itself,
	// the only format there is.
	FormatYAML = "yaml"
)

// NewCommand builds the `loadout` cobra command for a companion binary.
//
// binName is used only in help text. loadoutYAML is the companion's own
// embedded (via the go embed directive) loadout document bytes.
func NewCommand(binName string, loadoutYAML []byte) *cobra.Command {
	return NewDeferredCommand(binName, func() []byte { return loadoutYAML })
}

// NewDeferredCommand is NewCommand for a binary whose loadout bytes are not in
// hand when the command tree is built: content reads them at RUN time. This
// is ctxloom's own shape — the CLI package owns its command tree (so the
// documented tree carries `loadout` without a composition), while the
// embedded bytes live in the composition root (go:embed cannot reach outside
// the embedding package), which hands them over before dispatch. A companion
// that embeds beside its own main uses NewCommand.
func NewDeferredCommand(binName string, content func() []byte) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   Subcommand,
		Short: fmt.Sprintf("Print the context, commands, hooks and MCP servers %s contributes to a session", binName),
		Long: fmt.Sprintf(`loadout prints the ctxloom loadout %s contributes — a document with the RUN
bundle a session consumes and the typed INIT section setup consumes — for
ctxloom's companion discovery to seed under the source ref
ctxloom:companion@%s.

ctxloom's companion discovery execs `+"`%s loadout --format yaml`"+` and parses
the document it prints.`, binName, binName, binName),
		Example: fmt.Sprintf("  %[1]s %[2]s", binName, Subcommand),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return Emit(cmd.OutOrStdout(), resolveFormat(cmd, format), content())
		},
	}
	cmd.Flags().StringVar(&format, FormatFlag, FormatYAML, "output format: yaml (the loadout document)")
	return cmd
}

// resolveFormat picks the format loadout emits, honoring a host CLI's
// --json shorthand.
//
// This command carries its OWN --format, narrower than the host root's
// persistent one (yaml only) — so the host's --json, which every other command
// on the tree treats as pure shorthand for --format json, does not reach the
// local variable. A flag a command accepts and ignores is worse than one it
// rejects: --json is passed through as "json", which Emit refuses.
//
// An explicitly given --format is the more specific request and wins. That
// differs from cliemit.Resolve, where --json is unconditional; a companion
// whose local vocabulary is a SUBSET must be able to say which member of it
// it wants without a shorthand for a sibling format overriding it. When the
// host declares no --json at all (ltk, or NewCommand driven without a root)
// the lookup is nil and nothing changes.
func resolveFormat(cmd *cobra.Command, local string) string {
	if cmd.Flags().Changed(FormatFlag) {
		return local
	}
	if f := cmd.Flags().Lookup("json"); f != nil && f.Changed {
		return "json"
	}
	return local
}

// Emit is the pure core NewDeferredCommand's RunE drives: deterministic, no network,
// no filesystem access beyond the bytes already in hand. Exported so each
// companion's own tests can drive it directly without going through cobra.
func Emit(w io.Writer, format string, loadoutYAML []byte) error {
	// A companion binary embedding zero bytes (a build mistake: forgot the
	// go:embed directive, wrong glob, empty loadout.yaml) would otherwise emit
	// nothing that every downstream stage also accepts, so "companion present
	// but contributing nothing" would be indistinguishable from a healthy
	// companion. Fail loud here, at the emitter, where the companion's OWN
	// tests catch it.
	if len(loadoutYAML) == 0 {
		return fmt.Errorf("empty loadout: this companion has no content to contribute (embedded loadout.yaml is empty or missing)")
	}
	if format != FormatYAML {
		return fmt.Errorf("unknown format %q (supported: %s)", format, FormatYAML)
	}
	_, err := w.Write(loadoutYAML)
	return err
}
