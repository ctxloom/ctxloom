package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/compression"
	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
)

var bundleDistillForce bool
var bundleDistillDryRun bool
var bundleDistillLLM string

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
	files, err := expandDistillFiles(args)
	if err != nil {
		return err
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Distillation runs on its own labeled config, independent of the primary
	// role, so a project can pair (say) a gemini-fast label for distill with a
	// claude-opus label for coding. The --llm flag names a config label;
	// otherwise the fast role's label is used.
	label := bundleDistillLLM
	if label == "" {
		label = cfg.FastLabel()
	}
	distiller := newLLMDistillerForLabel(cfg, label)

	var totalFiles, totalItems, totalSkipped int
	for _, filePath := range files {
		res, err := operations.DistillBundleFile(cmd.Context(), operations.DistillBundleFileRequest{
			Path:      filePath,
			Force:     bundleDistillForce,
			DryRun:    bundleDistillDryRun,
			Distiller: distiller,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			continue
		}
		fmt.Printf("Processing: %s\n", filePath)
		items, skipped := renderDistillItems(res.Items)
		totalItems += items
		totalSkipped += skipped
		if res.Saved {
			totalFiles++
		}
	}

	printDistillSummary(totalItems, totalFiles, totalSkipped)
	return nil
}

// renderDistillItems prints one line per item outcome and returns the count of
// distilled/planned items and skipped items for the run summary.
func renderDistillItems(items []operations.DistillBundleItem) (processed, skipped int) {
	for _, it := range items {
		switch it.Status {
		case operations.DistillStatusSkipped:
			fmt.Printf("  Skipping %s %s (%s)\n", it.Kind, it.Name, it.Reason)
			skipped++
		case operations.DistillStatusPlanned:
			fmt.Printf("  Would distill %s: %s\n", it.Kind, it.Name)
			processed++
		case operations.DistillStatusDistilled:
			fmt.Printf("  Distilled %s: %s (%s)\n", it.Kind, it.Name, it.ModelID)
			processed++
		}
	}
	return processed, skipped
}

// expandDistillFiles resolves the CLI patterns to a list of bundle files. Glob
// matches expand; a non-matching pattern is tried as a literal path and warned
// about if absent. Returns an error only when no files resolve at all.
func expandDistillFiles(patterns []string) ([]string, error) {
	var files []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %q: %w", pattern, err)
		}
		if len(matches) == 0 {
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
		return nil, fmt.Errorf("no files found matching patterns")
	}
	return files, nil
}

// printDistillSummary prints the run summary, branching on dry-run.
func printDistillSummary(totalItems, totalFiles, totalSkipped int) {
	if bundleDistillDryRun {
		fmt.Printf("\nDry run: would distill %d items\n", totalItems)
		return
	}
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
	prompt, err := operations.GetPrompt(context.Background(), cfg, operations.GetPromptRequest{Name: "distill"})
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

	appendSiblingFragments(&ctx, bundle, excludeName)
	appendSiblingPrompts(&ctx, bundle, excludeName)

	return ctx.String()
}

// hasSiblingsOfType reports whether a bundle has sibling items of a type worth
// listing: more than one of that type, or exactly one that isn't the excluded
// (currently-distilling) item.
func hasSiblingsOfType(count int, excludeName, prefix string) bool {
	return count > 1 || (count == 1 && !strings.HasPrefix(excludeName, prefix))
}

// firstLineTruncated returns the first line of s, trimmed and capped at 60 runes
// (57 + "…"-style ellipsis) for a compact one-line summary.
func firstLineTruncated(s string) string {
	line := strings.Split(strings.TrimSpace(s), "\n")[0]
	if len(line) > 60 {
		return line[:57] + "..."
	}
	return line
}

