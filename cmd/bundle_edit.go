package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/iox"
	"github.com/ctxloom/ctxloom/internal/paths"
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

	bundleDir := paths.BundlesPath(cfg.AppPaths[0])
	bundlePath, err := writeBundleSkeleton(afero.NewOsFs(), bundleDir, name, bundleCreateDesc)
	if err != nil {
		return err
	}

	w := iox.NewErrWriter(cmd.OutOrStdout())
	w.Printf("Created bundle: %s\n", bundlePath)
	w.Println("Edit the file to add your fragments and prompts.")

	return w.Err()
}

// writeBundleSkeleton creates a starter bundle YAML at <bundleDir>/<name>.yaml
// on fs, refusing to overwrite an existing file. Returns the path it
// wrote (for the user-visible success line) and any error encountered.
// Extracted from runBundleCreate so the "already exists" guard and the
// initial bundle shape are testable without a real filesystem.
func writeBundleSkeleton(fs afero.Fs, bundleDir, name, description string) (string, error) {
	if err := fs.MkdirAll(bundleDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create bundles directory: %w", err)
	}

	bundlePath := filepath.Join(bundleDir, name+".yaml")
	if _, err := fs.Stat(bundlePath); err == nil {
		return "", fmt.Errorf("bundle already exists: %s", bundlePath)
	}

	bundle := bundles.Bundle{
		Version:     "1.0.0",
		Description: description,
		Tags:        []string{},
		Fragments: map[string]bundles.BundleFragment{
			"example": {
				Tags:    []string{"example"},
				Content: "# Example Fragment\n\nAdd your content here.",
			},
		},
		Prompts: map[string]bundles.BundlePrompt{
			"example": {
				Description: "Example prompt",
				Tags:        []string{"example"},
				Content:     "Example prompt content. Describe what this prompt does.",
			},
		},
	}
	data, err := yaml.Marshal(&bundle)
	if err != nil {
		return "", fmt.Errorf("failed to marshal bundle: %w", err)
	}
	if err := afero.WriteFile(fs, bundlePath, data, 0644); err != nil {
		return "", fmt.Errorf("failed to write bundle: %w", err)
	}
	return bundlePath, nil
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
	modified := false

	if edits.Description != "" {
		b.Description = edits.Description
		modified = true
	}
	if edits.Version != "" {
		b.Version = edits.Version
		modified = true
	}

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

	loader := bundles.NewLoader(bundleDirs, false)
	bundle, err := loader.Load(name)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", name)
	}

	confirm := stdinConfirmer(cmd.InOrStdin())
	return deleteBundleFile(afero.NewOsFs(), name, bundle.Path, bundleDeleteForce, confirm, cmd.OutOrStdout())
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

// deleteBundleFile removes the bundle file at path on fs. When force is
// false, confirm is consulted with a "Delete bundle... [y/N]" prompt
// first; a negative response prints "Cancelled." to out and returns nil
// without touching the file. Returns an error iff the file removal
// itself failed.
//
// Extracted from runBundleDelete so the confirm-gating decision and the
// cancellation message are testable without driving stdin.
func deleteBundleFile(fs afero.Fs, name, path string, force bool, confirm confirmFn, out io.Writer) error {
	w := iox.NewErrWriter(out)
	if !force {
		prompt := fmt.Sprintf("Delete bundle %q at %s? [y/N] ", name, path)
		if !confirm(prompt) {
			w.Println("Cancelled.")
			return w.Err()
		}
	}
	if err := fs.Remove(path); err != nil {
		return fmt.Errorf("failed to delete bundle: %w", err)
	}
	w.Printf("Deleted bundle: %s\n", path)
	return w.Err()
}
