package cli

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/compression"
	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/textutil"
	"github.com/ctxloom/ctxloom/resources"
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
  ctxloom bundle distill .ctxloom/content/bundles/*.yaml           # All bundles in directory
  ctxloom bundle distill .ctxloom/content/bundles/**/*.yaml        # Recursive
  ctxloom bundle distill bundle1.yaml bundle2.yaml           # Multiple files
  ctxloom bundle distill ./my-bundle.yaml --force            # Re-distill all items
  ctxloom bundle distill ./my-bundle.yaml --dry-run          # Preview what would be distilled`,
	Args: cobra.MinimumNArgs(1),
	RunE: runBundleDistill,
}

// bundleDistillFileOutcome is one input file's distill result: the same
// per-item detail operations.DistillBundleFile returns, kept alongside the
// file path so json/yaml/toml/markdown output can attribute items to files
// (the text renderer's "Processing: <path>" header does the same, visually).
type bundleDistillFileOutcome struct {
	Path  string                         `json:"path" col:"File"`
	Saved bool                           `json:"saved"`
	Items []operations.DistillBundleItem `json:"items"`
}

// bundleDistillResult is emit()'s result for `ctxloom bundle distill`: the
// full per-file, per-item detail plus the run-level summary counts and
// invalidated-approval refs, so every format carries what --format text has
// always printed. Errors is populated for input files that failed to load —
// bundle distill keeps processing the rest rather than aborting the run.
type bundleDistillResult struct {
	DryRun       bool                       `json:"dry_run"`
	Files        []bundleDistillFileOutcome `json:"files"`
	Errors       []string                   `json:"errors,omitempty"`
	TotalItems   int                        `json:"total_items"`
	TotalFiles   int                        `json:"total_files"`
	TotalSkipped int                        `json:"total_skipped"`
	Invalidated  []string                   `json:"invalidated,omitempty"`
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
	// role, so a project can pair (say) a cheap codex label for distill with a
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

	result := bundleDistillResult{DryRun: bundleDistillDryRun}
	for _, filePath := range files {
		res, err := operations.DistillBundleFile(cmd.Context(), operations.DistillBundleFileRequest{
			Path:      filePath,
			Force:     bundleDistillForce,
			DryRun:    bundleDistillDryRun,
			Distiller: distiller,
			Cfg:       cfg,
		})
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", filePath, err))
			continue
		}
		result.Files = append(result.Files, bundleDistillFileOutcome{Path: filePath, Saved: res.Saved, Items: res.Items})
		items, skipped := countDistillItems(res.Items)
		result.TotalItems += items
		result.TotalSkipped += skipped
		if res.Saved {
			result.TotalFiles++
		}
		for _, inv := range res.Invalidated {
			result.Invalidated = append(result.Invalidated, filePath+"#"+inv)
		}
	}

	if err := emit(cmd, result, func() error {
		// Diagnostics ride the command's ERROR writer (not the process's
		// os.Stderr), and every line goes through an ErrWriter so a broken
		// stdout is reported instead of producing a silent success.
		errw := iox.NewErrWriter(cmd.ErrOrStderr())
		for _, e := range result.Errors {
			errw.Println(e)
		}
		w := iox.NewErrWriter(cmd.OutOrStdout())
		for _, f := range result.Files {
			w.Printf("Processing: %s\n", f.Path)
			printDistillItems(w, f.Items)
		}
		printDistillSummary(w, result.TotalItems, result.TotalFiles, result.TotalSkipped, result.DryRun)
		printDistillInvalidatedApprovals(w, result.Invalidated)
		if err := w.Err(); err != nil {
			return err
		}
		return errw.Err()
	}); err != nil {
		return err
	}
	// Every per-file failure above only ever appended to
	// result.Errors and `continue`d — nothing converted a non-empty Errors
	// into a non-nil error for THIS command, so `ctxloom bundle distill` over
	// a directory where every file failed still printed to stderr and exited
	// 0. Checked AFTER emit (not instead of it) so the structured payload —
	// which already carries result.Errors — is still printed either way;
	// this only changes the exit code / final error, never the output.
	if len(result.Errors) > 0 {
		return fmt.Errorf("bundle distill: %d of %d file(s) failed", len(result.Errors), len(files))
	}
	return nil
}

// printDistillInvalidatedApprovals is the re-distill LOUD PATH (spec §10.4):
// fired at the moment invalidation is CAUSED, by the command that caused it —
// never silently discovered later at the next `ctxloom review`. It explains
// WHY in one sentence (so a user does not go looking for a way to silence it)
// and names the exact recovery command.
func printDistillInvalidatedApprovals(w *iox.ErrWriter, refs []string) {
	if len(refs) == 0 {
		return
	}
	w.Printf("\n⚠ %d approval(s) invalidated.\n\n", len(refs))
	w.Println("  Re-distilling rewrote the DISTILLED form of these items. Your approvals")
	w.Println("  covered the previous bytes — the agent would now see text nobody has")
	w.Println("  reviewed, so they are back to pending and are withheld until you review")
	w.Println("  them.")
	w.Println()
	for _, ref := range refs {
		w.Printf("    %s\n", ref)
	}
	w.Println()
	w.Println("  Review them:            ctxloom review")
	w.Println("  (Raw forms are unaffected — their approvals still stand.)")
}

