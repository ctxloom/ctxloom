package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

// The `bundle push` / `command push` frontend: resolve the bundle, resolve the
// target remote, publish, render.

// pushBundle publishes the named bundle to a remote. Each step is an operations
// call — resolve the bundle path, resolve the target remote (an explicit
// override or inferred from the bundle's location), then publish — so the CLI
// re-implements none of the push logic and the same path is reachable by any
// frontend.
func pushBundle(cmd *cobra.Command, bundleName, remoteOverride string, createPR bool, message string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	return pushBundleCfg(cmd, cfg, nil, bundleName, remoteOverride, createPR, message)
}

// pushBundleCfg is the testable body of pushBundle: cfg and mgr are DI'd (a
// real config.Config over a temp project, and an optional PublishManager
// backed by a mock Publisher). mgr==nil uses PushBundle's own default (a real,
// network-backed manager) — production's path.
func pushBundleCfg(cmd *cobra.Command, cfg *config.Config, mgr *remote.PublishManager, bundleName, remoteOverride string, createPR bool, message string) error {
	bundle, err := operations.GetBundle(cfg, bundleName)
	if err != nil {
		return fmt.Errorf("load bundle %q: %w", bundleName, err)
	}

	remoteName, err := operations.ResolveBundleRemote(cfg, bundle.Path, remoteOverride)
	if err != nil {
		return err
	}

	result, err := operations.PushBundle(cmd.Context(), cfg, operations.PushBundleRequest{
		Path:           bundle.Path,
		Remote:         remoteName,
		Message:        message,
		CreatePR:       createPR,
		PublishManager: mgr,
	})
	if err != nil {
		return err
	}

	return emit(cmd, result, func() error { return printPushResult(cmd.OutOrStdout(), result) })
}

// printPushResult renders a push outcome for humans.
func printPushResult(w io.Writer, r *operations.PushBundleResult) error {
	if r.Status == "pr-created" {
		_, err := fmt.Fprintf(w, "Created pull request: %s\n", r.PRURL)
		return err
	}
	if _, err := fmt.Fprintf(w, "Pushed %s to %s\n", r.TargetPath, r.Remote); err != nil {
		return err
	}
	if r.CommitSHA != "" {
		if _, err := fmt.Fprintf(w, "Commit: %s\n", gitutil.ShortSHA(r.CommitSHA)); err != nil {
			return err
		}
	}
	return nil
}
