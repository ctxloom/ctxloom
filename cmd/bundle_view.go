package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/shared/iox"
)

var bundleViewCmd = &cobra.Command{
	Use:   "view <name[#path]>",
	Short: "View bundle content",
	Long: `View bundle content, optionally drilling into specific items.

Without a path, displays the full bundle YAML.
With a path after #, displays just that item's content.

Path formats:
  bundle-name                     Full bundle YAML
  bundle-name#fragments/name      Fragment content
  bundle-name#prompts/name        Prompt content
  bundle-name#mcp/name            MCP server config

Examples:
  ctxloom bundle view core-practices
  ctxloom bundle view core-practices#fragments/tdd
  ctxloom bundle view mcp-tasks#prompts/setup-tasks
  ctxloom bundle view sequential-thinking#mcp/default`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleView,
}

var bundleViewDistilled bool

func runBundleView(cmd *cobra.Command, args []string) error {
	ref := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	bundleName, itemPath := parseBundleViewRef(ref)

	res, err := operations.ReadBundle(cmd.Context(), cfg, operations.ReadBundleRequest{Name: bundleName})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()

	// If no path, show full bundle YAML.
	if itemPath == "" {
		_, _ = out.Write(res.Raw)
		return nil
	}

	return renderBundleViewItem(out, res.Bundle, itemPath, bundleViewDistilled)
}

// parseBundleViewRef splits a `view` argument like `mybundle` or
// `mybundle#fragments/intro` into its (bundleName, itemPath) parts.
// Empty itemPath signals "render the whole bundle YAML"; non-empty
// goes through renderBundleViewItem's type/name switch.
func parseBundleViewRef(ref string) (bundleName, itemPath string) {
	if before, after, ok := strings.Cut(ref, "#"); ok {
		return before, after
	}
	return ref, ""
}

// renderBundleViewItem dispatches on the item type prefix in itemPath
// (fragments/prompts/mcp). For fragments/prompts, the distilled view is
// preferred when useDistilled is true AND the entry has a distilled
// payload; otherwise the raw Content is rendered. For mcp, the entry is
// YAML-marshaled. A single-MCP bundle's lone server is returned even if
// the name doesn't match — convenient for `view bundle#mcp/default`
// against bundles that only ship one server under an arbitrary key.
func renderBundleViewItem(out io.Writer, bundle *bundles.Bundle, itemPath string, useDistilled bool) error {
	itemType, itemName, ok := strings.Cut(itemPath, "/")
	if !ok {
		return fmt.Errorf("invalid path format: %s (expected type/name)", itemPath)
	}

	w := iox.NewErrWriter(out)
	switch itemType {
	case "fragments":
		frag, ok := bundle.Fragments[itemName]
		if !ok {
			return fmt.Errorf("fragment not found: %s", itemName)
		}
		writeViewContent(w, frag.Content, frag.Distilled, useDistilled)
		return w.Err()

	case "prompts":
		prompt, ok := bundle.Prompts[itemName]
		if !ok {
			return fmt.Errorf("prompt not found: %s", itemName)
		}
		writeViewContent(w, prompt.Content, prompt.Distilled, useDistilled)
		return w.Err()

	case "mcp":
		mcp, name, ok := lookupBundleMCP(bundle, itemName)
		if !ok {
			return fmt.Errorf("mcp server not found: %s", itemName)
		}
		data, err := yaml.Marshal(mcp)
		if err != nil {
			return fmt.Errorf("failed to marshal MCP config: %w", err)
		}
		w.Printf("# MCP Server: %s\n", name)
		w.WriteRaw(data)
		return w.Err()

	default:
		return fmt.Errorf("unknown item type: %s (expected fragments, prompts, or mcp)", itemType)
	}
}

// writeViewContent picks between distilled and raw content per the
// useDistilled flag and ensures a trailing newline. Used by both
// the fragments and prompts arms of renderBundleViewItem.
func writeViewContent(w *iox.ErrWriter, raw, distilled string, useDistilled bool) {
	content := raw
	if useDistilled && distilled != "" {
		content = distilled
		w.Println("# (distilled version)")
	}
	w.Print(content)
	if !strings.HasSuffix(content, "\n") {
		w.Println()
	}
}

// lookupBundleMCP returns the MCP entry for itemName from the bundle.
// If the name doesn't match and the bundle ships exactly one MCP server,
// returns that server (with its real name) — this convenience lets
// `view <bundle>#mcp/default` work against single-server bundles
// regardless of the server's actual key.
func lookupBundleMCP(bundle *bundles.Bundle, itemName string) (bundles.BundleMCP, string, bool) {
	if mcp, ok := bundle.MCP[itemName]; ok {
		return mcp, itemName, true
	}
	if len(bundle.MCP) == 1 {
		for name, mcp := range bundle.MCP {
			return mcp, name, true
		}
	}
	return bundles.BundleMCP{}, "", false
}
