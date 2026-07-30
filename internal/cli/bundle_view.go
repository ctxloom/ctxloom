package cli

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// bundleViewResult is emit()'s result for `bundle view`: Content is exactly
// the bytes --format text has always printed (the full bundle YAML, or one
// item's raw/distilled body) — json/yaml/toml/markdown wrap the same bytes
// with the bundle/path/distilled context those formats can express but a
// bare stdout dump could not.
type bundleViewResult struct {
	Bundle    string `json:"bundle"`
	Path      string `json:"path,omitempty"`
	Distilled bool   `json:"distilled,omitempty"`
	Content   string `json:"content"`
}

var bundleViewCmd = &cobra.Command{
	Use:   "view <name[#path]>",
	Short: "View bundle content",
	Long: `View bundle content, optionally drilling into specific items.

Without a path, displays the full bundle YAML.
With a path after #, displays just that item's content.

Path formats:
  bundle-name                     Full bundle YAML
  bundle-name#fragments/name      Fragment content
  bundle-name#commands/name       Command content
  bundle-name#mcp/name            MCP server config
  bundle-name#profiles/name       Profile definition

Examples:
  ctxloom bundle view core-practices
  ctxloom bundle view core-practices#fragments/tdd
  ctxloom bundle view mcp-tasks#commands/setup-tasks
  ctxloom bundle view sequential-thinking#mcp/default
  ctxloom bundle view code-review#profiles/cr-security-golang`,
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

	// If no path, the content is the full bundle YAML; otherwise render just
	// that item through the existing writer-based renderer, buffered so the
	// exact same bytes back both the --format text write and the structured
	// result's Content field.
	var content []byte
	if itemPath == "" {
		content = res.Raw
	} else {
		var buf bytes.Buffer
		if err := renderBundleViewItem(&buf, res.Bundle, itemPath, bundleViewDistilled); err != nil {
			return err
		}
		content = buf.Bytes()
	}

	result := bundleViewResult{
		Bundle:    bundleName,
		Path:      itemPath,
		Distilled: itemPath != "" && bundleViewDistilled,
		Content:   string(content),
	}
	return emit(cmd, result, func() error {
		_, err := cmd.OutOrStdout().Write(content)
		return err
	})
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
		frag, found := bundle.Fragments[itemName]
		if !found {
			return fmt.Errorf("fragment not found: %s", itemName)
		}
		writeViewContent(w, frag.Content, frag.Distilled, useDistilled)
		return w.Err()

	case "commands":
		command, found := bundle.Commands[itemName]
		if !found {
			return fmt.Errorf("command not found: %s", itemName)
		}
		writeViewContent(w, command.Content, command.Distilled, useDistilled)
		return w.Err()

	case "mcp":
		mcp, name, found := lookupBundleMCP(bundle, itemName)
		if !found {
			return fmt.Errorf("mcp server not found: %s", itemName)
		}
		return writeViewYAML(w, "MCP Server: "+name, mcp, "MCP config")

	case "profiles":
		profile, found := bundle.Profiles[itemName]
		if !found {
			return fmt.Errorf("profile not found: %s", itemName)
		}
		return writeViewYAML(w, "Profile: "+itemName, profile, "profile")

	default:
		return fmt.Errorf("unknown item type: %s (expected fragments, commands, mcp, or profiles)", itemType)
	}
}

// writeViewYAML renders a structured item (an MCP server, a profile) as a
// "# <heading>" line followed by its YAML. The mcp and profiles arms of
// renderBundleViewItem differ only in heading and in what a marshal failure is
// called, so the shape lives here once. what names the item in that error.
func writeViewYAML(w *iox.ErrWriter, heading string, v any, what string) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal %s: %w", what, err)
	}
	w.Printf("# %s\n", heading)
	w.WriteRaw(data)
	return w.Err()
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
