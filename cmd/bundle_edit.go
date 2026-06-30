package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

var bundleCreateDesc string

var bundleCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new bundle",
	Long: `Create a new bundle file in .ctxloom/bundles.

Creates a skeleton bundle YAML file that you can edit to add content.`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleCreate,
}

func runBundleCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	if name == "help" {
		return cmd.Help()
	}

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
		Skills: map[string]operations.BundleSkillInput{
			"example": {Description: "Example prompt", Tags: []string{"example"}, Content: "Example prompt content. Describe what this prompt does.", NoDistill: true},
		},
	})
	if err != nil {
		return err
	}

	w := iox.NewErrWriter(cmd.OutOrStdout())
	w.Printf("Created bundle: %s\n", res.Path)
	w.Println("Edit the file to add your fragments and prompts.")

	return w.Err()
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
	if name == "help" {
		return cmd.Help()
	}

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
		return err
	}

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
}

// placeholderFragments/Prompts/MCP build the add-only inputs for the
// `bundle edit --add-*` flags: a starter item the user fills in later.
// Distillation is left off (runBundleEdit passes no Distiller) since there's no
// real content yet.
func placeholderFragments(names []string) map[string]operations.BundleFragmentInput {
	if len(names) == 0 {
		return nil
	}
	m := make(map[string]operations.BundleFragmentInput, len(names))
	for _, n := range names {
		m[n] = operations.BundleFragmentInput{Content: "# " + n + "\n\nAdd content here."}
	}
	return m
}

func placeholderPrompts(names []string) map[string]operations.BundleSkillInput {
	if len(names) == 0 {
		return nil
	}
	m := make(map[string]operations.BundleSkillInput, len(names))
	for _, n := range names {
		m[n] = operations.BundleSkillInput{Description: n, Content: "Add prompt content here."}
	}
	return m
}

func placeholderMCP(names []string) map[string]operations.BundleMCPInput {
	if len(names) == 0 {
		return nil
	}
	m := make(map[string]operations.BundleMCPInput, len(names))
	for _, n := range names {
		m[n] = operations.BundleMCPInput{Command: n}
	}
	return m
}

var bundleDeleteForce bool

var bundleDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a bundle",
	Long: `Delete a bundle from the local .ctxloom/bundles directory.

This permanently removes the bundle file. Use --force to skip confirmation.

Examples:
  ctxloom bundle delete old-bundle
  ctxloom bundle delete my-bundle --force`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleDelete,
}

func runBundleDelete(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Read-only load just to show the path in the confirm prompt; the actual
	// (guarded) removal goes through operations.DeleteBundle.
	bundle, err := operations.GetBundle(cfg, name)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", name)
	}

	w := iox.NewErrWriter(cmd.OutOrStdout())
	if !bundleDeleteForce {
		confirm := stdinConfirmer(cmd.InOrStdin())
		if !confirm(fmt.Sprintf("Delete bundle %q at %s? [y/N] ", name, bundle.Path)) {
			w.Println("Cancelled.")
			return w.Err()
		}
	}
	res, err := operations.DeleteBundle(cmd.Context(), cfg, operations.DeleteBundleRequest{Name: name})
	if err != nil {
		return err
	}
	w.Printf("Deleted bundle: %s\n", res.Path)
	return w.Err()
}

// confirmFn returns true iff the user confirms an interactive prompt.
// The prompt text is passed in so the helper can phrase its own
// question; the function itself owns reading input.
type confirmFn func(prompt string) bool

// stdinConfirmer builds a confirmFn that reads one line from in (defaults
// to os.Stdin via cobra), printing prompt to os.Stderr first. Only "y"
// or "Y" counts as confirmation; anything else (including EOF) is a no.
func stdinConfirmer(in io.Reader) confirmFn {
	return func(prompt string) bool {
		fmt.Fprint(os.Stderr, prompt)
		var answer string
		// Scanln on a Reader requires fmt.Fscanln. EOF / read errors
		// land here as err != nil with answer == "", which the
		// comparison naturally treats as "no".
		_, _ = fmt.Fscanln(in, &answer)
		return answer == "y" || answer == "Y"
	}
}
