package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/signing/agentkey"
)

// signKeyFlag backs `ctxloom sign --key <path|fingerprint>` — an explicit
// override that wins over both git config user.signingkey and ssh-agent
// auto-detection (spec §7A.4).
var (
	signKeyFlag string
	signAllFlag bool
)

var signCmd = &cobra.Command{
	Use:   "sign [ref]",
	Short: "Sign a local bundle for publication",
	Long: `Sign a local bundle file, writing a detached <bundle>.yaml.sig sibling that
lets anyone who trusts your key verify the bundle came from you (signature-
envelope spec §3.1, §4.2).

ref is a bundle ref or an item ref — the same grammar 'ctxloom trust' uses.
A publisher signature covers the whole bundle FILE, so an item ref
("<bundle>#fragments/<name>") resolves to its CONTAINING bundle and signs
that; ctxloom sign says so.

Key discovery is zero-config: it tries 'git config user.signingkey' first
(anyone who already signs commits with SSH needs no ctxloom setup at all),
then the sole identity in ssh-agent when there is exactly one. --key (or
'ctxloom manage config set sign.key') overrides both. ctxloom never reads,
generates, or stores private key material — every signature is produced by
your existing ssh-agent.

Examples:
  ctxloom sign my-tools                          # bare = local bundle (the common case)
  ctxloom sign my-tools#fragments/go-testing      # resolves to bundle my-tools
  ctxloom sign --all                              # every local bundle this project publishes
  ctxloom sign my-tools --key ~/.ssh/id_ed25519.pub`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		ref := ""
		if len(args) > 0 {
			ref = args[0]
		}
		return runSign(cmd, cfg, agentkey.NewDiscoverer(), ref, signAllFlag, signKeyFlag)
	},
}

// runSign is the testable body of `ctxloom sign`: cfg and discoverer are
// both DI seams (a real config.Config over a temp project, and a fake
// agentkey.Discoverer wired to fake git-config/ssh-agent responses, mirror
// internal/signing/agentkey's own tests) so this composition — resolve key,
// resolve target(s), sign, report — is exercisable without a real ssh-agent
// or git binary.
func runSign(cmd *cobra.Command, cfg *config.Config, discoverer *agentkey.Discoverer, ref string, all bool, keyFlag string) error {
	if all && ref != "" {
		return fmt.Errorf("ctxloom sign: --all cannot be combined with a ref")
	}
	if !all && ref == "" {
		return fmt.Errorf("ctxloom sign: a ref is required (or pass --all)")
	}

	explicit := keyFlag
	if explicit == "" && cfg != nil {
		explicit = cfg.SignKey()
	}
	discovered, err := discoverer.Discover(cmd.Context(), explicit)
	if err != nil {
		return err
	}

	targets, err := resolveSignTargets(cfg, ref, all)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no local bundles to sign")
		return nil
	}

	for _, target := range targets {
		res, err := operations.SignBundleFile(cfg, operations.SignBundleRequest{
			Target: target,
			Signer: discovered.Signer,
		})
		if err != nil {
			return err
		}
		printSignResult(cmd.OutOrStdout(), res, discovered)
	}
	return nil
}

// resolveSignTargets expands ref/--all into the SignTarget list to sign,
// reusing operations.ResolveSignTarget (which itself reuses the SAME ref
// grammar 'ctxloom trust' uses — no second grammar, ADR 0032).
func resolveSignTargets(cfg *config.Config, ref string, all bool) ([]operations.SignTarget, error) {
	if all {
		var targets []operations.SignTarget
		for _, name := range operations.ListLocalBundleNames(cfg, nil) {
			targets = append(targets, operations.SignTarget{BundleName: name})
		}
		return targets, nil
	}
	target, err := operations.ResolveSignTarget(ref)
	if err != nil {
		return nil, err
	}
	return []operations.SignTarget{target}, nil
}

// printSignResult renders one sign outcome (spec §7A.1 example format).
func printSignResult(w io.Writer, res *operations.SignBundleResult, d *agentkey.Discovered) {
	if res.ItemNote != "" {
		fmt.Fprintf(w, "Signing bundle %s (contains %s) — signatures cover whole bundles.\n", res.BundleName, res.ItemNote)
	}
	fmt.Fprintf(w, "  %s  ->  %s\n", res.BundlePath, res.SigPath)
	fmt.Fprintf(w, "  signed by %s (%s)\n", d.Source, d.Fingerprint)
}

func init() {
	rootCmd.AddCommand(signCmd)
	signCmd.Flags().BoolVar(&signAllFlag, "all", false, "sign every local bundle this project publishes")
	signCmd.Flags().StringVar(&signKeyFlag, "key", "", "explicit signing key: a path to a public key, or a SHA256:... ssh-agent fingerprint")
}
