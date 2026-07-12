// Package companionloadout is the shared `loadout` subcommand every in-repo
// companion binary (cmd/ltk, cmd/taskloom) wires in identically: print the
// companion's own ctxloom loadout (signature-envelope spec §4.3), either as
// raw bundle YAML or as the JSON envelope ctxloom's companion discovery
// execs (`<bin> loadout --format json`).
//
// Only the DISPATCH logic lives here. The loadout content itself stays
// per-binary: go:embed can only embed a file that lives in the embedding
// file's own package directory, so each companion embeds its own
// loadout.yaml (and, once a release signing pipeline exists, its own
// loadout.yaml.sig) and hands the resulting bytes to NewCommand.
package companionloadout

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/signing"
)

// NewCommand builds the `loadout` cobra command for a companion binary.
//
// binName is used only in help text. bundleYAML is the companion's own
// go:embed'd loadout bundle bytes. sig is an OPTIONAL detached publish
// signature over bundleYAML (namespace signing.NamespacePublish), embedded
// the same way at release build time; nil/empty emits unsigned — legal,
// ordinary, and routes to ctxloom's review path rather than an error (spec
// §10.1). No release signing pipeline exists yet, so every in-repo companion
// passes nil today; this seam is what a future signed build changes through
// without touching this function.
func NewCommand(binName string, bundleYAML, sig []byte) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "loadout",
		Short: fmt.Sprintf("Print %s's ctxloom loadout — the bundle content %s contributes to a session", binName, binName),
		Long: fmt.Sprintf(`loadout emits the ctxloom bundle %s contributes to a session, for ctxloom's
companion discovery to seed into its trust gate under the source ref
ctxloom:companion@%s (signature-envelope spec §4.3, §6).

--format json is the machine contract ctxloom's companion discovery execs
(`+"`%s loadout --format json`"+`): a JSON envelope carrying the exact bundle YAML
bytes (base64) plus an OPTIONAL detached publish signature.

--format yaml (the default) prints the raw bundle YAML for a human to read.`, binName, binName, binName),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return Emit(cmd.OutOrStdout(), format, bundleYAML, sig)
		},
	}
	cmd.Flags().StringVar(&format, "format", "yaml", "output format: yaml (raw bundle) or json (signed envelope)")
	return cmd
}

// Emit is the pure core NewCommand's RunE drives: deterministic, no network,
// no filesystem access beyond the bytes already in hand. Exported so each
// companion's own tests can drive it directly without going through cobra.
func Emit(w io.Writer, format string, bundleYAML, sig []byte) error {
	switch format {
	case "yaml":
		_, err := w.Write(bundleYAML)
		return err
	case "json":
		env, err := signing.EncodeLoadoutEnvelope(bundleYAML, sig, "")
		if err != nil {
			return fmt.Errorf("encode loadout envelope: %w", err)
		}
		if _, err := w.Write(env); err != nil {
			return err
		}
		_, err = fmt.Fprintln(w)
		return err
	default:
		return fmt.Errorf("unknown format %q (supported: yaml, json)", format)
	}
}
