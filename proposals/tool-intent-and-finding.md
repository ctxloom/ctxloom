# Proposal: tool-intent-and-finding — a new WHY (F6)

DRAFT for the human to review and rewrite in their own voice. The fragment
itself is NOT edited: it lives at
`.ctxloom/content/bundles/v2/ctxloom-project/fragments/tool-intent-and-finding.md`
(with its `.distilled.md` beside it), and the assembled form is pinned in
`internal/core/composite/testdata/assemble.golden` — both in THIS repo. When the
text is settled: edit the fragment, regenerate its `.distilled.md`
(`ctxloom fragment distill`), then regenerate the golden
(`CTXLOOM_UPDATE_GOLDEN=1 just test-pkg ./internal/core/composite/`).

## What changes and why

The rule stays; its reason was ctxloom's own session compaction, which is gone.
The current "Why" section describes ctxloom's essence ("A session transcript is
compacted into an essence that a later session resumes from"; "the essence
records that a command ran"). The engines now own compaction, and the same
argument holds for theirs: a summary of a long session keeps what was SAID and
reduces tool output to its shape, or drops it.

Claim to verify before adopting: that claude-code's (and opencode's) own
compaction summaries reduce or drop tool results while keeping the
conversational text around them. The measured figures below are this
project's transcripts and are unchanged; the `[2,431 bytes, 47 lines]`
illustration was the shape ctxloom's compactor wrote and should be replaced
by (or relabelled as) an example of what an engine's summary keeps.

## Proposed replacement for the "Why" section

## Why this is a rule and not a style preference

A long session does not stay in the window. Your engine compacts it — it
replaces the conversation with a summary and carries on — and tool RESULTS do
not survive that. They are reduced to their shape, or dropped, because a
truncated fragment of a grep is neither the information nor a summary of it,
and the content is re-derivable by asking again.

What is NOT re-derivable is what you were trying to find out, and what the
answer changed. The two sentences you write either side of a tool call are
ordinary conversation, so they are what the summary carries forward. Omit them
and what survives is that a command ran, with the reasoning gone.

Measured on this project's own transcripts: tool traffic is 50-69% of a
session. Bash already enforces the first half — `description` is a required
field, at 100% coverage across 1,286 calls. The second half is stated after
12% of tool results. That gap is the entire cost.

## Proposed opening line (unchanged in substance)

Two short statements per tool call. They are not narration — they are the only
part of a tool interaction that survives your engine's compaction.
