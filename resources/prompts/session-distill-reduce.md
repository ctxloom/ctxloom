You are merging several already-distilled partial summaries of a single work session into one coherent essence. The input inside `<session_log>` is the concatenation of those partial summaries, separated by `---` rules. Each was distilled from a consecutive chunk of the same conversation, so they overlap and must be unified — not re-summarized chunk by chunk.

Your job is to merge, deduplicate, and order — not to compress further for its own sake. Preserve all distinct information; only collapse genuine repetition.

## Output Format

Begin your output with a YAML frontmatter block in this exact form — this is mandatory; the resume picker fails to render a row summary without it:

    ---
    summary: <a few words — a terse title, ≤40 characters, no quotes, no trailing period>
    ---

The summary line is a very short title — just a few words naming what the whole session was about. It is used verbatim as the session's name, so make it terse and label-like (think a folder name, not a sentence). Do not begin it with conversational filler ("Looking at", "Let me", "Here is"); lead with the work itself. Examples:
  - Bundle review on startup
  - Parallelized chunk distillation

After the closing `---` and a blank line, emit the unified body with these sections (omit a section only if it is genuinely empty):

### Open Items
- [merged pending items, deduplicated, most important first]

### State
[unified current state of the work]

### Decisions
- [merged decisions]

### Completed
- [merged accomplishments]

### Key Context
- [merged context for the next session]

## Rules

- **Never drop identifiers under the merge.** Carry through verbatim, character-for-character, every: file path, directory path, function/type/symbol name, command line, session ID and harp name (e.g. `soft-idle-scone`, UUIDs), and URL that appears in any partial summary. Losing one breaks resuming the work. When two partials mention the same path, keep it once; never paraphrase it away.
- Deduplicate overlapping bullets, but if two partials disagree, keep both and note the divergence rather than silently picking one.
- Output the frontmatter summary line first, always.
- A character budget is appended below. It is an ABSOLUTE budget and does not
  scale with how long the session was: the essence is re-injected into a FRESH
  context window at resume, and that window does not grow because the session
  did. A longer session means more compression, not a longer essence.
