package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/compression"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/iox"
	"github.com/ctxloom/ctxloom/internal/paths"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/remote"
)

var bundleCmd = &cobra.Command{
	Use:    "bundle",
	Short:  "Manage ctxloom bundles",
	Hidden: true, // Use fragment/prompt commands for content management
	Long: `Manage ctxloom bundles - versioned collections of fragments, prompts, and MCP servers.

Bundles are the primary content unit in ctxloom. They group related context fragments,
prompts, and optional MCP server configurations with a single version.

Examples:
  ctxloom bundle list                  # List all installed bundles
  ctxloom bundle show go-tools         # Show bundle contents
  ctxloom bundle create my-bundle      # Create a new bundle
  ctxloom bundle export go-tools ./out # Export bundle to directory
  ctxloom bundle import ./my-bundle.yaml # Import bundle from file`,
}

var bundleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed bundles",
	Long: `List all bundles installed in the .ctxloom/bundles directory.

Shows bundle name, version, description, and content summary.`,
	RunE: runBundleList,
}

func runBundleList(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	bundleDirs := cfg.GetBundleDirs()
	out := cmd.OutOrStdout()
	if len(bundleDirs) == 0 {
		w := iox.NewErrWriter(out)
		w.Println("No bundles directory found. Create one with: mkdir -p .ctxloom/bundles")
		return w.Err()
	}

	loader := bundles.NewLoader(bundleDirs, false)
	bundleInfos, err := loader.List()
	if err != nil {
		return fmt.Errorf("failed to list bundles: %w", err)
	}

	return renderBundleList(out, bundleInfos)
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
		w.Println("Install bundles with: ctxloom install <remote>/bundle-name")
		return w.Err()
	}

	w.Printf("Installed bundles (%d):\n\n", len(infos))
	for _, info := range infos {
		w.Printf("  %s", info.Name)
		if info.Version != "" {
			w.Printf(" (v%s)", info.Version)
		}
		w.Println()

		if info.Description != "" {
			w.Printf("    %s\n", info.Description)
		}

		var parts []string
		if info.FragmentCount > 0 {
			parts = append(parts, fmt.Sprintf("%d fragments", info.FragmentCount))
		}
		if info.PromptCount > 0 {
			parts = append(parts, fmt.Sprintf("%d prompts", info.PromptCount))
		}
		if info.MCPCount > 0 {
			if info.MCPCount == 1 {
				parts = append(parts, "1 MCP server")
			} else {
				parts = append(parts, fmt.Sprintf("%d MCP servers", info.MCPCount))
			}
		}
		if len(parts) > 0 {
			w.Printf("    Contains: %s\n", strings.Join(parts, ", "))
		}
		if len(info.Tags) > 0 {
			w.Printf("    Tags: %s\n", strings.Join(info.Tags, ", "))
		}
		w.Println()
	}
	return w.Err()
}

var bundleShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show bundle contents",
	Long: `Display detailed information about a bundle.

Shows all fragments, prompts, and MCP server configuration contained in the bundle.`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleShow,
}

func runBundleShow(cmd *cobra.Command, args []string) error {
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

	return renderBundleShow(cmd.OutOrStdout(), bundle)
}

