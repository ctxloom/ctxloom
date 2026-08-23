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
	"github.com/ctxloom/ctxloom/internal/trust"
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
  bundle-name#commands/name       Command content (prompts/ is accepted too)
  bundle-name#mcp/name            MCP server config
  bundle-name#skills/name         Skill manifest
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

	// The bundle half and the item selector, split verbatim: `view` walks the
	// bundle DOCUMENT, and its path vocabulary is a SUPERSET of the addressable
	// item kinds (a profile is viewable, never trust-addressable), so the split
	// happens here and renderBundleViewItem judges the kind.
	bundleName, itemPath, _ := strings.Cut(ref, "#")

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

	// Content carries the RAW bytes: a json/yaml/toml consumer is not a
	// terminal, its own grammar already renders a control byte inert inside a
	// string, and escaping the payload a second time would corrupt what a
	// script parses. The terminal rendering is the sanitised one, and only it
	// (delicious-goatskin).
	result := bundleViewResult{
		Bundle:    bundleName,
		Path:      itemPath,
		Distilled: itemPath != "" && bundleViewDistilled,
		Content:   string(content),
	}
	return emit(cmd, result, func() error {
		return writeBundleViewText(cmd.OutOrStdout(), ref, itemPath, content)
	})
}

// writeBundleViewText writes `bundle view`'s --format text rendering: the same
// information, rendered inert for a terminal by the shared termsafe seam.
//
// Blank-line collapsing is applied to an ITEM body and not to the whole-bundle
// dump. An item body is content surrounded by ctxloom's own framing, and
// collapsing bounds how far a publisher can push that framing off the screen;
// the bare `bundle view <name>` dump is a DOCUMENT that people redirect to a
// file, and handing back a document that is not the one on disk would be a
// different bug from the one being fixed. Escaping applies to both, because a
// terminal is on the other end of both.
func writeBundleViewText(w io.Writer, ref, itemPath string, content []byte) error {
	return publisherBody("", "", itemPath != "").Render(w, ref, string(content))
}

// renderBundleViewItem renders one item out of an already-loaded bundle,
// dispatching on the KIND its selector names rather than on a literal
// directory word. The kind comes from trust.ParseSelector, so every spelling
// that parser accepts — "#commands/x" and its "#prompts/x" alias alike —
// reaches the same arm here as it does through every other reader.
//
// The profiles arm is the ONE addition to that vocabulary, and it lives here
// because `view` walks the bundle DOCUMENT: a profile is content a reader may
// want to see, while trust.ParseSelector addresses only what can be delivered
// and countersigned, which a profile never is.
//
// For fragments and commands the distilled view is preferred when useDistilled
// is true AND the entry has a distilled payload; otherwise the raw Content is
// rendered. For mcp and profiles the entry is YAML-marshaled. A single-MCP
// bundle's lone server is returned even if the name doesn't match — convenient
// for `view bundle#mcp/default` against bundles that only ship one server
// under an arbitrary key.
func renderBundleViewItem(out io.Writer, bundle *bundles.Bundle, itemPath string, useDistilled bool) error {
	w := iox.NewErrWriter(out)

	if profName, ok := strings.CutPrefix(itemPath, profileViewPrefix); ok {
		profile, found := bundle.Profiles[profName]
		if !found {
			return fmt.Errorf("profile not found: %s", profName)
		}
		return writeViewYAML(w, "Profile: "+profName, profile, "profile")
	}

	kind, itemName, err := trust.ParseSelector(itemPath)
	if err != nil {
		return fmt.Errorf("invalid path format: %w (expected fragments, commands, mcp, skills, or profiles)", err)
	}

	switch kind {
	case trust.KindFragment:
		frag, found := bundle.Fragments[itemName]
		if !found {
			return fmt.Errorf("fragment not found: %s", itemName)
		}
		writeViewContent(w, frag.Content, frag.Distilled, useDistilled)
		return w.Err()

	case trust.KindPrompt:
		command, found := bundle.Commands[itemName]
		if !found {
			return fmt.Errorf("command not found: %s", itemName)
		}
		writeViewContent(w, command.Content, command.Distilled, useDistilled)
		return w.Err()

	case trust.KindMCP:
		mcp, name, found := lookupBundleMCP(bundle, itemName)
		if !found {
			return fmt.Errorf("mcp server not found: %s", itemName)
		}
		return writeViewYAML(w, "MCP Server: "+name, mcp, "MCP config")

	case trust.KindSkill:
		skill, found := bundle.Skills[itemName]
		if !found {
			return fmt.Errorf("skill not found: %s", itemName)
		}
		return writeViewYAML(w, "Skill: "+itemName, skill, "skill")

	default:
		return fmt.Errorf("%s items are not viewable: %s", kind.Dir(), itemPath)
	}
}

// profileViewPrefix is the selector directory `view` accepts beyond the
// addressable item kinds. It is spelled here and nowhere else: a profile is
// not an item any other surface may name.
const profileViewPrefix = "profiles/"

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

// registerBundleViewFlags defines `bundle view`'s flags.
func registerBundleViewFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVarP(&bundleViewDistilled, "distilled", "d", false, "Show distilled version if available")
}
