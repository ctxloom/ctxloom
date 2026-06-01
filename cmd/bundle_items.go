package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// Fragment and prompt management under `bundle` was a partial duplicate of the
// top-level `fragment`/`prompt` command trees (which carry the full CRUD and
// already route through internal/operations). Those duplicate subtrees were
// removed; MCP-server editing has no top-level equivalent, so it stays here —
// routed through the operations core like everything else.

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

	// Read the current config through operations; YAML is just this frontend's
	// editor representation (the core deals in structured BundleMCPInput).
	cur, err := operations.GetBundleMCP(cmd.Context(), cfg, operations.GetBundleMCPRequest{Bundle: bundleName, Name: mcpName})
	if err != nil {
		if errors.Is(err, operations.ErrItemNotFound) {
			return fmt.Errorf("MCP server not found: %s", mcpName)
		}
		return err
	}

	mcpYAML, err := yaml.Marshal(&cur.MCP)
	if err != nil {
		return fmt.Errorf("failed to serialize MCP config: %w", err)
	}

	newContent, err := editInEditor(cfg, string(mcpYAML), mcpName+".yaml")
	if err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}
	if newContent == string(mcpYAML) {
		fmt.Println("No changes made.")
		return nil
	}

	var edited bundles.BundleMCP
	if err := yaml.Unmarshal([]byte(newContent), &edited); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}

	if _, err := operations.SetBundleMCP(cmd.Context(), cfg, operations.SetBundleMCPRequest{
		Bundle: bundleName,
		Name:   mcpName,
		MCP: operations.BundleMCPInput{
			Command:      edited.Command,
			Args:         edited.Args,
			Env:          edited.Env,
			Notes:        edited.Notes,
			Installation: edited.Installation,
		},
	}); err != nil {
		return err
	}

	fmt.Printf("Updated MCP server %q in bundle %q\n", mcpName, bundleName)
	return nil
}

// editInEditor opens content in the configured editor and returns the edited
// content. The temp-file round-trip and editor invocation are frontend concerns;
// callers persist the result through internal/operations.
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