// renderBundleShow writes the detailed bundle view to out. Sections
// (MCP/Fragments/Prompts/Notes) are suppressed when empty. Fragment and
// prompt entries are decorated with their tag list and a `(distilled)`
// or `(no_distill)` marker; fragments also get a 70-char first-line
// preview, prompts get the optional Description.
func renderBundleShow(out io.Writer, bundle *bundles.Bundle) error {
	w := iox.NewErrWriter(out)
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

	if len(bundle.Prompts) > 0 {
		w.Printf("Prompts (%d):\n", len(bundle.Prompts))
		for _, name := range bundle.PromptNames() {
			renderBundlePromptEntry(w, name, bundle.Prompts[name])
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
		for k, v := range mcp.Env {
			w.Printf("        %s=%s\n", k, v)
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

	firstLine := strings.Split(strings.TrimSpace(frag.Content), "\n")[0]
	if len(firstLine) > 70 {
		firstLine = firstLine[:67] + "..."
	}
	w.Printf("      %s\n", firstLine)
}

func renderBundlePromptEntry(w *iox.ErrWriter, name string, prompt bundles.BundlePrompt) {
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
		Description:    bundleEditDesc,
		Version:        bundleEditVersion,
		AddTags:        bundleEditAddTags,
		RemoveTags:     bundleEditRemoveTags,
		AddFragments:   bundleEditAddFragment,
		RemoveFragments: bundleEditRemoveFragment,
		AddPrompts:     bundleEditAddPrompt,
		RemovePrompts:  bundleEditRemovePrompt,
		AddMCP:         bundleEditAddMCP,
		RemoveMCP:      bundleEditRemoveMCP,
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

var (
	bundlePushPR      bool
	bundlePushBranch  string
	bundlePushMessage string
)

var bundlePushCmd = &cobra.Command{
	Use:   "push <name> [remote]",
	Short: "Publish a bundle to a remote repository",
	Long: `Publish a local bundle to a remote repository.

By default, publishes directly to the default branch. Use --pr to create
a pull request instead.

If no remote is specified, uses the default remote.

Examples:
  ctxloom bundle push my-bundle
  ctxloom bundle push my-bundle ctxloom-default
  ctxloom bundle push my-bundle --pr
  ctxloom bundle push my-bundle ctxloom-default --message "Add my bundle"`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runBundlePush,
}

func runBundlePush(cmd *cobra.Command, args []string) error {
	bundleName := args[0]
	remoteName := ""
	if len(args) > 1 {
		remoteName = args[1]
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Load the bundle
	bundleDirs := cfg.GetBundleDirs()
	if len(bundleDirs) == 0 {
		return fmt.Errorf("no bundles directory found")
	}

	loader := bundles.NewLoader(bundleDirs, false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	// Initialize registry
	registry, err := remote.NewRegistry("")
	if err != nil {
		return fmt.Errorf("failed to initialize registry: %w", err)
	}

	// Use default remote if not specified
	if remoteName == "" {
		remoteName = registry.GetDefault()
		if remoteName == "" {
			return fmt.Errorf("no remote specified and no default set. Use: ctxloom bundle push <name> <remote>")
		}
	}

	auth := remote.LoadAuth("")

	// Build publish options
	opts := remote.PublishOptions{
		CreatePR: bundlePushPR,
		Branch:   bundlePushBranch,
		Message:  bundlePushMessage,
		ItemType: remote.ItemTypeBundle,
	}

	fmt.Printf("Publishing bundle %q to %s...\n", bundleName, remoteName)

	pm := remote.NewPublishManager(registry, auth)
	result, err := pm.Publish(cmd.Context(), bundle.Path, remoteName, opts)
	if err != nil {
		return err
	}

	if result.PRURL != "" {
		fmt.Printf("Created pull request: %s\n", result.PRURL)
	} else {
		action := "Created"
		if !result.Created {
			action = "Updated"
		}
		fmt.Printf("%s %s\n", action, result.Path)
		fmt.Printf("Commit: %s\n", result.SHA[:7])
	}

	return nil
}

var bundleExportOutput string

var bundleExportCmd = &cobra.Command{
	Use:   "export <name> [dest-dir]",
	Short: "Export a bundle to a file or directory",
	Long: `Export a bundle from .ctxloom/bundles to a file or directory.

Useful for publishing bundles to a shared repository like ctxloom-default.
The bundle is copied as-is, preserving all content including distilled versions.

Use -o to specify an output file path directly.

Examples:
  ctxloom bundle export go-tools ../ctxloom-default/ctxloom/v1/bundles
  ctxloom bundle export my-bundle ./exports
  ctxloom bundle export my-bundle -o exported.yaml`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runBundleExport,
}

func runBundleExport(cmd *cobra.Command, args []string) error {
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

	destDir := ""
	if len(args) > 1 {
		destDir = args[1]
	}
	destPath, err := exportBundleFile(afero.NewOsFs(), bundle.Path, bundleExportOutput, destDir)
	if err != nil {
		return err
	}

	w := iox.NewErrWriter(cmd.OutOrStdout())
	w.Printf("Exported: %s -> %s\n", bundle.Path, destPath)
	return w.Err()
}

// exportBundleFile copies srcPath to a destination chosen by the
// outputFile / destDir pair. Exactly one must be non-empty. The
// destination directory (or parent dir of outputFile) is created if
// missing. Returns the resolved destination path on success.
//
// Extracted from runBundleExport so the (-o <file> vs <dest-dir>)
// dispatch and the missing-arg error are testable against a MemMapFs.
func exportBundleFile(fs afero.Fs, srcPath, outputFile, destDir string) (string, error) {
	srcData, err := afero.ReadFile(fs, srcPath)
	if err != nil {
		return "", fmt.Errorf("failed to read bundle: %w", err)
	}

	var destPath string
	switch {
	case outputFile != "":
		destPath = outputFile
		if dir := filepath.Dir(destPath); dir != "." {
			if err := fs.MkdirAll(dir, 0755); err != nil {
				return "", fmt.Errorf("failed to create destination directory: %w", err)
			}
		}
	case destDir != "":
		if err := fs.MkdirAll(destDir, 0755); err != nil {
			return "", fmt.Errorf("failed to create destination directory: %w", err)
		}
		destPath = filepath.Join(destDir, filepath.Base(srcPath))
	default:
		return "", fmt.Errorf("either -o <file> or <dest-dir> must be specified")
	}

	if err := afero.WriteFile(fs, destPath, srcData, 0644); err != nil {
		return "", fmt.Errorf("failed to write bundle: %w", err)
	}
	return destPath, nil
}

var bundleImportForce bool

var bundleImportCmd = &cobra.Command{
	Use:   "import <path>",
	Short: "Import a bundle from a local file",
	Long: `Import a bundle from a local YAML file into .ctxloom/bundles.

The bundle is copied into the local .ctxloom/bundles directory.
Use --force to overwrite an existing bundle.

Examples:
  ctxloom bundle import ../ctxloom-default/ctxloom/v1/bundles/go-tools.yaml
  ctxloom bundle import ./my-bundle.yaml --force`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleImport,
}

func runBundleImport(cmd *cobra.Command, args []string) error {
	srcPath := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	bundleDir := paths.BundlesPath(cfg.AppPaths[0])
	destPath, bundle, err := importBundleFile(afero.NewOsFs(), srcPath, bundleDir, bundleImportForce)
	if err != nil {
		return err
	}

	w := iox.NewErrWriter(cmd.OutOrStdout())
	w.Printf("Imported: %s -> %s\n", srcPath, destPath)
	w.Printf("  Version: %s\n", bundle.Version)
	w.Printf("  Fragments: %d, Prompts: %d, MCP: %d\n", len(bundle.Fragments), len(bundle.Prompts), len(bundle.MCP))

	return w.Err()
}

// importBundleFile reads srcPath via fs, validates it parses as a bundle,
// then copies it into bundleDir (creating bundleDir if needed). force=false
// errors when the destination exists. Returns the destination path and the
// parsed bundle (for caller-side summary output) on success.
//
// Extracted from runBundleImport so the validation + overwrite-guard
// decision tree is testable against a MemMapFs without touching real
// .ctxloom/bundles/.
func importBundleFile(fs afero.Fs, srcPath, bundleDir string, force bool) (string, *bundles.Bundle, error) {
	srcData, err := afero.ReadFile(fs, srcPath)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read source file: %w", err)
	}

	bundle, err := bundles.ParseBundle(srcData)
	if err != nil {
		return "", nil, fmt.Errorf("invalid bundle file: %w", err)
	}

	if err := fs.MkdirAll(bundleDir, 0755); err != nil {
		return "", nil, fmt.Errorf("failed to create bundles directory: %w", err)
	}

	destPath := filepath.Join(bundleDir, filepath.Base(srcPath))
	if _, err := fs.Stat(destPath); err == nil && !force {
		return "", nil, fmt.Errorf("bundle already exists: %s (use --force to overwrite)", destPath)
	}

	if err := afero.WriteFile(fs, destPath, srcData, 0644); err != nil {
		return "", nil, fmt.Errorf("failed to write bundle: %w", err)
	}
	return destPath, bundle, nil
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

	bundleDirs := cfg.GetBundleDirs()
	if len(bundleDirs) == 0 {
		return fmt.Errorf("no bundles directory found")
	}

	loader := bundles.NewLoader(bundleDirs, false)

	bundleName, itemPath := parseBundleViewRef(ref)

	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	out := cmd.OutOrStdout()

	// If no path, show full bundle YAML.
	if itemPath == "" {
		data, err := os.ReadFile(bundle.Path)
		if err != nil {
			return fmt.Errorf("failed to read bundle: %w", err)
		}
		_, _ = out.Write(data)
		return nil
	}

	return renderBundleViewItem(out, bundle, itemPath, bundleViewDistilled)
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

var bundleDistillForce bool
var bundleDistillDryRun bool
var bundleDistillPlugin string

var bundleDistillCmd = &cobra.Command{
	Use:   "distill <file-pattern>...",
	Short: "Distill bundle files to create token-efficient versions",
	Long: `Distill bundle files to create minimal-token versions that preserve meaning.

This command processes each fragment and prompt in the bundle through an LLM
to create a compressed version. The distilled content, content hash, and
model info are written back to the bundle file.

Supports glob patterns to process multiple files at once.

Examples:
  ctxloom bundle distill ./my-bundle.yaml                    # Single file
  ctxloom bundle distill .ctxloom/bundles/*.yaml                 # All bundles in directory
  ctxloom bundle distill .ctxloom/bundles/**/*.yaml              # Recursive
  ctxloom bundle distill bundle1.yaml bundle2.yaml           # Multiple files
  ctxloom bundle distill ./my-bundle.yaml --force            # Re-distill all items
  ctxloom bundle distill ./my-bundle.yaml --dry-run          # Preview what would be distilled`,
	Args: cobra.MinimumNArgs(1),
	RunE: runBundleDistill,
}

func runBundleDistill(cmd *cobra.Command, args []string) error {
	// Expand glob patterns to get list of files
	var files []string
	for _, pattern := range args {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("invalid pattern %q: %w", pattern, err)
		}
		if len(matches) == 0 {
			// Try as literal path
			if _, err := os.Stat(pattern); err == nil {
				files = append(files, pattern)
			} else {
				fmt.Fprintf(os.Stderr, "Warning: no files match %q\n", pattern)
			}
		} else {
			files = append(files, matches...)
		}
	}

	if len(files) == 0 {
		return fmt.Errorf("no files found matching patterns")
	}

	// Load config for plugin settings
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Determine plugin to use
	pluginName := bundleDistillPlugin
	if pluginName == "" {
		pluginName = cfg.GetDefaultLLMPlugin()
	}

	// Get plugin env config
	pluginCfg := cfg.LM.Plugins[pluginName]

	// Load distill prompt
	distillPrompt, err := loadDistillPrompt()
	if err != nil {
		return err
	}

	// Process each file
	totalFiles := 0
	totalItems := 0
	totalSkipped := 0

	for _, filePath := range files {
		// Read and parse bundle
		data, err := os.ReadFile(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", filePath, err)
			continue
		}

		bundle, err := bundles.ParseBundle(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing %s: %v\n", filePath, err)
			continue
		}
		bundle.Path = filePath

		fmt.Printf("Processing: %s\n", filePath)
		modified := false

		// Distill each fragment
		for name, frag := range bundle.Fragments {
			if frag.NoDistill {
				fmt.Printf("  Skipping fragment %s (no_distill)\n", name)
				totalSkipped++
				continue
			}

			if !bundleDistillForce && !frag.NeedsDistill() {
				fmt.Printf("  Skipping fragment %s (unchanged)\n", name)
				totalSkipped++
				continue
			}

			if bundleDistillDryRun {
				fmt.Printf("  Would distill fragment: %s\n", name)
				totalItems++
				continue
			}

			fmt.Printf("  Distilling fragment: %s...", name)

			// Build sibling context
			siblingCtx := buildSiblingContext(bundle, "fragments/"+name)

			distilled, modelID, err := distillWithModel(pluginName, pluginCfg.Env, name, frag.Content, distillPrompt, siblingCtx)
			if err != nil {
				fmt.Printf(" FAILED: %v\n", err)
				continue
			}

			frag.Distilled = distilled
			frag.ContentHash = frag.ComputeContentHash()
			frag.DistilledBy = modelID
			bundle.Fragments[name] = frag
			modified = true
			totalItems++

			fmt.Printf(" OK (%s)\n", modelID)
		}

		// Distill each prompt
		for name, prompt := range bundle.Prompts {
			if prompt.NoDistill {
				fmt.Printf("  Skipping prompt %s (no_distill)\n", name)
				totalSkipped++
				continue
			}

			if !bundleDistillForce && !prompt.NeedsDistill() {
				fmt.Printf("  Skipping prompt %s (unchanged)\n", name)
				totalSkipped++
				continue
			}

			if bundleDistillDryRun {
				fmt.Printf("  Would distill prompt: %s\n", name)
				totalItems++
				continue
			}

			fmt.Printf("  Distilling prompt: %s...", name)

			// Build sibling context
			siblingCtx := buildSiblingContext(bundle, "prompts/"+name)

			distilled, modelID, err := distillWithModel(pluginName, pluginCfg.Env, name, prompt.Content, distillPrompt, siblingCtx)
			if err != nil {
				fmt.Printf(" FAILED: %v\n", err)
				continue
			}

			prompt.Distilled = distilled
			prompt.ContentHash = prompt.ComputeContentHash()
			prompt.DistilledBy = modelID
			bundle.Prompts[name] = prompt
			modified = true
			totalItems++

			fmt.Printf(" OK (%s)\n", modelID)
		}

		// Save bundle if modified
		if modified && !bundleDistillDryRun {
			if err := bundle.Save(); err != nil {
				fmt.Fprintf(os.Stderr, "  Error saving %s: %v\n", filePath, err)
				continue
			}
			totalFiles++
		}
	}

	// Summary
	if bundleDistillDryRun {
		fmt.Printf("\nDry run: would distill %d items\n", totalItems)
	} else {
		var parts []string
		if totalItems > 0 {
			parts = append(parts, fmt.Sprintf("distilled %d items in %d files", totalItems, totalFiles))
		}
		if totalSkipped > 0 {
			parts = append(parts, fmt.Sprintf("skipped %d", totalSkipped))
		}
		if len(parts) > 0 {
			fmt.Printf("\n%s\n", strings.Join(parts, ", "))
		} else {
			fmt.Println("\nNo items to distill.")
		}
	}

	return nil
}

// defaultDistillPrompt is used when no distill prompt is found in bundles.
const defaultDistillPrompt = `You are a context compression assistant for AI coding assistants.

TASK: Compress the content by removing unimportant words while preserving meaning.

PRESERVE (never remove):
- Code syntax and exact patterns
- Function/file/variable names (breadcrumbs for navigation)
- Error handling rules and edge cases
- Actionable instructions ("DO X", "NEVER do Y")
- Technical constraints and requirements

COMPRESS AGGRESSIVELY:
- Verbose explanations of "why"
- Redundant examples (keep 1 best example per concept)
- Motivational/philosophical content
- Historical context unless directly actionable

RULES:
- Use bullet points and abbreviations where clear
- Do NOT add new information or rephrase semantics
- Output format: same structure, fewer words
- Target: 30-50% of original size

Output only the compressed content.`

// loadDistillPrompt loads the distillation prompt from bundles.
func loadDistillPrompt() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return defaultDistillPrompt, nil
	}

	// Try to load "distill" prompt from bundles
	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	prompt, err := loader.GetPrompt("distill")
	if err == nil && prompt.Content != "" {
		return strings.TrimSpace(prompt.Content), nil
	}

	// Use default prompt
	return defaultDistillPrompt, nil
}

// buildSiblingContext creates context about sibling items in a bundle.
func buildSiblingContext(bundle *bundles.Bundle, excludeName string) string {
	var ctx strings.Builder

	fmt.Fprintf(&ctx, "Bundle: %s", bundle.Description)
	if bundle.Version != "" {
		fmt.Fprintf(&ctx, " (v%s)", bundle.Version)
	}
	ctx.WriteString("\n")

	if len(bundle.Tags) > 0 {
		ctx.WriteString("Tags: ")
		ctx.WriteString(strings.Join(bundle.Tags, ", "))
		ctx.WriteString("\n")
	}
	ctx.WriteString("\n")

	// List sibling fragments
	if len(bundle.Fragments) > 1 || (len(bundle.Fragments) == 1 && !strings.HasPrefix(excludeName, "fragments/")) {
		ctx.WriteString("Sibling fragments:\n")
		for name, frag := range bundle.Fragments {
			if "fragments/"+name == excludeName {
				continue
			}
			firstLine := strings.Split(strings.TrimSpace(frag.Content), "\n")[0]
			if len(firstLine) > 60 {
				firstLine = firstLine[:57] + "..."
			}
			fmt.Fprintf(&ctx, "- %s: %s\n", name, firstLine)
		}
		ctx.WriteString("\n")
	}

	// List sibling prompts
	if len(bundle.Prompts) > 1 || (len(bundle.Prompts) == 1 && !strings.HasPrefix(excludeName, "prompts/")) {
		ctx.WriteString("Sibling prompts:\n")
		for name, prompt := range bundle.Prompts {
			if "prompts/"+name == excludeName {
				continue
			}
			desc := prompt.Description
			if desc == "" {
				desc = strings.Split(strings.TrimSpace(prompt.Content), "\n")[0]
				if len(desc) > 60 {
					desc = desc[:57] + "..."
				}
			}
			fmt.Fprintf(&ctx, "- %s: %s\n", name, desc)
		}
	}

	return ctx.String()
}

// compressionRouter is a shared router for AST/JSON compression.
var compressionRouter = compression.NewRouter()

// distillWithModel sends content through compression and returns distilled content and model ID.
// It first tries AST-based compression for code and JSON structure compression for JSON content.
// For text/markdown content (or if AST compression doesn't achieve good compression), it falls back to LLM.
func distillWithModel(pluginName string, env map[string]string, name, content, distillPrompt, siblingCtx string) (string, string, error) {
	ctx := context.Background()

	// Detect content type and try AST/JSON compression first
	contentType := compression.DetectContentType(name, content)

	// For code and JSON, try fast local compression
	if isStructuredContent(contentType) {
		result, err := compressionRouter.CompressWithType(ctx, contentType, content, 0.5)
		if err == nil && result.Ratio < 0.7 {
			// Good compression achieved with AST/JSON - use it
			return result.Content, result.ModelID, nil
		}
		// If compression didn't achieve good ratio or failed, fall back to LLM
	}

	// For text content or when AST compression isn't effective, use LLM
	return distillWithLLM(pluginName, env, name, content, distillPrompt, siblingCtx)
}

// isStructuredContent returns true for content types that can be compressed structurally.
func isStructuredContent(ct compression.ContentType) bool {
	switch ct {
	case compression.ContentTypeGo, compression.ContentTypePython, compression.ContentTypeJavaScript,
		compression.ContentTypeTypeScript, compression.ContentTypeRust, compression.ContentTypeJava,
		compression.ContentTypeJSON:
		return true
	}
	return false
}

// distillWithLLM sends content through the LLM and returns distilled content and model ID.
func distillWithLLM(pluginName string, env map[string]string, name, content, distillPrompt, siblingCtx string) (string, string, error) {
	// Build content to distill
	var builder strings.Builder

	if siblingCtx != "" {
		builder.WriteString("<bundle_context>\n")
		builder.WriteString(siblingCtx)
		builder.WriteString("\n</bundle_context>\n\n")
		builder.WriteString("CONTEXT-AWARE COMPRESSION:\n")
		builder.WriteString("- The bundle_context shows sibling items that will be loaded together\n")
		builder.WriteString("- DO NOT repeat concepts already covered by siblings - reference them instead\n")
		builder.WriteString("- Compress knowing this content will be loaded alongside those siblings\n\n")
	}

	builder.WriteString("<content_to_compress>\n# ")
	builder.WriteString(name)
	builder.WriteString("\n\n")
	builder.WriteString(content)
	builder.WriteString("\n</content_to_compress>")

	// Create plugin client
	client, err := pb.NewSelfInvokingClient(pluginName, 0)
	if err != nil {
		return "", "", fmt.Errorf("failed to start plugin: %w", err)
	}
	defer client.Kill()

	// Build request
	req := &pb.RunRequest{
		Prompt: &pb.Fragment{
			Content: builder.String(),
		},
		Fragments: []*pb.Fragment{
			{Content: distillPrompt},
		},
		Options: &pb.RunOptions{
			AutoApprove: true,
			Mode:        pb.ExecutionMode_ONESHOT,
			Env:         env,
		},
	}

	// Execute and capture model info
	var stdout, stderr bytes.Buffer
	result, err := client.RunWithModelInfo(context.Background(), req, &stdout, &stderr)
	if err != nil {
		return "", "", err
	}

	if result.ExitCode != 0 {
		return "", "", fmt.Errorf("LLM exited with code %d: %s", result.ExitCode, stderr.String())
	}

	// Build model ID from model info
	modelID := pluginName
	if result.ModelInfo != nil {
		if result.ModelInfo.ModelName != "" {
			modelID = result.ModelInfo.ModelName
		}
		if result.ModelInfo.ModelVersion != "" {
			modelID = fmt.Sprintf("%s:%s", modelID, result.ModelInfo.ModelVersion)
		}
	}

	// Clean up distilled content
	distilled := cleanDistilledOutput(strings.TrimSpace(stdout.String()))

	return distilled, modelID, nil
}

// preambleRe matches markdown horizontal rules/separators
var preambleRe = regexp.MustCompile(`(?m)^-{3,}\s*$`)

// codeFenceRe matches opening code fences
var codeFenceRe = regexp.MustCompile("^```[a-z]*\\s*\n")

// conversationalStarts are patterns LLMs add despite instructions
var conversationalStarts = []string{
	"here's ", "here is ", "below is ", "below you'll find ",
	"the compressed version", "the following ",
	"i've compressed ", "i have compressed ", "my compressed version",
}

// cleanDistilledOutput removes LLM preamble artifacts.
func cleanDistilledOutput(content string) string {
	content = strings.TrimSpace(content)
	foundPreamble := false

	// Check for conversational prefixes
	lower := strings.ToLower(content)
	for _, prefix := range conversationalStarts {
		if strings.HasPrefix(lower, prefix) {
			foundPreamble = true
			if idx := strings.Index(content, "\n"); idx != -1 {
				content = strings.TrimSpace(content[idx+1:])
			}
			break
		}
	}

	// Strip separator if preamble was found
	if foundPreamble {
		if loc := preambleRe.FindStringIndex(content); loc != nil && loc[0] < 100 {
			after := content[loc[1]:]
			if len(after) > 0 && after[0] == '\n' {
				after = after[1:]
			}
			content = strings.TrimSpace(after)
		}
	}

	// Strip code fence if present
	if loc := codeFenceRe.FindStringIndex(content); loc != nil && loc[0] == 0 {
		content = content[loc[1]:]
		if idx := strings.LastIndex(content, "```"); idx != -1 {
			if strings.TrimSpace(content[idx+3:]) == "" {
				content = strings.TrimSpace(content[:idx])
			}
		}
	}

	return content
}

// ============ Bundle Fragment Commands ============

var bundleFragmentCmd = &cobra.Command{
	Use:   "fragment",
	Short: "Manage fragments within a bundle",
	Long:  `Commands for managing fragments within a bundle.`,
}

var bundleFragmentListCmd = &cobra.Command{
	Use:   "list <bundle-name>",
	Short: "List fragments in a bundle",
	Long: `List all fragments in a specific bundle.

Examples:
  ctxloom bundle fragment list my-bundle
  ctxloom bundle fragment list go-tools`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleFragmentList,
}

func runBundleFragmentList(cmd *cobra.Command, args []string) error {
	bundleName := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	return renderBundleFragmentList(cmd.OutOrStdout(), bundle)
}

// renderBundleFragmentList writes one line per fragment to out:
// `<name>[ [tag1, tag2]]`. Empty bundle gets a helpful one-line hint.
// Iteration order follows the map iteration order (intentionally
// unordered — the show command's FragmentNames() produces a stable
// sort instead).
func renderBundleFragmentList(out io.Writer, bundle *bundles.Bundle) error {
	w := iox.NewErrWriter(out)
	if len(bundle.Fragments) == 0 {
		w.Println("No fragments in this bundle.")
		return w.Err()
	}
	for name, frag := range bundle.Fragments {
		w.Printf("%s", name)
		if len(frag.Tags) > 0 {
			w.Printf(" [%s]", strings.Join(frag.Tags, ", "))
		}
		w.Println()
	}
	return w.Err()
}

var bundleFragmentEditCmd = &cobra.Command{
	Use:   "edit <bundle-name> <fragment-name>",
	Short: "Edit a fragment's content",
	Long: `Edit a fragment's content using your configured editor.

Opens the fragment content in your editor. When you save and close,
the bundle is updated with the new content.

Examples:
  ctxloom bundle fragment edit my-bundle coding-standards
  ctxloom bundle fragment edit go-tools golang`,
	Args: cobra.ExactArgs(2),
	RunE: runBundleFragmentEdit,
}

func runBundleFragmentEdit(cmd *cobra.Command, args []string) error {
	bundleName, fragName := args[0], args[1]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	frag, exists := bundle.Fragments[fragName]
	if !exists {
		return fmt.Errorf("fragment not found: %s", fragName)
	}

	// Edit content using editor
	newContent, err := editInEditor(cfg, frag.Content, fragName+".md")
	if err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}

	if newContent == frag.Content {
		fmt.Println("No changes made.")
		return nil
	}

	// Update fragment content
	frag.Content = newContent
	bundle.Fragments[fragName] = frag

	// Auto-distill if not marked as no_distill
	if !frag.NoDistill {
		fmt.Printf("Distilling %s...", fragName)

		// Load distill prompt
		distillPrompt, err := loadDistillPrompt()
		if err != nil {
			fmt.Printf(" skipped (no distill prompt)\n")
		} else {
			// Get plugin config
			pluginName := cfg.GetDefaultLLMPlugin()
			pluginCfg := cfg.LM.Plugins[pluginName]

			// Build sibling context
			siblingCtx := buildSiblingContext(bundle, "fragments/"+fragName)

			distilled, modelID, err := distillWithModel(pluginName, pluginCfg.Env, fragName, frag.Content, distillPrompt, siblingCtx)
			if err != nil {
				fmt.Printf(" failed: %v\n", err)
			} else {
				frag.Distilled = distilled
				frag.DistilledBy = modelID
				frag.ContentHash = frag.ComputeContentHash()
				bundle.Fragments[fragName] = frag
				fmt.Printf(" done\n")
			}
		}
	}

	if err := bundle.Save(); err != nil {
		return fmt.Errorf("failed to save bundle: %w", err)
	}

	fmt.Printf("Updated fragment %q in bundle %q\n", fragName, bundleName)
	return nil
}

// ============ Bundle Prompt Commands ============

var bundlePromptCmd = &cobra.Command{
	Use:   "prompt",
	Short: "Manage prompts within a bundle",
	Long:  `Commands for managing prompts within a bundle.`,
}

var bundlePromptListCmd = &cobra.Command{
	Use:   "list <bundle-name>",
	Short: "List prompts in a bundle",
	Long: `List all prompts in a specific bundle.

Examples:
  ctxloom bundle prompt list my-bundle
  ctxloom bundle prompt list go-tools`,
	Args: cobra.ExactArgs(1),
	RunE: runBundlePromptList,
}

func runBundlePromptList(cmd *cobra.Command, args []string) error {
	bundleName := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	return renderBundlePromptList(cmd.OutOrStdout(), bundle)
}

// renderBundlePromptList writes one line per prompt name to out, or a
// one-line hint when the bundle has no prompts. Names only — tag and
// description rendering live in renderBundlePromptEntry (used by show).
func renderBundlePromptList(out io.Writer, bundle *bundles.Bundle) error {
	w := iox.NewErrWriter(out)
	if len(bundle.Prompts) == 0 {
		w.Println("No prompts in this bundle.")
		return w.Err()
	}
	for name := range bundle.Prompts {
		w.Println(name)
	}
	return w.Err()
}

var bundlePromptEditCmd = &cobra.Command{
	Use:   "edit <bundle-name> <prompt-name>",
	Short: "Edit a prompt's content",
	Long: `Edit a prompt's content using your configured editor.

Opens the prompt content in your editor. When you save and close,
the bundle is updated with the new content.

Examples:
  ctxloom bundle prompt edit my-bundle code-review
  ctxloom bundle prompt edit go-tools refactor`,
	Args: cobra.ExactArgs(2),
	RunE: runBundlePromptEdit,
}

func runBundlePromptEdit(cmd *cobra.Command, args []string) error {
	bundleName, promptName := args[0], args[1]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	prompt, exists := bundle.Prompts[promptName]
	if !exists {
		return fmt.Errorf("prompt not found: %s", promptName)
	}

	// Edit content using editor
	newContent, err := editInEditor(cfg, prompt.Content, promptName+".md")
	if err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}

	if newContent == prompt.Content {
		fmt.Println("No changes made.")
		return nil
	}

	// Update prompt content
	prompt.Content = newContent
	bundle.Prompts[promptName] = prompt

	// Auto-distill if not marked as no_distill
	if !prompt.NoDistill {
		fmt.Printf("Distilling %s...", promptName)

		// Load distill prompt
		distillPrompt, err := loadDistillPrompt()
		if err != nil {
			fmt.Printf(" skipped (no distill prompt)\n")
		} else {
			// Get plugin config
			pluginName := cfg.GetDefaultLLMPlugin()
			pluginCfg := cfg.LM.Plugins[pluginName]

			// Build sibling context
			siblingCtx := buildSiblingContext(bundle, "prompts/"+promptName)

			distilled, modelID, err := distillWithModel(pluginName, pluginCfg.Env, promptName, prompt.Content, distillPrompt, siblingCtx)
			if err != nil {
				fmt.Printf(" failed: %v\n", err)
			} else {
				prompt.Distilled = distilled
				prompt.DistilledBy = modelID
				prompt.ContentHash = prompt.ComputeContentHash()
				bundle.Prompts[promptName] = prompt
				fmt.Printf(" done\n")
			}
		}
	}

	if err := bundle.Save(); err != nil {
		return fmt.Errorf("failed to save bundle: %w", err)
	}

	fmt.Printf("Updated prompt %q in bundle %q\n", promptName, bundleName)
	return nil
}

// ============ Bundle MCP Commands ============

var bundleMCPCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage MCP servers within a bundle",
	Long:  `Commands for managing MCP server configurations within a bundle.`,
}

