package cli

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/textutil"
)

var bundleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed bundles",
	Long: `List all installed bundles: local bundles in .ctxloom/content/bundles
plus remote bundles pinned in the lockfile.

Shows bundle name, version, description, and content summary.`,
	RunE: runBundleList,
}

func runBundleList(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	bundleDirs := cfg.GetBundleDirs()
	var bundleInfos []*bundles.BundleInfo
	if len(bundleDirs) > 0 {
		bundleInfos, err = operations.ListBundles(cmd.Context(), cfg)
		if err != nil {
			return fmt.Errorf("failed to list bundles: %w", err)
		}
	}
	if bundleInfos == nil {
		bundleInfos = []*bundles.BundleInfo{}
	}

	return emit(cmd, bundleInfos, func() error {
		out := cmd.OutOrStdout()
		if len(bundleDirs) == 0 {
			w := iox.NewErrWriter(out)
			w.Println("No bundles directory found. Create one with: mkdir -p .ctxloom/content/bundles")
			return w.Err()
		}
		return renderBundleList(out, bundleInfos)
	})
}

// renderBundleList writes the human-readable summary of installed bundles
// to out. Extracted from runBundleList so the formatting decisions (the
// `1 MCP server`/`N MCP servers` singular/plural rule, the optional
// Tags/Contains lines, the empty-list hint) can be unit-tested without
// touching the loader.
func renderBundleList(out io.Writer, infos []*bundles.BundleInfo) error {
	w := iox.NewErrWriter(out)
	if len(infos) == 0 {
		w.Println("No bundles installed.")
		w.Println("Add remote bundles to a profile (ctxloom profile create/modify), then ctxloom deps pull")
		return w.Err()
	}

	w.Printf("Installed bundles (%d):\n\n", len(infos))
	for _, info := range infos {
		renderBundleListEntry(w, info)
	}
	return w.Err()
}

// bundleContentParts builds the "Contains: …" summary parts for a bundle,
// applying the 1-MCP-server singular/plural rule and omitting zero counts.
func bundleContentParts(info *bundles.BundleInfo) []string {
	var parts []string
	if info.FragmentCount > 0 {
		parts = append(parts, fmt.Sprintf("%d fragments", info.FragmentCount))
	}
	if info.CommandCount > 0 {
		parts = append(parts, fmt.Sprintf("%d commands", info.CommandCount))
	}
	if info.MCPCount == 1 {
		parts = append(parts, "1 MCP server")
	} else if info.MCPCount > 1 {
		parts = append(parts, fmt.Sprintf("%d MCP servers", info.MCPCount))
	}
	return parts
}

// renderBundleListEntry writes one bundle's summary line plus its optional
// description, Contains, and Tags lines.
func renderBundleListEntry(w *iox.ErrWriter, info *bundles.BundleInfo) {
	w.Printf("  %s", info.Name)
	if info.Deleted {
		// Removed upstream: no version/metadata to show — just flag it.
		w.Println(" (deleted upstream)")
		return
	}
	if info.Version != "" {
		w.Printf(" (v%s)", info.Version)
	}
	// State markers ride the name line so a scan of the listing surfaces them
	// without reading each entry's body. Both are silent-loss modes: a held
	// bundle looks exactly like a failing sync, and a retracted one is still
	// installed and still being served while its publisher has said not to use
	// it. Rendering neither is what made `bundle list` unable to tell a
	// deliberate freeze from a broken pull (J001900's B3 hop).
	if info.Held {
		w.Print(" [held]")
	}
	if info.Retracted {
		w.Print(" [retracted]")
	}
	w.Println()

	if info.Retracted && info.RetractedReason != "" {
		w.Printf("    retracted by its publisher: %s\n", info.RetractedReason)
	}

	if info.Description != "" {
		w.Printf("    %s\n", info.Description)
	}

	if parts := bundleContentParts(info); len(parts) > 0 {
		w.Printf("    Contains: %s\n", strings.Join(parts, ", "))
	}
	if len(info.Tags) > 0 {
		w.Printf("    Tags: %s\n", strings.Join(info.Tags, ", "))
	}
	w.Println()
}

var bundleShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show bundle contents",
	Long: `Display detailed information about a bundle.

Shows all fragments, commands, and MCP server configuration contained in the bundle.`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleShow,
}

func runBundleShow(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	bundleDirs := cfg.GetBundleDirs()
	if len(bundleDirs) == 0 {
		// No bundles anywhere, so there is no bundle named "help" either —
		// the courtesy shortcut costs nothing here (see the lookup below).
		if name == helpArgName {
			return cmd.Help()
		}
		return fmt.Errorf("no bundles directory found")
	}

	bundle, err := operations.GetBundle(cfg, name)
	if err != nil {
		// "help" is a legal bundle name, so the `bundle show help` courtesy
		// shortcut fires only when there is no such bundle — where the command
		// was going to fail anyway. A bundle actually named "help" is shown.
		if name == helpArgName {
			return cmd.Help()
		}
		return fmt.Errorf("bundle not found: %s", name)
	}

	// Route through emit() so `bundle show --format json` yields the structured
	// bundle, matching `bundle list` and the rest of the CLI; text stays the
	// human view.
	if err := emit(cmd, bundle, func() error {
		return renderBundleShow(cmd.OutOrStdout(), bundle)
	}); err != nil {
		return err
	}

	// TR4 interactive trust review: render per-item effective trust and offer a
	// per-hook trust/blacklist action. TTY-gated and json-suppressed so the bundle
	// body above (and `--format json`) is byte-for-byte unchanged; all trust UI
	// goes to stderr. Viewing never trusts.
	if bundleShowInteractive && outputFormatOf(cmd) != formatJSON && isInteractiveTerminal() {
		return offerBundleTrust(cmd, cfg, name, bundle)
	}
	return nil
}