// countDistillItems tallies distilled/planned items vs. skipped ones for the
// run summary, with no output side effect — the pure-data half of what used
// to be renderDistillItems before results were buffered for emit().
func countDistillItems(items []operations.DistillBundleItem) (processed, skipped int) {
	for _, it := range items {
		switch it.Status {
		case operations.DistillStatusSkipped:
			skipped++
		case operations.DistillStatusPlanned, operations.DistillStatusDistilled:
			processed++
		}
	}
	return processed, skipped
}

// printDistillItems prints one line per item outcome — the text-format half
// of what used to be renderDistillItems, now separated from counting so
// counting can happen unconditionally (for the structured result) while
// printing happens only inside emit()'s text closure.
func printDistillItems(w *iox.ErrWriter, items []operations.DistillBundleItem) {
	for _, it := range items {
		switch it.Status {
		case operations.DistillStatusSkipped:
			w.Printf("  Skipping %s %s (%s)\n", it.Kind, it.Name, it.Reason)
		case operations.DistillStatusPlanned:
			w.Printf("  Would distill %s: %s\n", it.Kind, it.Name)
		case operations.DistillStatusDistilled:
			w.Printf("  Distilled %s: %s (%s)\n", it.Kind, it.Name, it.ModelID)
		}
	}
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
func printDistillSummary(w *iox.ErrWriter, totalItems, totalFiles, totalSkipped int, dryRun bool) {
	if dryRun {
		w.Printf("\nDry run: would distill %d items\n", totalItems)
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
		w.Printf("\n%s\n", strings.Join(parts, ", "))
	} else {
		w.Println("\nNo items to distill.")
	}
}

// defaultDistillPrompt is used when no distill prompt is found in bundles.
var defaultDistillPrompt = resources.MustGetPromptText("distill-default")

// loadDistillPrompt loads the distillation prompt from bundles.
//
// It cannot fail: every unavailable source (no config, no `distill` command in
// any trusted bundle, an empty one) falls back to the embedded default prompt,
// so there is always a usable prompt and never an error to report. It returns
// no error rather than a permanently-nil one, so no caller has to decide what
// to do about a failure that cannot happen (the discarded error was
// one of three "silent return nil" paths in newLLMDistillerForLabel).
func loadDistillPrompt() string {
	cfg, err := config.Load()
	if err != nil {
		return defaultDistillPrompt
	}

	// Try to load "distill" prompt from bundles
	prompt, err := operations.GetCommand(context.Background(), cfg, operations.GetCommandRequest{Name: "distill"})
	if err == nil && prompt.Content != "" {
		return strings.TrimSpace(prompt.Content)
	}

	// Use default prompt
	return defaultDistillPrompt
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

// hasSiblingsOfType reports whether a bundle has sibling items of a kind worth
// listing: more than one of that kind, or exactly one that isn't the excluded
// (currently-distilling) item. It takes the item KIND, not a spelled-out
// prefix, so the "fragments/"/"commands/" ref grammar lives only in
// itemRefPrefix (item_kind.go).
func hasSiblingsOfType(count int, excludeName string, kind ItemType) bool {
	return count > 1 || (count == 1 && !strings.HasPrefix(excludeName, itemRefPrefix(kind)))
}

// firstLineTruncated returns the first line of s, trimmed and capped at 60
// bytes TOTAL (ellipsis included, never splitting a multibyte rune) for a
// compact one-line summary. Ellipsize reserves the ellipsis from the budget,
// so 60 is the real cap and the call site carries no pre-compensation.
func firstLineTruncated(s string) string {
	line := strings.Split(strings.TrimSpace(s), "\n")[0]
	return textutil.Ellipsize(line, 60)
}

// appendSiblingFragments lists the bundle's fragments (excluding excludeName)
// with a one-line content preview, in NAME order. The listing is part of the
// message handed to the distiller, so map order here would make the model's
// input — and therefore the distilled bytes — differ run to run.
func appendSiblingFragments(ctx *strings.Builder, bundle *bundles.Bundle, excludeName string) {
	if !hasSiblingsOfType(len(bundle.Fragments), excludeName, ItemTypeFragment) {
		return
	}
	ctx.WriteString("Sibling fragments:\n")
	for _, name := range slices.Sorted(maps.Keys(bundle.Fragments)) {
		if itemRefPrefix(ItemTypeFragment)+name == excludeName {
			continue
		}
		fmt.Fprintf(ctx, "- %s: %s\n", name, firstLineTruncated(bundle.Fragments[name].Content))
	}
	ctx.WriteString("\n")
}

// appendSiblingPrompts lists the bundle's prompts (excluding excludeName) in
// NAME order (see appendSiblingFragments on why order is load-bearing),
// preferring each prompt's Description over a content preview.
func appendSiblingPrompts(ctx *strings.Builder, bundle *bundles.Bundle, excludeName string) {
	if !hasSiblingsOfType(len(bundle.Commands), excludeName, ItemTypeCommand) {
		return
	}
	ctx.WriteString("Sibling commands:\n")
	for _, name := range slices.Sorted(maps.Keys(bundle.Commands)) {
		if itemRefPrefix(ItemTypeCommand)+name == excludeName {
			continue
		}
		prompt := bundle.Commands[name]
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
func distillWithModel(ctx context.Context, llmName, llmLabel, model string, env map[string]string, name, content, distillPrompt, siblingCtx string) (string, string, error) {
	// Detect content type and try AST/JSON compression first
	contentType := compression.DetectContentType(name, content)

	// For code and JSON, try fast local compression
	if isStructuredContent(contentType) {
		result, err := compressionRouter.CompressWithType(ctx, contentType, content)
		if err == nil && result.Ratio < 0.7 {
			// Good compression achieved with AST/JSON - use it
			return result.Content, result.ModelID, nil
		}
		// If compression didn't achieve good ratio or failed, fall back to LLM
	}

	// For text content or when AST compression isn't effective, use LLM
	return distillWithLLM(ctx, llmName, llmLabel, model, env, name, content, distillPrompt, siblingCtx)
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
func distillWithLLM(ctx context.Context, llmName, llmLabel, model string, env map[string]string, name, content, distillPrompt, siblingCtx string) (string, string, error) {
	// buildDistillMessage already leads with distillPrompt (see its own doc):
	// SkipSetup below means Setup never runs, and Setup is the ONLY path that
	// delivers RunStart.Fragments to the backend (internal/lm/grpc/server.go's
	// Run handler only converts+delivers req.Fragments inside the
	// !opts.GetSkipSetup() branch) — a Fragments entry here would be silently
	// dropped, never reaching the model. SILENT NO-OP: this
	// caller used to also set Fragments:[{Content: distillPrompt}] alongside
	// the already-smuggled message, which did nothing but read as if the
	// instructions had a second, redundant delivery path. Removed; the prompt
	// body is the ONLY delivery path for a SkipSetup run, by design.
	message := buildDistillMessage(distillPrompt, siblingCtx, name, content)

	// Create plugin client. The label rides along so serve configures the exact
	// entry the distill resolved (--llm or the fast role), not a type-scan pick.
	//
	// DEGRADE, DON'T DUMP: a failure here means the "ctxloom llm serve
	// <backend>" subprocess never completed the go-plugin handshake (backend
	// unregistered/misconfigured, binary missing, or it crashed on startup) —
	// i.e. no reachable engine for this label. go-plugin's err in that case is
	// a multi-line internal diagnostic ("Unrecognized remote plugin
	// message... Additional notes about plugin: Path/Mode/Owner/Group/ELF
	// architecture...") never meant for an end user; one dumped line reads
	// exactly like an unrelated package's `go test` failure. The caller
	// (distillFragments/distillPrompts) already warns-and-continues on any
	// Distill error — the SAME skip-but-keep-raw-content path SetItemContent
	// takes for a nil Distiller — so replacing go-plugin's err with one clean
	// sentence here is enough to keep that existing degrade path from ever
	// leaking raw handshake output; it does not change the Distiller
	// contract or add a second skip mechanism.
	client, err := pb.NewSelfInvokingClientForLabel(llmName, llmLabel, 0)
	if err != nil {
		return "", "", fmt.Errorf("no reachable engine for distillation (backend %q, label %q) — content saved raw, undistilled", llmName, llmLabel)
	}
	defer client.Kill()

	// Build request
	req := &pb.RunStart{
		Prompt: &pb.Fragment{
			Content: message,
		},
		Options: &pb.RunOptions{
			PermissionMode: agent.PermissionBypass.String(),
			Mode:           pb.ExecutionMode_ONESHOT,
			Model:          model, // explicit override; empty → backend's lightweight model
			Env:            env,
			SkipSetup:      true, // Headless distill: no hooks/commands/context writes
		},
	}

	// Execute and capture model info
	var stdout, stderr bytes.Buffer
	result, err := client.RunWithModelInfo(ctx, req, nil, &stdout, &stderr, nil)
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

// registerBundleDistillFlags defines `bundle distill`'s flags.
func registerBundleDistillFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVarP(&bundleDistillForce, "force", "f", false, "Re-distill even if unchanged")
	cmd.Flags().BoolVarP(&bundleDistillDryRun, "dry-run", "n", false, "Preview what would be distilled")
	cmd.Flags().StringVarP(&bundleDistillLLM, "llm", "l", "", "config label to use (e.g. claude-code, claude-fast, codex); overrides the configured default")
}
