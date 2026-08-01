package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// Fragment and prompt management under `bundle` was a partial duplicate of the
// top-level `fragment`/`prompt` command trees (which carry the full CRUD and
// already route through internal/operations). Those duplicate subtrees were
// removed.
//
// Bundle-scoped MCP-server editing used to hang off `bundle mcp edit`. The verb
// spine moved it to the MCP-server noun — `mcp server edit <bundle>#mcp/<name>`
// (mcp.go) — and deleted the `bundle mcp` group node; the body below is that
// leaf's implementation, still routed through the operations core.

// runBundleMCPEdit edits one bundle-scoped MCP server. args is
// [bundle-name, mcp-name]: the caller (runMCPServerEdit) has already split the
// `<bundle>#mcp/<name>` ref into its two halves.
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
	w := iox.NewErrWriter(cmd.OutOrStdout())
	if newContent == string(mcpYAML) {
		w.Println("No changes made.")
		return w.Err()
	}
	// U034-F01: an emptied editor buffer (the user deleted everything and
	// saved, or the editor exited leaving a blank temp file) still differs
	// from mcpYAML, so it fell straight past the "No changes made" guard
	// above. yaml.Unmarshal("", &edited) succeeds with a ZERO-VALUE struct —
	// no error, no empty-input signal of its own — so this must be checked
	// explicitly, before it ever reaches SetBundleMCP, which validates
	// nothing about Command being non-empty.
	if strings.TrimSpace(newContent) == "" {
		return fmt.Errorf("aborted: the edited MCP config is empty; bundle %q was not changed", bundleName)
	}

	var edited bundles.BundleMCP
	if err := yaml.Unmarshal([]byte(newContent), &edited); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}
	if edited.Command == "" {
		return fmt.Errorf("aborted: the edited MCP config has no `command:`; bundle %q was not changed", bundleName)
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

	w.Printf("Updated MCP server %q in bundle %q\n", mcpName, bundleName)
	return w.Err()
}

// editInEditor opens content in the configured editor and returns the edited
// content. The temp-file round-trip and editor invocation are frontend concerns;
// callers persist the result through internal/operations.
func editInEditor(cfg *config.Config, content, filename string) (string, error) {
	// A private per-call temp dir: a fixed os.TempDir()/<name> path collides
	// across concurrent edits of same-named items and is predictable and
	// world-readable (0644) on shared machines for possibly-private content.
	// The subdir keeps the user-visible filename (editors show it) while
	// 0700 + MkdirTemp give isolation and unpredictability.
	tmpDir, err := os.MkdirTemp("", "ctxloom-edit-*")
	if err != nil {
		// tmpDir is normally "" here (MkdirTemp itself failed) — defensive
		// against a mutant flipping this check and orphaning a dir MkdirTemp
		// actually created, since the defer below only registers after this
		// point (see the identical guard in internal/lm/isolation).
		_ = os.RemoveAll(tmpDir)
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	tmpFile := filepath.Join(tmpDir, filename)
	if err := os.WriteFile(tmpFile, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}

	// Resolve the editor through the one shared policy (config → VISUAL →
	// EDITOR → nano), which also splits multi-word values like "code --wait".
	editor, editorArgs := cfg.GetEditorCommand()

	// Run editor
	editorCmd := exec.Command(editor, append(editorArgs, tmpFile)...)
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