var bundleMCPEditCmd = &cobra.Command{
	Use:   "edit <bundle-name> <mcp-name>",
	Short: "Edit an MCP server configuration",
	Long: `Edit an MCP server's configuration using your configured editor.

Opens the MCP server config as YAML in your editor. When you save and close,
the bundle is updated with the new configuration.

Examples:
  ctxloom bundle mcp edit my-bundle tree-sitter
  ctxloom bundle mcp edit tools sequential-thinking`,
	Args: cobra.ExactArgs(2),
	RunE: runBundleMCPEdit,
}

func runBundleMCPEdit(cmd *cobra.Command, args []string) error {
	bundleName, mcpName := args[0], args[1]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	mcp, exists := bundle.MCP[mcpName]
	if !exists {
		return fmt.Errorf("MCP server not found: %s", mcpName)
	}

	// Serialize MCP config to YAML for editing
	mcpYAML, err := yaml.Marshal(&mcp)
	if err != nil {
		return fmt.Errorf("failed to serialize MCP config: %w", err)
	}

	// Edit content using editor
	newContent, err := editInEditor(cfg, string(mcpYAML), mcpName+".yaml")
	if err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}

	if newContent == string(mcpYAML) {
		fmt.Println("No changes made.")
		return nil
	}

	// Parse new YAML
	var newMCP bundles.BundleMCP
	if err := yaml.Unmarshal([]byte(newContent), &newMCP); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}

	bundle.MCP[mcpName] = newMCP

	if err := bundle.Save(); err != nil {
		return fmt.Errorf("failed to save bundle: %w", err)
	}

	fmt.Printf("Updated MCP server %q in bundle %q\n", mcpName, bundleName)
	return nil
}