// appendSiblingFragments lists the bundle's fragments (excluding excludeName)
// with a one-line content preview.
func appendSiblingFragments(ctx *strings.Builder, bundle *bundles.Bundle, excludeName string) {
	if !hasSiblingsOfType(len(bundle.Fragments), excludeName, "fragments/") {
		return
	}
	ctx.WriteString("Sibling fragments:\n")
	for name, frag := range bundle.Fragments {
		if "fragments/"+name == excludeName {
			continue
		}
		fmt.Fprintf(ctx, "- %s: %s\n", name, firstLineTruncated(frag.Content))
	}
	ctx.WriteString("\n")
}

// appendSiblingPrompts lists the bundle's prompts (excluding excludeName),
// preferring each prompt's Description over a content preview.
func appendSiblingPrompts(ctx *strings.Builder, bundle *bundles.Bundle, excludeName string) {
	if !hasSiblingsOfType(len(bundle.Prompts), excludeName, "prompts/") {
		return
	}
	ctx.WriteString("Sibling prompts:\n")
	for name, prompt := range bundle.Prompts {
		if "prompts/"+name == excludeName {
			continue
		}
		desc := prompt.Description
		if desc == "" {
			desc = firstLineTruncated(prompt.Content)
		}
		fmt.Fprintf(ctx, "- %s: %s\n", name, desc)
	}
}

// compressionRouter is a shared router for AST/JSON compression.
var compressionRouter = compression.NewRouter()

// distillWithModel sends content through compression and returns distilled content and model ID.
// It first tries AST-based compression for code and JSON structure compression for JSON content.
// For text/markdown content (or if AST compression doesn't achieve good compression), it falls back to LLM.
func distillWithModel(llmName, model string, env map[string]string, name, content, distillPrompt, siblingCtx string) (string, string, error) {
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
	return distillWithLLM(llmName, model, env, name, content, distillPrompt, siblingCtx)
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
func distillWithLLM(llmName, model string, env map[string]string, name, content, distillPrompt, siblingCtx string) (string, string, error) {
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
	client, err := pb.NewSelfInvokingClient(llmName, 0)
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
			Model:       model, // explicit override; empty → backend's lightweight model
			Env:         env,
			SkipSetup:   true, // Headless distill: no hooks/skills/context writes
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
	modelID := llmName
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

// conversationalStarts are patterns Configs add despite instructions
var conversationalStarts = []string{
	"here's ", "here is ", "below is ", "below you'll find ",
	"the compressed version", "the following ",
	"i've compressed ", "i have compressed ", "my compressed version",
}

// cleanDistilledOutput removes LLM preamble artifacts.
func cleanDistilledOutput(content string) string {
	content = strings.TrimSpace(content)

	content, foundPreamble := stripConversationalPreamble(content)
	if foundPreamble {
		content = stripPreambleSeparator(content)
	}
	return stripCodeFence(content)
}

// stripConversationalPreamble drops a leading conversational line (e.g. "Sure,
// here's...") and reports whether one was found.
func stripConversationalPreamble(content string) (string, bool) {
	lower := strings.ToLower(content)
	for _, prefix := range conversationalStarts {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		if idx := strings.Index(content, "\n"); idx != -1 {
			content = strings.TrimSpace(content[idx+1:])
		}
		return content, true
	}
	return content, false
}

// stripPreambleSeparator removes a near-the-top "---"-style separator left after
// a conversational preamble (only when it appears within the first 100 chars).
func stripPreambleSeparator(content string) string {
	loc := preambleRe.FindStringIndex(content)
	if loc == nil || loc[0] >= 100 {
		return content
	}
	after := content[loc[1]:]
	if len(after) > 0 && after[0] == '\n' {
		after = after[1:]
	}
	return strings.TrimSpace(after)
}

// stripCodeFence unwraps a leading ```fence (and its matching trailing fence,
// when that fence is alone on the last line).
func stripCodeFence(content string) string {
	loc := codeFenceRe.FindStringIndex(content)
	if loc == nil || loc[0] != 0 {
		return content
	}
	content = content[loc[1]:]
	if idx := strings.LastIndex(content, "```"); idx != -1 {
		if strings.TrimSpace(content[idx+3:]) == "" {
			content = strings.TrimSpace(content[:idx])
		}
	}
	return content
}
