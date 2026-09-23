---
title: "Distillation"
---

Distillation compresses verbose context into token-efficient versions while preserving essential information. This helps you stay within context limits and reduce costs.

This is authoring-time compression: it runs on the fragments and commands in your bundles, not on conversation history. If you're looking for how ctxloom summarizes a session's transcript across `/clear`, that's a different feature — see [Session Memory](/getting-started/memory).

## Why Distill?

### The Problem

AI context windows have limits, and verbose documentation can quickly consume your budget:

- A comprehensive coding standards document might be 5,000 tokens
- You might want 10+ such documents in your context
- That's 50,000+ tokens just for standards, leaving little room for code

### The Solution

Distillation uses AI to compress content while preserving meaning:

- Every rule is kept, with its conditions and exceptions; restatement, motivation and extra examples are dropped
- More room for actual code and conversation

There is no size target. Verbose guidance shrinks a lot; dense guidance may barely shrink, and the distiller will not drop a rule to reach a length.

## How It Works

ctxloom uses a hybrid compression approach:

### AST-Based Compression (Code & JSON)

For structured content, ctxloom uses tree-sitter AST parsing for fast, deterministic compression:

| Content Type | Strategy |
|--------------|----------|
| **Go, Python, JS, TS, Rust, Java** | Preserve signatures, elide function bodies |
| **JSON** | Preserve structure, truncate low-entropy values |

This approach is:
- **Fast**: No API calls, instant compression
- **Deterministic**: Same input always produces same output
- **Structure-preserving**: Maintains navigational breadcrumbs

Tree-sitter parsing is built behind a build tag (`treesitter`) and only ships in
the `ctxloom-full` release artifact. The default `ctxloom` binary — the one
"recommended for most users" — compiles a stub instead: it never claims a code
file, so code content falls through to LLM compression the same as prose. JSON
compression has no such gate and works in every build.

### LLM-Based Compression (Prose)

For prose and documentation, ctxloom falls back to LLM compression. The model is
the **fast role** (`llm.defaults.fast`, or `bundle distill --llm <label>`), run as
one internal one-shot session whose turns are the items being distilled. The
prompt is ctxloom's built-in one unless a bundle you use ships a `distill`
command, which replaces it; a `distill` command the trust gate withholds stops the
run rather than falling back to the built-in prompt (`ctxloom review` settles it).

1. **Original content** is analyzed by an AI model
2. **Key information** is extracted and condensed
3. **Distilled version** is stored alongside the original
4. **Content hash** tracks when re-distillation is needed

### Compression Router

When you distill content, ctxloom automatically routes to the best strategy:

```
Code file (.go, .py, .js, etc.) → AST compression (ctxloom-full only; falls
                                    back to LLM compression in the default build)
JSON file → JSON structure compression
Markdown/prose → LLM compression
```

## Distilling Fragments

### Single Fragment

```bash
# Distill a specific fragment
ctxloom fragment distill my-bundle#fragments/coding-standards

# Force re-distillation even if hash matches
ctxloom fragment distill --force my-bundle#fragments/coding-standards
```

### Multiple Fragments

Distill one fragment at a time with `ctxloom fragment distill`, or distill a whole bundle (or a glob of bundles) in one pass with `ctxloom bundle distill`, which skips items that are already distilled and unchanged:

```bash
# Distill everything in a bundle that needs it
ctxloom bundle distill ./my-bundle.yaml

# Preview what would be distilled without doing it
ctxloom bundle distill ./my-bundle.yaml --dry-run

# Force re-distillation of every item
ctxloom bundle distill ./my-bundle.yaml --force

# Multiple files / globs
ctxloom bundle distill .ctxloom/content/bundles/v2/*.yaml
```

`bundle distill` reads single-file bundle documents. Your project's own bundles
live under `.ctxloom/content/bundles/v2/`; a file directly under
`.ctxloom/content/bundles/` is not loaded at all.

### Comparing Original and Distilled Content

```bash
# Show the original content
ctxloom fragment show my-bundle#fragments/coding-standards

# Show the distilled version
ctxloom fragment show --distilled my-bundle#fragments/coding-standards
```

## Using Distilled Content

### Automatic Selection

By default, ctxloom uses distilled content when available:

```bash
# Uses distilled versions automatically
ctxloom run -f my-bundle#fragments/coding-standards
```

### Prefer Original

To use original content instead:

```yaml
# In config.yaml
config:
  use_distilled: false
```

There is no per-run override. To compare the original and distilled versions of a fragment:

```bash
ctxloom fragment show my-bundle#fragments/coding-standards
ctxloom fragment show --distilled my-bundle#fragments/coding-standards
```

## Bundle Configuration

### In Bundle YAML

```yaml
version: "1.0"
fragments:
  verbose-standards:
    content: |
      # Comprehensive Coding Standards

      [5000 tokens of detailed documentation...]

    # After distillation, these fields are added:
    distilled: |
      # Coding Standards (Distilled)

      [2000 tokens of condensed key points...]

    content_hash: "sha256:abc123..."
    distilled_by: "claude-3-opus"

  keep-original:
    no_distill: true  # Prevent distillation
    content: |
      # Critical Exact Wording

      This content must be preserved exactly as written.
```

### Distillation Fields

| Field | Description |
|-------|-------------|
| `content` | Original, full content |
| `distilled` | AI-compressed version |
| `content_hash` | SHA256 hash of content (for change detection) |
| `distilled_by` | Model that created the distillation |
| `no_distill` | If true, never distill this fragment |

## When to Distill

### Good Candidates

- **Long reference documents** - Style guides, standards, best practices
- **Comprehensive tutorials** - Can be condensed to key points
- **API documentation** - Essential patterns and gotchas
- **Historical context** - Background info that's useful but verbose