// editInEditor opens content in the configured editor and returns the edited content.
func editInEditor(cfg *config.Config, content, filename string) (string, error) {
	// Create temp file
	tmpDir := os.TempDir()
	tmpFile := filepath.Join(tmpDir, filename)
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmpFile) }()

	// Get editor command
	editor := cfg.Editor.Command
	if editor == "" {
		editor = os.Getenv("EDITOR")
		if editor == "" {
			editor = "nano"
		}
	}

	// Build command
	args := append(cfg.Editor.Args, tmpFile)

	// Run editor
	editorCmd := exec.Command(editor, args...)
	editorCmd.Stdin = os.Stdin
	editorCmd.Stdout = os.Stdout
	editorCmd.Stderr = os.Stderr

	if err := editorCmd.Run(); err != nil {
		return "", fmt.Errorf("editor exited with error: %w", err)
	}

	// Read back content
	newContent, err := os.ReadFile(tmpFile)
	if err != nil {
		return "", fmt.Errorf("failed to read edited file: %w", err)
	}

	return string(newContent), nil
}

func init() {
	rootCmd.AddCommand(bundleCmd)
	bundleCmd.AddCommand(bundleListCmd)
	bundleCmd.AddCommand(bundleShowCmd)
	bundleCmd.AddCommand(bundleViewCmd)
	bundleCmd.AddCommand(bundleCreateCmd)
	bundleCmd.AddCommand(bundleEditCmd)
	bundleCmd.AddCommand(bundleDeleteCmd)
	bundleCmd.AddCommand(bundlePushCmd)
	bundleCmd.AddCommand(bundleExportCmd)
	bundleCmd.AddCommand(bundleImportCmd)
	bundleCmd.AddCommand(bundleDistillCmd)

	// Fragment subcommands
	bundleCmd.AddCommand(bundleFragmentCmd)
	bundleFragmentCmd.AddCommand(bundleFragmentListCmd)
	bundleFragmentCmd.AddCommand(bundleFragmentEditCmd)

	// Prompt subcommands
	bundleCmd.AddCommand(bundlePromptCmd)
	bundlePromptCmd.AddCommand(bundlePromptListCmd)
	bundlePromptCmd.AddCommand(bundlePromptEditCmd)

	// MCP subcommands
	bundleCmd.AddCommand(bundleMCPCmd)
	bundleMCPCmd.AddCommand(bundleMCPEditCmd)

	bundleCreateCmd.Flags().StringVarP(&bundleCreateDesc, "description", "d", "", "Bundle description")
	bundleDeleteCmd.Flags().BoolVarP(&bundleDeleteForce, "force", "f", false, "Skip confirmation prompt")
	bundlePushCmd.Flags().BoolVar(&bundlePushPR, "pr", false, "Create a pull request instead of pushing directly")
	bundlePushCmd.Flags().StringVar(&bundlePushBranch, "branch", "", "Target branch (default: repository default)")
	bundlePushCmd.Flags().StringVarP(&bundlePushMessage, "message", "m", "", "Commit message")
	bundleImportCmd.Flags().BoolVarP(&bundleImportForce, "force", "f", false, "Overwrite existing bundle")
	bundleExportCmd.Flags().StringVarP(&bundleExportOutput, "output", "o", "", "Output file path")
	bundleViewCmd.Flags().BoolVarP(&bundleViewDistilled, "distilled", "d", false, "Show distilled version if available")

	// bundleEditCmd flags
	bundleEditCmd.Flags().StringVarP(&bundleEditDesc, "description", "d", "", "New description")
	bundleEditCmd.Flags().StringVar(&bundleEditVersion, "version", "", "New version")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditAddTags, "add-tag", nil, "Tag(s) to add")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditRemoveTags, "remove-tag", nil, "Tag(s) to remove")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditAddFragment, "add-fragment", nil, "Fragment(s) to add")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditRemoveFragment, "remove-fragment", nil, "Fragment(s) to remove")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditAddPrompt, "add-prompt", nil, "Prompt(s) to add")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditRemovePrompt, "remove-prompt", nil, "Prompt(s) to remove")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditAddMCP, "add-mcp", nil, "MCP server(s) to add")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditRemoveMCP, "remove-mcp", nil, "MCP server(s) to remove")

	bundleDistillCmd.Flags().BoolVarP(&bundleDistillForce, "force", "f", false, "Re-distill even if unchanged")
	bundleDistillCmd.Flags().BoolVarP(&bundleDistillDryRun, "dry-run", "n", false, "Preview what would be distilled")
	bundleDistillCmd.Flags().StringVarP(&bundleDistillPlugin, "plugin", "l", "", "LLM to use (default from config)")
}