var bundleShowInteractive bool

// renderBundleShow writes the detailed bundle view to out. Sections
// (MCP/Fragments/Commands/Notes) are suppressed when empty. Fragment and
// command entries are decorated with their tag list and a `(distilled)`
// or `(no_distill)` marker; fragments also get a 70-char first-line
// preview, commands get the optional Description.
// renderBundleShowHeader writes the bundle metadata header (name, plus optional
// version/author/description/tags), the path, and a trailing blank line.
func renderBundleShowHeader(w *iox.ErrWriter, bundle *bundles.Bundle) {
	w.Printf("Bundle: %s\n", bundle.Name)
	if bundle.Version != "" {
		w.Printf("Version: %s\n", bundle.Version)
	}
	if bundle.Author != "" {
		w.Printf("Author: %s\n", bundle.Author)
	}
	if bundle.Description != "" {
		w.Printf("Description: %s\n", bundle.Description)
	}
	if len(bundle.Tags) > 0 {
		w.Printf("Tags: %s\n", strings.Join(bundle.Tags, ", "))
	}
	w.Printf("Path: %s\n", bundle.Path)
	w.Println()
}

func renderBundleShow(out io.Writer, bundle *bundles.Bundle) error {
	w := iox.NewErrWriter(out)
	renderBundleShowHeader(w, bundle)

	if bundle.HasMCP() {
		w.Printf("MCP Servers (%d):\n", bundle.MCPCount())
		for _, name := range bundle.MCPNames() {
			renderBundleMCPEntry(w, name, bundle.MCP[name])
		}
		w.Println()
	}

	if len(bundle.Fragments) > 0 {
		w.Printf("Fragments (%d):\n", len(bundle.Fragments))
		for _, name := range bundle.FragmentNames() {
			renderBundleFragmentEntry(w, name, bundle.Fragments[name])
		}
		w.Println()
	}

	if len(bundle.Commands) > 0 {
		w.Printf("Commands (%d):\n", len(bundle.Commands))
		for _, name := range bundle.PromptNames() {
			renderBundleCommandEntry(w, name, bundle.Commands[name])
		}
		w.Println()
	}

	if bundle.Notes != "" {
		w.Println("Notes:")
		w.Printf("  %s\n", bundle.Notes)
	}
	return w.Err()
}

func renderBundleMCPEntry(w *iox.ErrWriter, name string, mcp bundles.BundleMCP) {
	w.Printf("  - %s\n", name)
	w.Printf("      Command: %s\n", mcp.Command)
	if len(mcp.Args) > 0 {
		w.Printf("      Args: %s\n", strings.Join(mcp.Args, " "))
	}
	if len(mcp.Env) > 0 {
		w.Println("      Env:")
		// Sorted: `bundle show` is diffed and scripted against, so two runs
		// over an unchanged bundle must produce identical bytes.
		for _, k := range slices.Sorted(maps.Keys(mcp.Env)) {
			w.Printf("        %s=%s\n", k, mcp.Env[k])
		}
	}
	if mcp.Notes != "" {
		w.Printf("      Notes: %s\n", mcp.Notes)
	}
	if mcp.Installation != "" {
		w.Printf("      Installation: %s\n", mcp.Installation)
	}
}

func renderBundleFragmentEntry(w *iox.ErrWriter, name string, frag bundles.BundleFragment) {
	w.Printf("  - %s", name)
	if len(frag.Tags) > 0 {
		w.Printf(" [%s]", strings.Join(frag.Tags, ", "))
	}
	switch {
	case frag.Distilled != "":
		w.Print(" (distilled)")
	case frag.NoDistill:
		w.Print(" (no_distill)")
	}
	w.Println()

	// 70 bytes TOTAL, ellipsis reserved by Ellipsize.
	firstLine := textutil.Ellipsize(strings.Split(strings.TrimSpace(frag.Content), "\n")[0], 70)
	w.Printf("      %s\n", firstLine)
}

func renderBundleCommandEntry(w *iox.ErrWriter, name string, prompt bundles.BundleCommand) {
	w.Printf("  - %s", name)
	if len(prompt.Tags) > 0 {
		w.Printf(" [%s]", strings.Join(prompt.Tags, ", "))
	}
	switch {
	case prompt.Distilled != "":
		w.Print(" (distilled)")
	case prompt.NoDistill:
		w.Print(" (no_distill)")
	}
	w.Println()
	if prompt.Description != "" {
		w.Printf("      %s\n", prompt.Description)
	}
}

// registerBundleShowFlags defines `bundle show`'s flags.
func registerBundleShowFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVarP(&bundleShowInteractive, "interactive", "i", false, "Review per-item effective trust and trust/blacklist individual hooks (interactive terminal only)")
}
