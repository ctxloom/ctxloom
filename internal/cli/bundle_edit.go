package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

var bundleCreateDesc string

var bundleCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new bundle",
	Long: `Create a new bundle file in .ctxloom/content/bundles.

Creates a skeleton bundle YAML file that you can edit to add content.`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleCreate,
}

func runBundleCreate(cmd *cobra.Command, args []string) error {
	// No help shortcut here: `bundle create help` is an explicit, unambiguous
	// request to create a bundle named "help", and the shortcut made that
	// impossible while reporting success and creating nothing. Cobra's --help
	// and `ctxloom help bundle create` are the ways to ask for help.
	name := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Route through the operations core so create gets the symlink-traversal
	// guard. nil Distiller: the starter template ships raw, exactly as the old
	// CLI skeleton did (NoDistill also marks the examples so a future wired
	// distiller still wouldn't waste a call on placeholder text).
	res, err := operations.CreateBundle(cmd.Context(), cfg, operations.CreateBundleRequest{
		Name:        name,
		Description: bundleCreateDesc,
		Tags:        []string{},
		Fragments: map[string]operations.BundleFragmentInput{
			"example": {Tags: []string{"example"}, Content: "# Example Fragment\n\nAdd your content here.", NoDistill: true},
		},
		Commands: map[string]operations.BundleCommandInput{
			"example": {Description: "Example prompt", Tags: []string{"example"}, Content: "Example prompt content. Describe what this prompt does.", NoDistill: true},
		},
	})
	if err != nil {
		return err
	}

	return emit(cmd, res, func() error {
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("Created bundle: %s\n", res.Path)
		w.Println("Edit the file to add your fragments and prompts.")
		return w.Err()
	})
}

// bundleEdit flags
var (
	bundleEditDesc           string
	bundleEditVersion        string
	bundleEditAddTags        []string
	bundleEditRemoveTags     []string
	bundleEditAddFragment    []string
	bundleEditRemoveFragment []string
	bundleEditAddPrompt      []string
	bundleEditRemovePrompt   []string
	bundleEditAddMCP         []string
	bundleEditRemoveMCP      []string
)

var bundleEditCmd = &cobra.Command{
	Use:   "edit <name>",
	Short: "Edit a bundle",
	Long: `Edit an existing bundle by adding or removing items.

Examples:
  ctxloom bundle edit my-bundle -d "New description"
  ctxloom bundle edit my-bundle --add-fragment coding-standards
  ctxloom bundle edit my-bundle --remove-prompt old-prompt
  ctxloom bundle edit my-bundle --add-tag golang --add-tag testing
  ctxloom bundle edit my-bundle --add-mcp tree-sitter`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleEdit,
}

func runBundleEdit(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	req := operations.UpdateBundleRequest{
		Name:             name,
		AddTags:          bundleEditAddTags,
		RemoveTags:       bundleEditRemoveTags,
		AddFragments:     placeholderFragments(bundleEditAddFragment),
		RemoveFragments:  bundleEditRemoveFragment,
		AddPrompts:       placeholderPrompts(bundleEditAddPrompt),
		RemovePrompts:    bundleEditRemovePrompt,
		AddMCPServers:    placeholderMCP(bundleEditAddMCP),
		RemoveMCPServers: bundleEditRemoveMCP,
		// nil Distiller: the flag path only adds placeholders, nothing to distill.
	}
	if bundleEditDesc != "" {
		d := bundleEditDesc
		req.SetDescription = &d
	}
	if bundleEditVersion != "" {
		v := bundleEditVersion
		req.SetVersion = &v
	}

	res, err := operations.UpdateBundle(cmd.Context(), cfg, req)
	if err != nil {
		// See runBundleShow: the courtesy shortcut only fires when nothing of
		// that name exists, so a bundle named "help" stays editable.
		if name == helpArgName {
			return cmd.Help()
		}
		return err
	}

	return emit(cmd, res, func() error {
		w := iox.NewErrWriter(cmd.OutOrStdout())
		if res.Status == "no_changes" {
			w.Println("No changes made. Use flags to specify what to edit.")
			if w.Err() != nil {
				return w.Err()
			}
			return cmd.Help()
		}
		for _, c := range res.Changes {
			w.Printf("%s\n", c)
		}
		w.Printf("Updated bundle: %s\n", res.Path)
		return w.Err()
	})
}

// placeholderFragments/Prompts/MCP build the add-only inputs for the
// `bundle edit --add-*` flags: a starter item the user fills in later.
// Distillation is left off (runBundleEdit passes no Distiller) since there's no
// real content yet.
// placeholders builds the name→placeholder-item map shared by the three
// scaffold kinds; make derives each item from its name. nil for no names, so
// an absent flag leaves the bundle section absent.
func placeholders[T any](names []string, make_ func(name string) T) map[string]T {
	if len(names) == 0 {
		return nil
	}
	m := make(map[string]T, len(names))
	for _, n := range names {
		m[n] = make_(n)
	}
	return m
}

func placeholderFragments(names []string) map[string]operations.BundleFragmentInput {
	return placeholders(names, func(n string) operations.BundleFragmentInput {
		return operations.BundleFragmentInput{Content: "# " + n + "\n\nAdd content here."}
	})
}

