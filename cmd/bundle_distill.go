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
	"github.com/ctxloom/ctxloom/resources"
	"github.com/ctxloom/shared/clidiag"
	"github.com/ctxloom/shared/textutil"
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
	// role, so a project can pair (say) an antigravity label for distill with a
	// claude-opus label for coding. The --llm flag names a config label;
	// otherwise the fast role's label is used.
	label := bundleDistillLLM
	if label == "" {
		label = cfg.FastLabel()
	} else if validated, verr := validateExplicitLLM(cfg, label); verr != nil {
		return verr
	} else {
		label = validated
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
				clidiag.Warn("ctxloom", "no files match %q", pattern)
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
var defaultDistillPrompt = resources.MustGetPromptText("distill-default")

// loadDistillPrompt loads the distillation prompt from bundles.
func loadDistillPrompt() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return defaultDistillPrompt, nil
	}

	// Try to load "distill" prompt from bundles
	prompt, err := operations.GetSkill(context.Background(), cfg, operations.GetSkillRequest{Name: "distill"})
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

// firstLineTruncated returns the first line of s, trimmed and capped at 60
// bytes (57 + ellipsis, never splitting a multibyte rune) for a compact
// one-line summary.
func firstLineTruncated(s string) string {
	line := strings.Split(strings.TrimSpace(s), "\n")[0]
	if len(line) > 60 {
		return textutil.TruncateBytes(line, 57) + "..."
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
	if !hasSiblingsOfType(len(bundle.Skills), excludeName, "skills/") {
		return
	}
	ctx.WriteString("Sibling skills:\n")
	for name, prompt := range bundle.Skills {
		if "skills/"+name == excludeName {
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
func distillWithModel(llmName, llmLabel, model string, env map[string]string, name, content, distillPrompt, siblingCtx string) (string, string, error) {
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
	return distillWithLLM(llmName, llmLabel, model, env, name, content, distillPrompt, siblingCtx)
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

// buildDistillMessage assembles the user message sent to the compressor: the
// distill prompt leads (framing the model as a compressor even in headless
// minimal mode, where the fragment/context-file channel is not loaded — without
// it weaker models answer the instruction-shaped content instead of compressing
// it), optional sibling context follows, and the item to compress is wrapped in
// <content_to_compress>. The item name rides as a tag ATTRIBUTE, not an injected
// `# name` markdown heading: injecting a heading made the model echo it on top
// of the content's own H1, doubling the title in the distilled output.
func buildDistillMessage(distillPrompt, siblingCtx, name, content string) string {
	var builder strings.Builder

	if distillPrompt != "" {
		builder.WriteString(distillPrompt)
		builder.WriteString("\n\n")
	}

	if siblingCtx != "" {
		builder.WriteString("<bundle_context>\n")
		builder.WriteString(siblingCtx)
		builder.WriteString("\n</bundle_context>\n\n")
		builder.WriteString("CONTEXT-AWARE COMPRESSION:\n")
		builder.WriteString("- The bundle_context shows sibling items that will be loaded together\n")
		builder.WriteString("- DO NOT repeat concepts already covered by siblings - reference them instead\n")
		builder.WriteString("- Compress knowing this content will be loaded alongside those siblings\n\n")
	}

	builder.WriteString("<content_to_compress name=\"")
	builder.WriteString(name)
	builder.WriteString("\">\n")
	builder.WriteString(content)
	builder.WriteString("\n</content_to_compress>")

	return builder.String()
}

// distillWithLLM sends content through the LLM and returns distilled content and model ID.
func distillWithLLM(llmName, llmLabel, model string, env map[string]string, name, content, distillPrompt, siblingCtx string) (string, string, error) {
	message := buildDistillMessage(distillPrompt, siblingCtx, name, content)

	// Create plugin client. The label rides along so serve configures the exact
	// entry the distill resolved (--llm or the fast role), not a type-scan pick.
	client, err := pb.NewSelfInvokingClientForLabel(llmName, llmLabel, 0)
	if err != nil {
		return "", "", fmt.Errorf("failed to start plugin: %w", err)
	}
	defer client.Kill()

	// Build request
	req := &pb.RunStart{
		Prompt: &pb.Fragment{
			Content: message,
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
	result, err := client.RunWithModelInfo(context.Background(), req, nil, &stdout, &stderr, nil)
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

	// Reject a chat reply: the model followed the (instruction-shaped) content
	// instead of compressing it. Erroring leaves the item raw rather than
	// saving garbage (the operations layer warns and continues).
	if distilled == "" || looksConversational(distilled) {
		return "", "", fmt.Errorf("distill produced a non-compression response (model %s answered the content instead of compressing it)", modelID)
	}

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
	content = strings.TrimSpace(runtimeNoiseRe.ReplaceAllString(content, ""))

	content, foundPreamble := stripConversationalPreamble(content)
	if foundPreamble {
		content = stripPreambleSeparator(content)
	}
	return stripCodeFence(content)
}

// runtimeNoiseRe matches stray CLI status banners that can leak into captured
// stdout (e.g. an MCP health notice) so they never land in distilled content.
var runtimeNoiseRe = regexp.MustCompile(`MCP issues detected\. Run /mcp list for status\.\s*`)

// conversationalLeads are sentence starts that mean the model answered the
// content as a prompt instead of compressing it.
var conversationalLeads = []string{
	"i see ", "i understand", "i'll ", "i will ", "i can help", "i'd be happy",
	"sure,", "sure!", "okay,", "of course", "let me know",
}

// conversationalPhrases are anywhere-in-output tells of a chat reply.
var conversationalPhrases = []string{
	"what would you like", "would you like me to", "how can i help",
	"let me know what", "what's the task", "what can i do for you",
}

// looksConversational reports whether output reads as a chat reply to the
// content (the model followed instruction-shaped input) rather than a
// compression of it. Such output is rejected so the item is left raw instead of
// being saved as garbage.
func looksConversational(content string) bool {
	lower := strings.ToLower(strings.TrimSpace(content))
	for _, lead := range conversationalLeads {
		if strings.HasPrefix(lower, lead) {
			return true
		}
	}
	for _, phrase := range conversationalPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
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
