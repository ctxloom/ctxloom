---
title: "Distillation"
---

Distillation compresses verbose context into token-efficient versions while preserving essential information. This helps you stay within context limits and reduce costs.

## Context Size Research

Understanding how LLMs process context helps optimize your setup.

### Key Findings

Recent research reveals important patterns in how LLMs handle context:

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
| Distill verbose content | Compress 5,000 tokens → 800 tokens |
| Front-load key info | Put critical instructions at the start |
| Summarize at end | Reiterate key points at context end |
| Use tags selectively | Include only relevant fragments |
| Profile per task | Different tasks need different context |

## Why Distill?

### The Problem

AI context windows have limits, and verbose documentation can quickly consume your budget:

- A comprehensive coding standards document might be 5,000 tokens
- You might want 10+ such documents in your context
- That's 50,000+ tokens just for standards, leaving little room for code

### The Solution

Distillation uses AI to compress content while preserving meaning:

- A verbose document can often be compressed 70-90% (e.g., 5,000 → 500-1,500 tokens)
- Essential rules and patterns preserved, verbose explanations removed
- More room for actual code and conversation

*Actual compression varies by content type—structured guidelines compress well, code examples less so.*

## How It Works

ctxloom uses a hybrid compression approach:

### AST-Based Compression (Code & JSON)

For structured content, ctxloom uses tree-sitter AST parsing for fast, deterministic compression:

| Content Type | Strategy |
|--------------|----------|
| **Go, Python, JS, TS, Rust** | Preserve signatures, elide function bodies |
| **JSON** | Preserve structure, truncate low-entropy values |

This approach is:
- **Fast**: No API calls, instant compression
- **Deterministic**: Same input always produces same output
- **Structure-preserving**: Maintains navigational breadcrumbs

### LLM-Based Compression (Prose)

For prose and documentation, ctxloom falls back to LLM compression:

1. **Original content** is analyzed by an AI model
2. **Key information** is extracted and condensed
3. **Distilled version** is stored alongside the original
4. **Content hash** tracks when re-distillation is needed

### Compression Router

When you distill content, ctxloom automatically routes to the best strategy:

```
Code file (.go, .py, .js, etc.) → AST compression
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

### All Fragments in a Bundle

```bash
# Distill all fragments that need it
ctxloom fragment distill my-bundle
```

### Checking Distillation Status

```bash
# Show fragment with distillation info
ctxloom fragment show my-bundle#fragments/coding-standards

# Show distilled version
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

```bash
# In config.yaml
defaults:
  use_distilled: false
```

Or per-run:

```bash
ctxloom run --no-distilled -f my-bundle#fragments/coding-standards
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

      [800 tokens of condensed key points...]

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

ctxloom's distillation uses an extractive approach designed to preserve actionable information while removing redundancy. The algorithm:

**Preserves (never removes):**
- Code syntax and exact patterns
- Function/file/variable names (breadcrumbs for navigation)
- Error handling rules and edge cases
- Actionable instructions ("DO X", "NEVER do Y")
- Technical constraints and requirements

**Compresses aggressively:**
- Verbose explanations of "why"
- Redundant examples (keeps 1 best example per concept)
- Motivational/philosophical content
- Historical context unless directly actionable

**Target:** 30-50% of original size while maintaining same structure.

### What Makes Good Distillation

- Preserves **key concepts** and **essential rules**
- Maintains **actionable guidance**
- Keeps **critical examples** (one per concept)
- Removes **redundancy** and **verbose explanations**
- Uses **bullet points and abbreviations** where clear

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

ctxloom tracks content hashes. When content changes, distillation is flagged as stale:

```bash
# Check if distillation is current
ctxloom fragment show my-bundle#fragments/standards
# Shows: "Distillation: stale (content changed)"
```

### Triggering Re-distillation

```bash
# Re-distill stale fragments
ctxloom fragment distill my-bundle

# Force re-distill everything
ctxloom fragment distill --force my-bundle
```

## Cost Considerations

Distillation uses AI API calls, which have costs:

- Each fragment requires one API call to distill
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