### Poor Candidates

- **Code examples** - Exact syntax matters
- **Legal/compliance text** - Exact wording required
- **Configuration templates** - Need precise formatting
- **Short fragments** - Already concise, no benefit

### Using no_distill

```yaml
fragments:
  legal-disclaimer:
    no_distill: true  # Must preserve exact wording
    content: |
      IMPORTANT: This software is provided "as is"...

  code-template:
    no_distill: true  # Exact code matters
    content: |
      ```go
      func main() {
          // Exact template structure
      }
      ```
```

## Distillation Quality

### Compression Strategy

The built-in prompt compresses by restructuring, never by deleting the small words a rule's meaning lives in ("unless", "only when", "not"). It:

**Never alters:**
- Conditions, exceptions, scope and negation (a rule it cannot shorten safely is emitted unchanged)
- Identifiers: paths, symbols, commands, flags, config keys, URLs
- Code and literal patterns
- Rule strength ("must", "never" and "prefer" stay distinct)
- The number of distinct rules (it never merges two)

**Drops:**
- Restatement of a rule already given
- Motivational or philosophical passages
- Historical narration
- Extra examples beyond the clearest one
- Commentary about the document itself

It keeps a rationale that states a constraint, a trap or a rejected alternative, and cuts a "why" only when it restates the rule. Output is direct imperative statements, one rule per line where it can, and never JSON or XML.

### What Makes Good Distillation

- Every rule survives with its conditions intact
- Identifiers are reproduced exactly or omitted, never abbreviated
- The clearest example per concept is kept
- Redundancy and motivational text are gone

### Example

**Original (verbose):**
```markdown
# Error Handling in Go

Error handling is one of the most important aspects of writing reliable
Go programs. Unlike many other languages that use exceptions, Go takes
a different approach by treating errors as values that are returned
from functions. This design decision was intentional and reflects the
Go philosophy of being explicit about error conditions.

When a function can fail, it typically returns an error as its last
return value. The caller is then responsible for checking this error
and handling it appropriately. This might seem verbose at first, but
it makes the error handling path explicit and visible in the code...

[continues for 2000 more words]
```

**Distilled:**
```markdown
# Go Error Handling

- Errors are values, not exceptions
- Return error as last value: `func Foo() (Result, error)`
- Always check: `if err != nil { return err }`
- Wrap with context: `fmt.Errorf("operation failed: %w", err)`
- Use sentinel errors sparingly: `var ErrNotFound = errors.New("not found")`
- Handle at appropriate level, don't over-wrap
```

## Re-distillation

### Automatic Detection

ctxloom tracks content hashes, so it knows when a fragment's content has moved on from what its `distilled` version was built from. `fragment show` doesn't surface that status itself — the signal comes from `fragment distill`: running it again tells you whether it actually re-distilled or found nothing to do:

```bash
ctxloom fragment distill my-bundle#fragments/standards
# If unchanged: Fragment "standards" is already distilled and unchanged
# Otherwise: re-distills and reports the model used
```

### Triggering Re-distillation

```bash
# Re-distill a specific fragment
ctxloom fragment distill my-bundle#fragments/standards

# Force re-distill even if unchanged
ctxloom fragment distill --force my-bundle#fragments/standards
```

## Cost Considerations

Distillation runs model turns on the fast role's engine, which have costs:

- Each item LLM-distilled takes one turn; AST and JSON compression take none
- Longer content = more tokens = higher cost
- Re-distillation only happens when content changes

### Minimizing Costs

1. **Distill selectively** - Only distill fragments that benefit
2. **Batch distillation** - Distill all at once, not repeatedly
3. **Use content hashes** - Don't re-distill unchanged content
4. **Review before distilling** - Ensure content is stable

## Best Practices

1. **Distill after finalizing** - Don't distill work-in-progress
2. **Review distilled output** - Ensure key info is preserved
3. **Keep originals** - Distilled versions can be regenerated
4. **Document no_distill usage** - Explain why certain content shouldn't be distilled
5. **Version control both** - Commit both original and distilled versions

## Context Size Research

The 16KB warning isn't arbitrary. It is grounded in published research on how LLMs actually handle long context.

### Key Findings

1. **Continuous Degradation**: Performance degrades as input grows, not at a specific threshold. The [Context Rot study](https://trychroma.com/research/context-rot) (Chroma, 2025) found accuracy is highest for early tokens and declines continuously.

2. **Lost in the Middle**: The [Lost in the Middle](https://arxiv.org/abs/2307.03172) paper (Liu et al., 2023) found LLMs process information at the start and end of context more reliably than the middle—a U-shaped performance curve with >30% degradation for middle-positioned content.

3. **Effective vs Advertised**: The [Maximum Effective Context Window](https://arxiv.org/abs/2509.21361) research found most models show severe degradation by ~1,000 tokens, falling 99% short of advertised windows.

### ctxloom's 16KB Warning

ctxloom warns when assembled context exceeds 16KB (~4,000 tokens):

```
ctxloom: warning: assembled context is 24KB (recommended max: 16KB)
ctxloom: warning: large context may reduce LLM effectiveness; consider distillation or fewer fragments
```

This threshold is conservative - degradation varies by model and task. The warning encourages you to:
- Use distillation to compress verbose content
- Prioritize most relevant fragments
- Structure context with key information at start/end

### Optimization Strategies

| Strategy | Description |
|----------|-------------|
| Distill verbose content | Drop restatement and motivation, keep every rule |
| Front-load key info | Put critical instructions at the start |
| Summarize at end | Reiterate key points at context end |
| Use tags selectively | Include only relevant fragments |
| Profile per task | Different tasks need different context |
