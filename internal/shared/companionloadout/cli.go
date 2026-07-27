// Package companionloadout is the shared `loadout` subcommand every in-repo
// companion binary (cmd/ltk, cmd/taskloom) wires in identically: print the
// companion's own ctxloom loadout (signature-envelope spec §4.3), either as
// raw bundle YAML or as the JSON envelope ctxloom's companion discovery
// execs (`<bin> loadout --format json`).
//
// Only the DISPATCH logic lives here. The loadout content itself stays
// per-binary: go:embed can only embed a file that lives in the embedding
// file's own package directory, so each companion embeds its own
// loadout.yaml and loadout.yaml.sig (via the `loadout.yaml*` wildcard —
// ReadEmbeddedSig below) and hands the resulting bytes to NewCommand.
package companionloadout

import (
	"embed"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/signing"
)

// NewCommand builds the `loadout` cobra command for a companion binary.
//
// binName is used only in help text. bundleYAML is the companion's own
// embedded (via the go embed directive) loadout bundle bytes. sig is an
// OPTIONAL detached publish signature over bundleYAML (namespace
// signing.NamespacePublish), embedded the same way at release build time;
// nil/empty emits unsigned — legal,
// ordinary, and routes to ctxloom's review path rather than an error (spec
// §10.1). No release signing pipeline exists yet, so every in-repo companion
// passes nil today; this seam is what a future signed build changes through
// without touching this function.
// U107-F02: this trio is a cross-process wire contract — ctxloom's own
// probe (internal/config/companions.go) execs a companion binary as
// `<bin> Subcommand --FormatFlag FormatJSON` — previously duplicated as bare
// string literals on BOTH sides with no shared constant and no test
// exercising both real sides together. Because a broken probe took a silent
// bare-return path (see companions.go's own U047-F04 fix for the OTHER half
// of this finding), renaming any of these three strings on either side alone
// used to pass the entire test suite while silently removing all companion
// contribution in production. Exporting them here and having the consumer
// build its argv from them makes a one-sided rename a compile error instead.
const (
	// Subcommand is the loadout probe's cobra command name.
	Subcommand = "loadout"
	// FormatFlag is the flag name selecting the output format.
	FormatFlag = "format"
	// FormatJSON is the FormatFlag value naming the machine-readable
	// signed-envelope format ctxloom's probe execs.
	FormatJSON = "json"
)

func NewCommand(binName string, bundleYAML, sig []byte) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   Subcommand,
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
	cmd.Flags().StringVar(&format, FormatFlag, "yaml", "output format: yaml (raw bundle) or json (signed envelope)")
	return cmd
}

// ReadEmbeddedSig reads the OPTIONAL loadout.yaml.sig sibling out of an
// embedded FS built from a companion's own `//go:embed loadout.yaml*`
// wildcard, degrading to nil (never an error) when it is absent — the
// ordinary, pre-signing state (spec §10.1). The wildcard pattern, rather
// than a literal `//go:embed loadout.yaml.sig`, is what keeps a companion's
// build from hard-failing when no .sig has been committed yet: a literal
// directive requires the named file to exist at compile time, but the .sig
// is meant to stay optional forever.
func ReadEmbeddedSig(fs embed.FS) []byte {
	data, err := fs.ReadFile("loadout.yaml.sig")
	if err != nil {
		return nil
	}
	return data
}

// Emit is the pure core NewCommand's RunE drives: deterministic, no network,
// no filesystem access beyond the bytes already in hand. Exported so each
// companion's own tests can drive it directly without going through cobra.
func Emit(w io.Writer, format string, bundleYAML, sig []byte) error {
	// U107-F01: a companion binary embedding zero bytes (a build mistake:
	// forgot the go:embed directive, wrong glob, empty loadout.yaml) used
	// to emit a well-formed envelope carrying nothing, in BOTH formats —
	// and every downstream stage (ctxloom's discovery decode, ParseBundle,
	// probe) also succeeded, so "companion present but contributing
	// nothing" was byte-for-byte indistinguishable from a healthy
	// companion. Fail loud here, at the emitter, where binName is known
	// and where the companion's OWN tests catch it — not just at
	// ctxloom's consumer side.
	if len(bundleYAML) == 0 {
		return fmt.Errorf("empty loadout bundle: this companion has no content to contribute (embedded loadout.yaml is empty or missing)")
	}
	switch format {
	case "yaml":
		_, err := w.Write(bundleYAML)
		return err
	case FormatJSON:
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
