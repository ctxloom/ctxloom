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
	"fmt"
	"io"
	"io/fs"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/signing"
)

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

// NewCommand builds the `loadout` cobra command for a companion binary.
//
// binName is used only in help text. bundleYAML is the companion's own
// embedded (via the go embed directive) loadout bundle bytes. sig is an
// OPTIONAL detached publish signature over bundleYAML (namespace
// signing.NamespacePublish), embedded the same way at build time. A NIL sig
// emits unsigned — legal, ordinary, and routes to ctxloom's review path
// rather than an error (spec §10.1); a non-nil but EMPTY one is refused, see
// Emit and ReadEmbeddedSig.
//
// The sig seam is LIVE, not speculative. `just sign-loadouts` is the signing
// pipeline, and both in-repo companions (cmd/ltk, cmd/taskloom) embed a
// committed loadout.yaml.sig that ReadEmbeddedSig hands straight to this
// parameter, so each ships a loadout that verifies against the compiled-in
// ctxloom release key rather than taking the review path. nil stays the
// contract for a companion that has NOT been signed — a third-party binary,
// or an in-repo one before its first `just sign-loadouts` run.
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
			return Emit(cmd.OutOrStdout(), resolveFormat(cmd, format), bundleYAML, sig)
		},
	}
	cmd.Flags().StringVar(&format, FormatFlag, "yaml", "output format: yaml (raw bundle) or json (signed envelope)")
	return cmd
}

// resolveFormat picks the format loadout emits, honoring a host CLI's
// --json shorthand.
//
// This command carries its OWN --format, narrower than the host root's
// persistent one (yaml/json here, and defaulting to yaml because a human
// reading a bundle wants the bundle) — so the host's --json, which every
// other command on the tree treats as pure shorthand for --format json, does
// not reach the local variable. A flag a command accepts and ignores is worse
// than one it rejects: the caller asked for JSON, got YAML, and got exit 0.
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
		return FormatJSON
	}
	return local
}

// ReadEmbeddedSig reads the OPTIONAL loadout.yaml.sig sibling out of an
// embedded FS built from a companion's own `//go:embed loadout.yaml*`
// wildcard, degrading to nil (never an error) when it is absent — the
// ordinary, pre-signing state (spec §10.1). The wildcard pattern, rather
// than a literal `//go:embed loadout.yaml.sig`, is what keeps a companion's
// build from hard-failing when no .sig has been committed yet: a literal
// directive requires the named file to exist at compile time, but the .sig
// is meant to stay optional forever.
//
// ABSENT AND EMPTY ARE DIFFERENT STATES and the return distinguishes them:
// nil means no .sig was committed (intended, and unsigned is legal); a
// non-nil zero-length slice means one WAS committed and is zero bytes, which
// is not a pre-signing state at all but a half-completed signing run. Both
// used to collapse to nil, so a signing run that produced an empty file
// shipped a silently unsigned build indistinguishable from a deliberately
// unsigned one. Emit is what acts on the difference; the explicit []byte{}
// here is what lets it, since a nil return from a zero-byte read is not
// something the caller can rely on the runtime not to produce.
//
// The parameter is fs.ReadFileFS rather than embed.FS purely so the empty
// case is reachable from a test: embed.FS can only be built by the compiler
// from files that exist in the embedding package's own directory, so a
// concrete embed.FS parameter makes "present but empty" untestable without
// committing a decoy loadout.yaml.sig into this package. Every real caller
// still passes an embed.FS, which satisfies this interface.
func ReadEmbeddedSig(files fs.ReadFileFS) []byte {
	data, err := files.ReadFile("loadout.yaml.sig")
	if err != nil {
		return nil
	}
	if len(data) == 0 {
		return []byte{}
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
	// The signature's own half of the same principle. nil sig is the ordinary
	// unsigned build and stays legal; a PRESENT but zero-byte one is a
	// half-completed signing run, and emitting it as merely "unsigned" makes a
	// broken signing pipeline byte-for-byte indistinguishable from a build
	// that was never meant to be signed. Refuse it here, where the companion's
	// own tests see it, rather than shipping a binary whose trust status is an
	// accident.
	if sig != nil && len(sig) == 0 {
		return fmt.Errorf("empty loadout signature: loadout.yaml.sig is present but zero bytes — a half-completed signing run; re-run `just sign-loadouts`, or delete the file to emit unsigned deliberately")
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
