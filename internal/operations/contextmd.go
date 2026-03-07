package operations

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/benjaminabbitt/scm/internal/config"
)

// SourceContextFiles are the files SCM looks for as the source of truth.
// Order matters - first found wins.
var SourceContextFiles = []string{"llm.md", "scm.md"}

// PreferredSourceFile is the default source file name when creating new.
const PreferredSourceFile = "llm.md"

// BackendTargetFiles maps backend names to their expected context file names.
var BackendTargetFiles = map[string]string{
	"claude-code": "CLAUDE.md",
	"gemini":      "GEMINI.md",
	"cursor":      ".cursorrules",
	"windsurf":    ".windsurfrules",
	"cline":       ".clinerules",
	"aider":       "CONVENTIONS.md",
	"codex":       "AGENTS.md",
}

// TransformContextRequest contains parameters for transforming context files.
type TransformContextRequest struct {
	// Backends specifies which backends to generate files for.
	// Empty means all configured backends.
	Backends []string `json:"backends,omitempty"`

	// FS is an optional filesystem (for testing).
	FS afero.Fs `json:"-"`
}

// TransformContextResult contains the result of context file transformation.
type TransformContextResult struct {
	Status      string                  `json:"status"`
	SourceFile  string                  `json:"source_file,omitempty"`
	Generated   []GeneratedContextFile  `json:"generated,omitempty"`
	Errors      []string                `json:"errors,omitempty"`
	Warnings    []string                `json:"warnings,omitempty"`
	Message     string                  `json:"message,omitempty"`
}

// GeneratedContextFile represents a generated context file for a backend.
type GeneratedContextFile struct {
	Backend  string `json:"backend"`
	Target   string `json:"target"`
	Status   string `json:"status"` // "created", "updated", "skipped", "user_managed"
}

// TransformContext reads llm.md or scm.md, transforms through plugins, and writes to backend target files.
func TransformContext(ctx context.Context, cfg *config.Config, req TransformContextRequest) (*TransformContextResult, error) {
	fs := req.FS
	if fs == nil {
		fs = afero.NewOsFs()
	}

	result := &TransformContextResult{
		Status: "completed",
	}

	// Find the work directory (project root)
	workDir := cfg.SCMRoot
	if workDir == "" {
		workDir = "."
	}

	// Find source file
	sourceFile, sourceContent, err := findSourceContextFile(fs, workDir)
	if err != nil {
		// No source file found - this is not an error, just nothing to do
		result.Status = "no_source"
		result.Message = "No llm.md or scm.md found"
		return result, nil
	}
	result.SourceFile = sourceFile

	// Determine which backends to generate for
	backends := req.Backends
	if len(backends) == 0 {
		// Use configured plugins from config
		backends = cfg.LM.GetConfiguredPlugins()
	}

	// Generate for each backend
	for _, backend := range backends {
		targetFile, ok := BackendTargetFiles[backend]
		if !ok {
			result.Errors = append(result.Errors, fmt.Sprintf("unknown backend: %s", backend))
			continue
		}

		// Transform content through plugin (echo for now)
		transformed := transformForBackend(sourceContent, backend, sourceFile)

		// Write to target file
		targetPath := filepath.Join(workDir, targetFile)
		genResult, err := writeContextFile(fs, targetPath, transformed, sourceFile)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", backend, err))
			continue
		}

		result.Generated = append(result.Generated, GeneratedContextFile{
			Backend: backend,
			Target:  targetFile,
			Status:  genResult,
		})
	}

	if len(result.Errors) > 0 {
		result.Status = "completed_with_errors"
	}

	// Count actual changes vs skips and collect warnings for user-managed files
	var created, updated, skipped int
	for _, g := range result.Generated {
		switch g.Status {
		case "created":
			created++
		case "updated":
			updated++
		case "skipped":
			skipped++
		case "user_managed":
			skipped++
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("%s exists but is not SCM-managed; edit %s instead for cross-LLM sync",
					g.Target, sourceFile))
		}
	}

	if created+updated > 0 {
		result.Message = fmt.Sprintf("Generated %d context files from %s (%d created, %d updated, %d unchanged)",
			len(result.Generated), sourceFile, created, updated, skipped)
	} else {
		result.Message = fmt.Sprintf("Context files up to date (source: %s)", sourceFile)
	}
	return result, nil
}

// findSourceContextFile looks for llm.md or scm.md in the work directory.
func findSourceContextFile(fs afero.Fs, workDir string) (string, string, error) {
	for _, name := range SourceContextFiles {
		path := filepath.Join(workDir, name)
		data, err := afero.ReadFile(fs, path)
		if err == nil {
			return name, string(data), nil
		}
	}
	return "", "", fmt.Errorf("no source context file found (looked for: %s)", strings.Join(SourceContextFiles, ", "))
}

// transformForBackend transforms source content for a specific backend.
// Currently just echoes the content - plugins can override this.
func transformForBackend(content, backend, sourceFile string) string {
	// Build the header
	header := buildDerivedHeader(sourceFile, backend)

	return header + content
}

// buildDerivedHeader creates the header indicating this file is derived.
func buildDerivedHeader(sourceFile, backend string) string {
	return fmt.Sprintf(`<!-- SCM:MANAGED
  Source: %s | Backend: %s

  This file is auto-generated from %s by SCM.
  Edit %s instead - changes sync to all configured LLM backends on startup.

  To disable sync and manage this file manually, delete this header block.
  SCM will not modify files without the SCM:MANAGED marker.
-->

`, sourceFile, backend, sourceFile, sourceFile)
}

// writeContextFile writes the transformed content to the target file.
// Returns "created", "updated", "skipped", or "user_managed" based on what happened.
func writeContextFile(fs afero.Fs, targetPath, content, sourceFile string) (string, error) {
	// Check if file exists and has our header
	existing, err := afero.ReadFile(fs, targetPath)
	if err == nil {
		// File exists - check if it's SCM-managed
		if !strings.Contains(string(existing), "SCM:MANAGED") {
			// Not our file - check if it looks like user content
			// Skip if it has content and no SCM marker
			if len(strings.TrimSpace(string(existing))) > 0 {
				// User has their own file - don't overwrite
				return "user_managed", nil
			}
		}

		// It's our file or empty - check if content changed
		if string(existing) == content {
			return "skipped", nil
		}
	}

	// Write the file
	if err := afero.WriteFile(fs, targetPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write %s: %w", targetPath, err)
	}

	if err != nil {
		return "created", nil
	}
	return "updated", nil
}

// TransformContextOnStartup is a convenience function for startup.
// It transforms context files with graceful error handling.
func TransformContextOnStartup(ctx context.Context, cfg *config.Config) (*TransformContextResult, error) {
	return TransformContext(ctx, cfg, TransformContextRequest{})
}
