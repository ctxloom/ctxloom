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

// signKeyFlagHelp documents every form --key/sign.key accepts, in the order
// resolveExplicit tries them (internal/signing/agentkey.Discoverer). Shared
// between the flag registration and anywhere else this needs restating.
const signKeyFlagHelp = "explicit signing key: a SHA256:... ssh-agent fingerprint, a path to a public key, or a ssh-agent key's comment/name (case-insensitive substring)"

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
'ctxloom manage config set sign.key') overrides both, and accepts a
SHA256:... fingerprint, a path to a public key, or a ssh-agent key's
comment/name (matched case-insensitively, substring OK — e.g. "ben@abbitt"
for "ben@abbitt.me"). ctxloom never reads, generates, or stores private key
material — every signature is produced by your existing ssh-agent.

Examples:
  ctxloom sign my-tools                          # bare = local bundle (the common case)
  ctxloom sign my-tools#fragments/go-testing      # resolves to bundle my-tools
  ctxloom sign --all                              # every local bundle this project publishes
  ctxloom sign my-tools --key ~/.ssh/id_ed25519.pub
  ctxloom sign my-tools --key ben@abbitt.me       # match by ssh-agent key comment`,
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

// signCmdResult is emit()'s result for `ctxloom sign`: one entry per bundle
// signed, so json/yaml/toml/markdown callers get every target uniformly
// instead of scraping the "signed by X (Y)" text lines this command has
// always printed.
type signCmdResult struct {
	Signed []signCmdTarget `json:"signed"`
}

// signCmdTarget mirrors operations.SignBundleResult plus the resolved signer
// identity (SignBundleResult itself doesn't carry the Discovered — signing is
// pure and takes an already-resolved ssh.Signer, so the CLI layer is the
// first place bundle result and signer identity are both in hand together).
type signCmdTarget struct {
	Bundle      string `json:"bundle" col:"Bundle"`
	ItemNote    string `json:"item_note,omitempty"`
	BundlePath  string `json:"bundle_path"`
	SigPath     string `json:"sig_path"`
	SignedBy    string `json:"signed_by"`
	Fingerprint string `json:"fingerprint"`
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
		return emit(cmd, signCmdResult{}, func() error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "no local bundles to sign")
			return err
		})
	}

	result := signCmdResult{Signed: make([]signCmdTarget, 0, len(targets))}
	for _, target := range targets {
		res, err := operations.SignBundleFile(cfg, operations.SignBundleRequest{
			Target: target,
			Signer: discovered.Signer,
		})
		if err != nil {
			return err
		}
		result.Signed = append(result.Signed, signCmdTarget{
			Bundle:      res.BundleName,
			ItemNote:    res.ItemNote,
			BundlePath:  res.BundlePath,
			SigPath:     res.SigPath,
			SignedBy:    discovered.Source,
			Fingerprint: discovered.Fingerprint,
		})
	}

	return emit(cmd, result, func() error {
		for _, t := range result.Signed {
			printSignResult(cmd.OutOrStdout(), t)
		}
		return nil
	})
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
func printSignResult(w io.Writer, t signCmdTarget) {
	if t.ItemNote != "" {
		fmt.Fprintf(w, "Signing bundle %s (contains %s) — signatures cover whole bundles.\n", t.Bundle, t.ItemNote)
	}
	fmt.Fprintf(w, "  %s  ->  %s\n", t.BundlePath, t.SigPath)
	fmt.Fprintf(w, "  signed by %s (%s)\n", t.SignedBy, t.Fingerprint)
}

func init() {
	rootCmd.AddCommand(signCmd)
	signCmd.Flags().BoolVar(&signAllFlag, "all", false, "sign every local bundle this project publishes")
	signCmd.Flags().StringVar(&signKeyFlag, "key", "", signKeyFlagHelp)
}
