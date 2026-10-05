package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/agentkey"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// signKeyFlag backs `ctxloom bundle sign --key <path|fingerprint>` — an explicit
// override that wins over both git config user.signingkey and ssh-agent
// auto-detection (spec §7A.4).
var (
	signKeyFlag   string
	signAllFlag   bool
	signForceFlag bool
)

// signKeyFlagHelp documents every form --key/sign.key accepts, in the order
// resolveKeyValue tries them (internal/adapters/signing/agentkey.Discoverer). Shared
// between the flag registration and anywhere else this needs restating.
const signKeyFlagHelp = "explicit signing key: a SHA256:... ssh-agent fingerprint, a path to a public key, or a ssh-agent key's comment/name (case-insensitive substring)"

// signCmdLong documents `ctxloom bundle sign`. A ref is never resolved
// against a catalog: a publishing repository signs the bundles it ships,
// whether or not they are installed anywhere. A remote bundle or a companion
// loadout is refused by name rather than skipped. Key discovery is zero-config
// so anyone who already signs commits with SSH needs no ctxloom setup.
const signCmdLong = `Sign a local bundle tree so anyone who trusts your key can verify it came
from you. Signing writes a SHA256SUMS manifest over every file in the tree,
headed by the bundle's name and version, and your signature over it in the
bundle's .sigs/ directory; anyone can check the files with
'sha256sum -c SHA256SUMS'.

With a VERSION file at the project root (the directory holding .ctxloom),
signing first sets bundle.yaml's version to match it, and says so. A version
already signed over different files is refused: bump the version, or pass
--force to re-sign it.

ref is a local bundle name, its 'ctxloom+local:<name>' URI, or an item ref
('<bundle>#fragments/<name>'), which signs the bundle that holds it. Only
bundles you author locally can be signed: a remote bundle is its publisher's
to sign, and a companion's loadout is signed where it is built.

The key is --key or the sign.key config value, else 'git config
user.signingkey', else the only identity in ssh-agent. Each accepts a
SHA256:... fingerprint, a public key or its path, or part of an ssh-agent
key's comment, matched case-insensitively (e.g. "ben@abbitt" for
"ben@abbitt.me"). ctxloom never reads, generates or stores private key
material: your ssh-agent makes every signature.`

const signCmdExample = `  ctxloom bundle sign my-tools                          # bare = local bundle (the common case)
  ctxloom bundle sign 'my-tools#fragments/go-testing'    # resolves to bundle my-tools
  ctxloom bundle sign 'ctxloom+local:my-tools'           # the same bundle, canonically
  ctxloom bundle sign --all                              # every local bundle this project publishes
  ctxloom bundle sign my-tools --key ~/.ssh/id_ed25519.pub
  ctxloom bundle sign my-tools --key ben@abbitt.me       # match by ssh-agent key comment`

// runSignCmd is bundleSignCmd's RunE.
func runSignCmd(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	ref := ""
	if len(args) > 0 {
		ref = args[0]
	}
	discoverer, err := operations.SignerDiscoverer()
	if err != nil {
		return err
	}
	return runSign(cmd, cfg, discoverer, ref, signAllFlag, signKeyFlag)
}

// bundleSignCmd is the bundle noun's `sign` domain verb.
var bundleSignCmd = &cobra.Command{
	Use:     "sign [ref]",
	Short:   "Sign a local bundle for publication",
	Long:    signCmdLong,
	Example: signCmdExample,
	Args:    cobra.MaximumNArgs(1),
	RunE:    runSignCmd,
}

// signCmdResult is emit()'s result for `ctxloom bundle sign`: one entry per bundle
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
	// ManifestPath is the SHA256SUMS the signature covers: every file in the
	// tree, not bundle.yaml alone.
	ManifestPath string `json:"manifest_path,omitempty"`
	// VersionStamp is set when signing rewrote bundle.yaml's version from
	// the project's VERSION file.
	VersionStamp *operations.VersionStamp `json:"version_stamp,omitempty"`
}

