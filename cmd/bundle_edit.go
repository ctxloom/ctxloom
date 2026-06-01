package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/iox"
	"github.com/ctxloom/ctxloom/internal/operations"
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
		Prompts: map[string]operations.BundlePromptInput{
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

	bundleDirs := cfg.GetBundleDirs()
	if len(bundleDirs) == 0 {
		return fmt.Errorf("no bundles directory found")
	}

	loader := bundles.NewLoader(bundleDirs, false)
	bundle, err := loader.Load(name)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", name)
	}

	edits := bundleEdits{
		Description:     bundleEditDesc,
		Version:         bundleEditVersion,
		AddTags:         bundleEditAddTags,
		RemoveTags:      bundleEditRemoveTags,
		AddFragments:    bundleEditAddFragment,
		RemoveFragments: bundleEditRemoveFragment,
		AddPrompts:      bundleEditAddPrompt,
		RemovePrompts:   bundleEditRemovePrompt,
		AddMCP:          bundleEditAddMCP,
		RemoveMCP:       bundleEditRemoveMCP,
	}

	w := iox.NewErrWriter(cmd.OutOrStdout())
	modified := applyBundleEdits(bundle, edits, w)
	if !modified {
		w.Println("No changes made. Use flags to specify what to edit.")
		if w.Err() != nil {
			return w.Err()
		}
		return cmd.Help()
	}

	// `bundle edit` mutates a loaded bundle in place and saves it directly
	// (operations.UpdateBundle's set-semantics would clobber existing fragment
	// content with the flag path's placeholders — see ADR 0018), so it must
	// apply the symlink-traversal guard itself, exactly as the operations write
	// paths do before saving.
	if err := operations.RequireSafeBundlePath(bundleDirs, bundle.Path); err != nil {
		return err
	}
	if err := bundle.Save(); err != nil {
		return fmt.Errorf("failed to save bundle: %w", err)
	}

	w.Printf("Updated bundle: %s\n", bundle.Path)
	return w.Err()
}

// bundleEdits bundles every mutation the `bundle edit` command supports.
// Empty strings / nil slices mean "no change to that slot". Pointer types
// would let the caller distinguish "unset" from "set to empty" if a
// future use case needs it; for now, current callers treat empty as
// "no change", which matches cobra's flag-defaulting behavior.
type bundleEdits struct {
	Description     string
	Version         string
	AddTags         []string
	RemoveTags      []string
	AddFragments    []string
	RemoveFragments []string
	AddPrompts      []string
	RemovePrompts   []string
	AddMCP          []string
	RemoveMCP       []string
}

// applyBundleEdits updates b in place per edits, writing one
// human-readable status line per attempted change to out. Returns true
// iff at least one change actually landed. Duplicate-adds and
// absent-removes for items are echoed as informational lines, mirroring
// applyProfileMutations.
//
// Extracted from runBundleEdit so the 10-slot mutation matrix (plus
// version + description) is testable without bundle loader IO. Note:
// RemoveTags currently always sets modified=true even when the tag
// wasn't present — preserved to match the original behavior; callers
// relying on the truth value should not infer "tag was actually removed."
func applyBundleEdits(b *bundles.Bundle, edits bundleEdits, w *iox.ErrWriter) bool {
	// OR (not short-circuit) so every category runs and emits its status lines.
	modified := applyBundleScalarEdits(b, edits)
	modified = applyBundleTagEdits(b, edits) || modified
	modified = applyBundleFragmentEdits(b, edits, w) || modified
	modified = applyBundlePromptEdits(b, edits, w) || modified
	modified = applyBundleMCPEdits(b, edits, w) || modified
	return modified
}

// applyBundleScalarEdits sets description/version when provided.
func applyBundleScalarEdits(b *bundles.Bundle, edits bundleEdits) bool {
	modified := false
	if edits.Description != "" {
		b.Description = edits.Description
		modified = true
	}
	if edits.Version != "" {
		b.Version = edits.Version
		modified = true
	}
	return modified
}

// applyBundleTagEdits adds (deduped) and removes tags. Note: a remove reports
// modified=true even when the tag was absent — preserved from the original
// behavior; callers must not infer "tag was actually removed" from the result.
func applyBundleTagEdits(b *bundles.Bundle, edits bundleEdits) bool {
	modified := false
	for _, tag := range edits.AddTags {
		if !sliceContains(b.Tags, tag) {
			b.Tags = append(b.Tags, tag)
			modified = true
		}
	}
	for _, tag := range edits.RemoveTags {
		b.Tags = sliceRemove(b.Tags, tag)
		modified = true
	}
	return modified
}

