package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

var (
	bundleMoveTo      string
	bundleMoveForce   bool
	bundleMoveMessage string
)

var bundleMoveCmd = &cobra.Command{
	Use:   "move <name> --to <remote|path>",
	Short: "Move an authored bundle to a remote or another project, carrying its signature",
	Long: `Move an authored bundle out of .ctxloom/content/bundles, signature and all.

The bundle's bytes are carried VERBATIM — never re-parsed, re-serialized, or
re-signed. A detached publisher signature (<name>.yaml.sig, made by
'ctxloom sign') therefore stays valid at the destination, because the bytes it
covers do not change. If a signature exists but cannot be carried, the move
FAILS: it never lands the bundle unsigned behind your back.

--to resolves one of two ways, and never guesses:

  remote name   A remote configured in .ctxloom/remotes.yaml (see
                'ctxloom remote list'). The bundle is published to it — the same
                path 'ctxloom bundle push' takes — with its signature published
                alongside as a detached sibling.

  directory     An existing local directory. If it is a ctxloom project checkout
                (it contains .ctxloom/), the bundle lands in that project's
                .ctxloom/content/bundles/; otherwise it lands in the directory
                itself. --force overwrites a bundle of the same name there.

A configured remote NAME always wins over a directory of the same spelling. An
argument that is neither a known remote nor an existing directory is an error.

The source is removed ONLY after the destination write has fully succeeded
(bundle AND signature). A move that fails part-way leaves the source untouched.

Examples:
  ctxloom bundle move go-tools --to ctxloom-default
  ctxloom bundle move go-tools --to ../other-project
  ctxloom bundle move go-tools --to ../shared/bundles --force`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleMove,
}

func runBundleMove(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	result, err := operations.MoveBundle(cmd.Context(), cfg, operations.MoveBundleRequest{
		Name:    args[0],
		To:      bundleMoveTo,
		Force:   bundleMoveForce,
		Message: bundleMoveMessage,
		// A move to a remote is a publish, and it DELETES the local source
		// afterwards — so the "which remote am I sending signed content to"
		// question matters here at least as much as on push.
		ConfirmRemote: publishRemoteAsk(cmd),
	})
	if err != nil {
		return err
	}

	return emit(cmd, result, func() error { return printMoveResult(cmd.OutOrStdout(), result) })
}

// printMoveResult renders a move outcome for humans.
func printMoveResult(w io.Writer, r *operations.MoveBundleResult) error {
	dest := r.Dest
	if r.DestKind == operations.MoveDestRemote {
		dest = fmt.Sprintf("%s (%s)", r.Dest, r.Remote)
	}
	if _, err := fmt.Fprintf(w, "Moved %s -> %s\n", r.Source, dest); err != nil {
		return err
	}
	if r.CommitSHA != "" {
		if _, err := fmt.Fprintf(w, "Commit: %s\n", gitutil.ShortSHA(r.CommitSHA)); err != nil {
			return err
		}
	}
	if r.SigDest != "" {
		if _, err := fmt.Fprintf(w, "Signature: %s\n", r.SigDest); err != nil {
			return err
		}
	}
	return nil
}

// registerBundleMoveFlags defines `bundle move`'s flags. --to is REQUIRED: a
// move with no destination has nowhere to go, and the requirement is an
// annotation on the flag, so it belongs with the flag.
func registerBundleMoveFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&bundleMoveTo, "to", "", "Destination: a configured remote name, or a local directory / ctxloom project checkout (a remote name wins)")
	cmd.Flags().BoolVarP(&bundleMoveForce, "force", "f", false, "Overwrite an existing bundle at a local destination")
	cmd.Flags().StringVarP(&bundleMoveMessage, "message", "m", "", "Commit message (remote destination only)")
	_ = cmd.MarkFlagRequired("to")
}
