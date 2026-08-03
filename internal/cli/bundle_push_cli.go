package cli

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
	"github.com/ctxloom/ctxloom/internal/signing/agentkey"
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
//   - neither: carry whatever valid sidecar is on disk; refuse a stale one;
//     publish bare when the bundle was never signed.
//   - --sign (or sign.default, unless --no-sign): SUGAR for sign-then-publish.
//     It runs the same signing operation `ctxloom bundle sign` runs, leaving
//     the same `<bundle>.yaml.sig` on disk, and then carries it. The
//     one-command path survives without signing becoming a property of the
//     push.
//   - --no-sign: publish BARE, even when a valid sidecar exists. Unsigned
//     publishing is not an oversight — third-party unsigned remotes default to
//     pending, which is what gives operations.EffectiveTrust a pending state
//     and `ctxloom review` a purpose.
//
// Key discovery, and any failure to find a key, happens BEFORE any network
// call — a signing failure must never degrade to a silent unsigned publish
// (spec §7A.4, normative).
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
	req.Signature, err = resolvePushSignature(cmd, cfg, discoverer, bundleName, bundle.Path, sign, noSign)
	if err != nil {
		return err
	}

	result, err := operations.PushBundle(cmd.Context(), cfg, req)
	if err != nil {
		return err
	}

	return emit(cmd, result, func() error { return printPushResult(cmd.OutOrStdout(), result) })
}

// resolvePushSignature decides WHICH signature travels with this publish, and
// is the only place the three inputs (--sign, --no-sign, sign.default) meet.
// It returns nil for "publish bare" — which is a legitimate, supported outcome,
// not a failure — and an error rather than nil for anything it could not
// resolve, so a signing problem can never degrade into a quiet unsigned
// publish.
func resolvePushSignature(cmd *cobra.Command, cfg *config.Config, discoverer *agentkey.Discoverer, bundleName, bundlePath string, sign, noSign bool) ([]byte, error) {
	if noSign {
		return nil, nil
	}
	if sign || cfg.ShouldSignByDefault() {
		if err := mintPushSignature(cmd, cfg, discoverer, bundleName, bundlePath); err != nil {
			return nil, err
		}
	}
	// One seam, shared with `bundle move` and `bundle export`: read the sidecar
	// and prove it covers the bytes about to be published, or refuse.
	return operations.PublisherSignature(nil, bundlePath, nil)
}

// mintPushSignature is the `--sign` / sign.default SUGAR: it runs exactly the
// operation `ctxloom bundle sign <bundle>` runs, so the sidecar left on disk is
// the same artifact by the same producer, and the push that follows merely
// carries it.
//
// The sidecar PERSISTS rather than being minted in flight. That is the whole
// point of "sign is the only producer": a transient signature would mean two
// producers writing to two different places, and would leave the local tree
// claiming the bundle is unsigned (or worse, still holding a STALE sidecar)
// while the remote holds a valid one. Persisting means `push --sign` and
// `bundle sign && bundle push` end in the same state, and that you can verify
// at rest exactly what you shipped. The write goes to the author's own
// `.ctxloom/content/bundles/<name>.yaml.sig` — the path `bundle sign` owns —
// and never anywhere else.
//
// It always RE-SIGNS, even when a valid sidecar is already there: --sign is an
// explicit instruction to sign, and the key it resolves (--key/sign.key/git
// config/ssh-agent) may not be the one that produced the old sidecar. Carrying
// somebody else's signature in response to "sign this" would be the surprising
// reading.
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
		Target: operations.SignTarget{BundleName: bundleName},
		Signer: discovered.Signer,
	})
	if err != nil {
		return err
	}
	// push resolves the bundle through the SEEDED loader (it can address a
	// pinned remote bundle) and sign resolves it through the authored store.
	// For anything actually signable the two agree; if they ever did not, the
	// sidecar would cover a different file than the one being published, so
	// say so rather than publish a pair nobody can verify.
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