func placeholderPrompts(names []string) map[string]operations.BundleCommandInput {
	return placeholders(names, func(n string) operations.BundleCommandInput {
		return operations.BundleCommandInput{Description: n, Content: "Add prompt content here."}
	})
}

func placeholderMCP(names []string) map[string]operations.BundleMCPInput {
	return placeholders(names, func(n string) operations.BundleMCPInput {
		return operations.BundleMCPInput{Command: n}
	})
}

var bundleRemoveYes bool

var bundleRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm", "del"},
	Short:   "Remove a bundle",
	Long: `Remove a bundle from the local .ctxloom/content/bundles directory.

Bare invocation reports what would be removed — the bundle and every
fragment/command/mcp-server/skill/profile it carries — and removes nothing
(exit 0). Pass --yes to apply it.

Examples:
  ctxloom bundle remove old-bundle
  ctxloom bundle remove my-bundle --yes`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleRemove,
}

func runBundleRemove(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Read-only load: confirms the bundle exists and, for the report path,
	// supplies the counts of what removing it would take with it.
	bundle, err := operations.GetBundle(cfg, name)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", name)
	}

	applyCmd := fmt.Sprintf("ctxloom bundle remove %s --yes", name)
	if !bundleRemoveYes {
		detail := bundleRemoveDetail(bundle)
		target := fmt.Sprintf("bundle %q", name)
		return emit(cmd, newRemovePreviewResult(target, detail, applyCmd), func() error {
			printRemovePreview(cmd.OutOrStdout(), target, detail, applyCmd)
			return nil
		})
	}

	res, err := operations.DeleteBundle(cmd.Context(), cfg, operations.DeleteBundleRequest{Name: name})
	if err != nil {
		return err
	}
	return emit(cmd, res, func() error {
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("Removed bundle: %s\n", res.Path)
		return w.Err()
	})
}

// bundleRemoveDetail summarizes what `bundle remove`'s bare path would
// destroy: one count per content kind the bundle carries, omitting any kind
// the bundle holds none of so an empty bundle's report doesn't pad itself
// with "0 fragments, 0 commands, ...".
func bundleRemoveDetail(b *bundles.Bundle) []string {
	var parts []string
	if n := len(b.Fragments); n > 0 {
		parts = append(parts, fmt.Sprintf("%d fragment(s)", n))
	}
	if n := len(b.Commands); n > 0 {
		parts = append(parts, fmt.Sprintf("%d command(s)", n))
	}
	if n := len(b.MCP); n > 0 {
		parts = append(parts, fmt.Sprintf("%d MCP server(s)", n))
	}
	if n := len(b.Skills); n > 0 {
		parts = append(parts, fmt.Sprintf("%d skill package(s)", n))
	}
	if n := len(b.Profiles); n > 0 {
		parts = append(parts, fmt.Sprintf("%d profile(s)", n))
	}
	if len(parts) == 0 {
		return nil
	}
	return []string{strings.Join(parts, ", ")}
}

// registerBundleCreateFlags defines `bundle create`'s flags.
func registerBundleCreateFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&bundleCreateDesc, "description", "d", "", "Bundle description")
}

// registerBundleRemoveFlags defines `bundle remove`'s flags. There is no
// --force: it used to mean "skip the confirmation prompt", that prompt no
// longer exists (bare `remove` reports, --yes applies, nothing else), and a
// flag with no referent is refused rather than silently accepted-and-ignored
// — a script still passing -f gets cobra's unknown-flag error, loud and
// pointing at the exact word to remove.
func registerBundleRemoveFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVarP(&bundleRemoveYes, "yes", "y", false, "Apply the removal this invocation would report (default: report only)")
}

// registerBundleEditFlags defines `bundle edit`'s add/remove flag pairs and the
// metadata setters.
func registerBundleEditFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&bundleEditDesc, "description", "d", "", "New description")
	cmd.Flags().StringVar(&bundleEditVersion, "version", "", "New version")
	cmd.Flags().StringSliceVar(&bundleEditAddTags, "add-tag", nil, "Tag(s) to add")
	cmd.Flags().StringSliceVar(&bundleEditRemoveTags, "remove-tag", nil, "Tag(s) to remove")
	cmd.Flags().StringSliceVar(&bundleEditAddFragment, "add-fragment", nil, "Fragment(s) to add")
	cmd.Flags().StringSliceVar(&bundleEditRemoveFragment, "remove-fragment", nil, "Fragment(s) to remove")
	cmd.Flags().StringSliceVar(&bundleEditAddPrompt, "add-prompt", nil, "Prompt(s) to add")
	cmd.Flags().StringSliceVar(&bundleEditRemovePrompt, "remove-prompt", nil, "Prompt(s) to remove")
	cmd.Flags().StringSliceVar(&bundleEditAddMCP, "add-mcp", nil, "MCP server(s) to add")
	cmd.Flags().StringSliceVar(&bundleEditRemoveMCP, "remove-mcp", nil, "MCP server(s) to remove")
}