// applyBundleFragmentEdits adds placeholder fragments and removes named ones,
// echoing one status line per attempted change. Duplicate-adds and
// absent-removes are informational and do not count as modifications.
func applyBundleFragmentEdits(b *bundles.Bundle, edits bundleEdits, w *iox.ErrWriter) bool {
	modified := false
	if b.Fragments == nil && len(edits.AddFragments) > 0 {
		b.Fragments = make(map[string]bundles.BundleFragment)
	}
	for _, fragName := range edits.AddFragments {
		if _, exists := b.Fragments[fragName]; exists {
			w.Printf("Fragment already exists: %s\n", fragName)
			continue
		}
		b.Fragments[fragName] = bundles.BundleFragment{
			Content: "# " + fragName + "\n\nAdd content here.",
		}
		modified = true
		w.Printf("Added fragment: %s\n", fragName)
	}
	for _, fragName := range edits.RemoveFragments {
		if b.Fragments == nil {
			continue
		}
		if _, exists := b.Fragments[fragName]; !exists {
			w.Printf("Fragment not found: %s\n", fragName)
			continue
		}
		delete(b.Fragments, fragName)
		modified = true
		w.Printf("Removed fragment: %s\n", fragName)
	}
	return modified
}

// applyBundlePromptEdits is the prompt counterpart of applyBundleFragmentEdits.
func applyBundlePromptEdits(b *bundles.Bundle, edits bundleEdits, w *iox.ErrWriter) bool {
	modified := false
	if b.Prompts == nil && len(edits.AddPrompts) > 0 {
		b.Prompts = make(map[string]bundles.BundlePrompt)
	}
	for _, promptName := range edits.AddPrompts {
		if _, exists := b.Prompts[promptName]; exists {
			w.Printf("Prompt already exists: %s\n", promptName)
			continue
		}
		b.Prompts[promptName] = bundles.BundlePrompt{
			Description: promptName,
			Content:     "Add prompt content here.",
		}
		modified = true
		w.Printf("Added prompt: %s\n", promptName)
	}
	for _, promptName := range edits.RemovePrompts {
		if b.Prompts == nil {
			continue
		}
		if _, exists := b.Prompts[promptName]; !exists {
			w.Printf("Prompt not found: %s\n", promptName)
			continue
		}
		delete(b.Prompts, promptName)
		modified = true
		w.Printf("Removed prompt: %s\n", promptName)
	}
	return modified
}

// applyBundleMCPEdits is the MCP-server counterpart of applyBundleFragmentEdits.
func applyBundleMCPEdits(b *bundles.Bundle, edits bundleEdits, w *iox.ErrWriter) bool {
	modified := false
	if b.MCP == nil && len(edits.AddMCP) > 0 {
		b.MCP = make(map[string]bundles.BundleMCP)
	}
	for _, mcpName := range edits.AddMCP {
		if _, exists := b.MCP[mcpName]; exists {
			w.Printf("MCP server already exists: %s\n", mcpName)
			continue
		}
		b.MCP[mcpName] = bundles.BundleMCP{Command: mcpName}
		modified = true
		w.Printf("Added MCP server: %s\n", mcpName)
	}
	for _, mcpName := range edits.RemoveMCP {
		if b.MCP == nil {
			continue
		}
		if _, exists := b.MCP[mcpName]; !exists {
			w.Printf("MCP server not found: %s\n", mcpName)
			continue
		}
		delete(b.MCP, mcpName)
		modified = true
		w.Printf("Removed MCP server: %s\n", mcpName)
	}
	return modified
}

func sliceContains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func sliceRemove(slice []string, item string) []string {
	result := make([]string, 0, len(slice))
	for _, s := range slice {
		if s != item {
			result = append(result, s)
		}
	}
	return result
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

	bundleDirs := cfg.GetBundleDirs()
	if len(bundleDirs) == 0 {
		return fmt.Errorf("no bundles directory found")
	}

	// Load read-only just to show the path in the confirm prompt; the actual
	// (guarded) removal goes through operations.DeleteBundle.
	loader := bundles.NewLoader(bundleDirs, false)
	bundle, err := loader.Load(name)
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

