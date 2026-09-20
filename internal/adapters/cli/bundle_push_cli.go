package cli

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/agentkey"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

// The `bundle push` / `command push` frontend: resolve the bundle, resolve the
// target remote, resolve which signature travels with it, publish, render.
//
// A SIGNATURE BELONGS TO THE BUNDLE, NOT TO THE PUBLISH. `ctxloom bundle sign`
// is the only thing that produces one; every publishing path CARRIES the
// sidecar it left on disk and refuses a stale one. That is what lets the
// signing key stay off the publishing machine entirely — you sign locally and
// CI ships signed content it could not itself forge — and it is why what you
// can verify at rest is exactly what shipped. `bundle move --to <remote>` has
// always worked this way; push now does too.

// pushBundle publishes the named bundle to a remote. Each step is an operations
// call — resolve the bundle path, resolve the target remote (an explicit
// override or inferred from the bundle's location), resolve which signature
// travels, then publish — so the CLI re-implements none of the push logic and
// the same path is reachable by any frontend.
//
// sign/noSign are the --sign/--no-sign flags (spec §7A.3):
//   - neither: publish the tree as it stands — its SHA256SUMS manifest and
//     .sigs/ entries travel with it; a stale manifest is refused.
//   - --sign (or sign.default, unless --no-sign): SUGAR for sign-then-publish.
//     It runs the same signing operation `ctxloom bundle sign` runs, so the
//     manifest entry that travels is the one `bundle sign` writes. The
//     one-command path survives without signing becoming a property of the
//     push.
//   - --no-sign: skip the signing sugar. Unsigned publishing is not an
//     oversight — third-party unsigned remotes default to pending, which is
//     what gives the trust gate a pending state and `ctxloom review` a
//     purpose.
//
// Key discovery, and any failure to find a key, happens BEFORE any network
// call — a signing failure must never degrade to a silent unsigned publish
// (spec §7A.4, normative).
func pushBundle(cmd *cobra.Command, bundleName, remoteOverride string, createPR bool, message string, sign, noSign bool) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	discoverer, err := operations.SignerDiscoverer()
	if err != nil {
		return err
	}
	return pushBundleCfg(cmd, cfg, discoverer, nil, bundleName, remoteOverride, createPR, message, sign, noSign)
}

// pushBundleCfg is the testable body of pushBundle: cfg, discoverer, and mgr
// are all DI'd (a real config.Config over a temp project, a fake
// agentkey.Discoverer — mirroring internal/adapters/cli/sign.go's runSign — and an
// optional PublishManager backed by a mock Publisher) so the
// --sign/--no-sign/sign.default composition is exercisable without a real
// config read, git binary, ssh-agent, or network call. mgr==nil uses
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
		// The human who confirms a remote nothing has been published to
		// before — nil unless there is a terminal, which is what makes an
		// agent or CI invocation refuse instead of prompt.
	}
	if err := resolvePushSignature(cmd, cfg, discoverer, bundleName, bundle.Path, sign, noSign); err != nil {
		return err
	}

	result, err := operations.PushBundle(cmd.Context(), cfg, req)
	if err != nil {
		return err
	}

	return emit(cmd, result, func() error { return printPushResult(cmd.OutOrStdout(), result) })
}

// resolvePushSignature is the only place the three inputs (--sign, --no-sign,
// sign.default) meet: it signs the bundle on disk first when asked, and
// otherwise leaves the tree to publish as it stands. An error rather than a
// quiet unsigned publish for anything it could not resolve.
func resolvePushSignature(cmd *cobra.Command, cfg *config.Config, discoverer *agentkey.Discoverer, bundleName, bundlePath string, sign, noSign bool) error {
	if noSign {
		return nil
	}
	if sign || cfg.ShouldSignByDefault() {
		return mintPushSignature(cmd, cfg, discoverer, bundleName, bundlePath)
	}
	return nil
}

// mintPushSignature is the `--sign` / sign.default SUGAR: it runs exactly the
// operation `ctxloom bundle sign <bundle>` runs, so the manifest entry left on
// disk is the same artifact by the same producer, and the push that follows
// merely carries it with the rest of the tree.
//
// The signature PERSISTS rather than being minted in flight. That is the
// whole point of "sign is the only producer": `push --sign` and `bundle sign
// && bundle push` end in the same state, and you can verify at rest exactly
// what you shipped.
//
// It always RE-SIGNS, even when a valid entry is already there: --sign is an
// explicit instruction to sign, and the key it resolves (--key/sign.key/git
// config/ssh-agent) may not be the one that produced the old entry.
//
// Discovery happens BEFORE any network call, so a missing key fails the whole
// command rather than degrading to an unsigned publish (spec §7A.4).
func mintPushSignature(cmd *cobra.Command, cfg *config.Config, discoverer *agentkey.Discoverer, bundleName, bundlePath string) error {
	discovered, err := discoverer.Discover(cmd.Context(), cfg.SignKey())
	if err != nil {
		return err
	}
	defer func() { _ = discovered.Close() }()

	res, err := operations.SignBundleFile(cfg, operations.SignBundleRequest{
		Target:       operations.SignTarget{BundleName: bundleName},
		Signer:       discovered.Signer,
		SignerSource: discovered.Source,
	})
	if err != nil {
		return err
	}
	// push resolves the bundle through the SEEDED loader (it can address a
	// pinned remote bundle) and sign resolves it through the authored store.
	// For anything actually signable the two agree; if they ever did not, the
	// signature would cover a different tree than the one being published, so
	// say so rather than publish what nobody can verify.
	if filepath.Clean(res.BundlePath) != filepath.Clean(bundlePath) {
		return fmt.Errorf("refusing to sign %s while publishing %s: signing resolved bundle %q to a different file",
			res.BundlePath, bundlePath, bundleName)
	}
	return nil
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