// runSign is the testable body of `ctxloom bundle sign`: cfg and discoverer are
// both DI seams (a real config.Config over a temp project, and a fake
// agentkey.Discoverer wired to fake git-config/ssh-agent responses, mirror
// internal/adapters/signing/agentkey's own tests) so this composition — resolve key,
// resolve target(s), sign, report — is exercisable without a real ssh-agent
// or git binary.
func runSign(cmd *cobra.Command, cfg *config.Config, discoverer *agentkey.Discoverer, ref string, all bool, keyFlag string) error {
	if err := validateSignRequest(ref, all); err != nil {
		return err
	}

	discovered, err := discoverer.Discover(cmd.Context(), resolveSignKeyOverride(cfg, keyFlag))
	if err != nil {
		return err
	}
	// Signer signs over a live ssh-agent connection; release it once every
	// target has been signed.
	defer func() { _ = discovered.Close() }()

	targets, err := resolveSignTargets(cfg, ref, all)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return noSignTargetsError(cfg)
	}

	result := signCmdResult{Signed: make([]signCmdTarget, 0, len(targets))}
	for _, target := range targets {
		res, err := operations.SignBundleFile(cfg, operations.SignBundleRequest{
			Target:       target,
			Signer:       discovered.Signer,
			SignerSource: discovered.Source,
			Force:        signForceFlag,
		})
		if err != nil {
			return err
		}
		result.Signed = append(result.Signed, signCmdTarget{
			Bundle:       res.BundleName,
			ItemNote:     res.ItemNote,
			BundlePath:   res.BundlePath,
			SigPath:      res.SigPath,
			SignedBy:     discovered.Source,
			Fingerprint:  discovered.Fingerprint,
			ManifestPath: res.ManifestPath,
			VersionStamp: res.VersionStamp,
		})
	}

	return emit(cmd, result, func() error {
		for _, t := range result.Signed {
			printSignResult(cmd.OutOrStdout(), t)
		}
		return nil
	})
}

// validateSignRequest rejects the two argument shapes `bundle sign` cannot
// act on: a ref together with --all (two different answers to "sign what?"),
// and neither of them (no answer at all). Split out of runSign, which was
// carrying argument validation, key-override precedence, target resolution,
// the empty-target diagnostic, the signing loop and rendering in one body —
// every guard here is a pre-flight check on the caller's argv,
// answerable before any key or bundle is touched.
func validateSignRequest(ref string, all bool) error {
	if all && ref != "" {
		return fmt.Errorf("ctxloom bundle sign: --all cannot be combined with a ref")
	}
	if !all && ref == "" {
		return fmt.Errorf("ctxloom bundle sign: a ref is required (or pass --all)")
	}
	return nil
}

// resolveSignKeyOverride applies the explicit-key precedence rule (spec
// §7A.4): --key wins, then the sign.key config value, then nothing — which
// leaves agentkey.Discoverer to fall back to git config user.signingkey and a
// sole ssh-agent identity. A nil cfg (no project loaded) contributes nothing
// rather than panicking.
func resolveSignKeyOverride(cfg *config.Config, keyFlag string) string {
	if keyFlag != "" || cfg == nil {
		return keyFlag
	}
	return cfg.SignKey()
}

// noSignTargetsError explains a `sign --all` that matched nothing. This used
// to print "no local bundles to sign" and exit 0, so signing NOTHING
// looked exactly like having nothing to sign. `sign --all` is how a publishing
// repo signs its shipped content; a run that signed none of it is a failed
// run, and naming the directories searched is what turns "it printed something
// reassuring" into a diagnosable answer. (It is also the visible face of the
// GetBundleDirs-points-at-cache/bundles defect, which this exit code stops
// hiding.)
func noSignTargetsError(cfg *config.Config) error {
	searched := cfg.GetBundleDirs()
	if len(searched) == 0 {
		return fmt.Errorf("sign --all: no local bundle directories exist to search (expected an authored %s tree)", paths.LocalBundlesPath(paths.AppDirName))
	}
	return fmt.Errorf("sign --all: no local bundles found in %s — nothing was signed", strings.Join(searched, ", "))
}

// resolveSignTargets expands ref/--all into the SignTarget list to sign,
// reusing operations.ResolveSignTarget (which itself reuses the SAME ref
// grammar 'ctxloom trust' uses — no second grammar, ADR 0032).
func resolveSignTargets(cfg *config.Config, ref string, all bool) ([]operations.SignTarget, error) {
	if all {
		var targets []operations.SignTarget
		local, lerr := operations.ListLocalBundleNames(cfg, nil)
		if lerr != nil {
			return nil, lerr
		}
		for _, name := range local {
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
	if st := t.VersionStamp; st != nil {
		fmt.Fprintf(w, "  version %s -> %s in %s (stamped from %s)\n", st.From, st.To, t.BundlePath, st.File)
	}
	// Name the MANIFEST, not bundle.yaml: a bundle's content lives in files
	// beside its envelope, and the signature covers all of them.
	fmt.Fprintf(w, "  %s (whole tree)  ->  %s\n", t.ManifestPath, t.SigPath)
	fmt.Fprintf(w, "  signed by %s (%s)\n", t.SignedBy, t.Fingerprint)
}

func init() {
	// bundleCmd.AddCommand(bundleSignCmd) itself lives in bundle.go, which
	// assembles the whole bundle subtree.
	bundleSignCmd.Flags().BoolVar(&signAllFlag, "all", false, "sign every local bundle this project publishes")
	bundleSignCmd.Flags().BoolVar(&signForceFlag, "force", false, "re-sign a version whose last signature covered different files (bump the version instead unless you mean it)")
	bundleSignCmd.Flags().StringVar(&signKeyFlag, "key", "", signKeyFlagHelp)
}
