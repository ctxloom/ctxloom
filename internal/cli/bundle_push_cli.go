package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
	"github.com/ctxloom/ctxloom/internal/signing/agentkey"
)

// The `bundle push` / `command push` frontend: resolve the bundle, resolve the
// target remote, resolve whether to sign, publish, render. Signing rides the
// producing action (spec §7A.3), and key discovery happens before any network
// call so a signing failure can never degrade to a silent unsigned publish.

// pushBundle publishes the named bundle to a remote. Each step is an operations
// call — resolve the bundle path, resolve the target remote (an explicit
// override or inferred from the bundle's location), resolve whether/how to
// sign, then publish — so the CLI re-implements none of the push logic and
// the same path is reachable by any frontend.
//
// sign/noSign are the --sign/--no-sign flags (spec §7A.3): signing rides the
// producing action the way `git commit -S` does. sign.default in config
// makes every push sign unless --no-sign overrides it for one invocation;
// an explicit --sign always signs. Key discovery, and any failure to find a
// key, happens BEFORE any network call — a signing failure must never
// degrade to a silent unsigned publish (spec §7A.4, normative).
func pushBundle(cmd *cobra.Command, bundleName, remoteOverride string, createPR bool, message string, sign, noSign bool) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	return pushBundleCfg(cmd, cfg, agentkey.NewDiscoverer(), nil, bundleName, remoteOverride, createPR, message, sign, noSign)
}

// pushBundleCfg is the testable body of pushBundle: cfg, discoverer, and mgr
// are all DI'd (a real config.Config over a temp project, a fake
// agentkey.Discoverer — mirroring internal/cli/sign.go's runSign — and an
// optional PublishManager backed by a mock Publisher) so the
// --sign/--no-sign/sign.default composition is exercisable without a real
// config.Load(), git binary, ssh-agent, or network call. mgr==nil uses
// PushBundle's own default (a real, network-backed manager) — production's
// path.
func pushBundleCfg(cmd *cobra.Command, cfg *config.Config, discoverer *agentkey.Discoverer, mgr *remote.PublishManager, bundleName, remoteOverride string, createPR bool, message string, sign, noSign bool) error {
	if sign && noSign {
		return fmt.Errorf("--sign and --no-sign are mutually exclusive")
	}

	bundle, err := operations.GetBundle(cfg, bundleName)
	if err != nil {
		return fmt.Errorf("load bundle %q: %w", bundleName, err)
	}

	remoteName, err := operations.ResolveBundleRemote(cfg, bundle.Path, remoteOverride)
	if err != nil {
		return err
	}

	req := operations.PushBundleRequest{
		Path:           bundle.Path,
		Remote:         remoteName,
		Message:        message,
		CreatePR:       createPR,
		PublishManager: mgr,
	}
	if sign || (cfg.ShouldSignByDefault() && !noSign) {
		discovered, err := discoverer.Discover(cmd.Context(), cfg.SignKey())
		if err != nil {
			return err
		}
		defer func() { _ = discovered.Close() }()
		req.Signer = discovered.Signer
	}

	result, err := operations.PushBundle(cmd.Context(), cfg, req)
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
	if r.Signed {
		if _, err := fmt.Fprintln(w, "Signed: yes"); err != nil {
			return err
		}
	}
	return nil
}
