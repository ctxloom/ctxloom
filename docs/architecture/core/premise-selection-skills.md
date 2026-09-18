# Premise selection for SKILLS — can a premise predict the moment a skill is invoked?

A trial, not a feature. The question the human set: **pick a subset of skills, mine git
history and transcripts for the moments a model invoked or should have invoked each, author a
premise per skill, and see whether the premise predicts those moments.** Added up front: get
data on the prompt the engine ACTUALLY uses to match skill descriptions, because every premise
should be shaped by what the matcher reads.

This document is the record of that trial in the style of [premise-selection.md](premise-selection.md),
which holds the design of record and the three measured authoring properties this trial reuses.

## Phase 0 — what the engine's skill matcher actually reads

Source: the claude-code tree at `~/workspace/claude-code-src/src` (read-only, outside this
repo). Quoted verbatim; paths are the authority. The running build on this box is NEWER than
this snapshot (see "the instruction" below), so treat the constants as the shape of the
mechanism rather than the exact numbers in production.

### The text about each skill

One bullet per skill, **name + description**, with an optional `when_to_use` frontmatter field
appended after ` - `. `src/tools/SkillTool/prompt.ts`:

```ts
// Per-entry hard cap. The listing is for discovery only — the Skill tool loads
// full content on invoke, so verbose whenToUse strings waste turn-1 cache_creation
// tokens without improving match rate. Applies to all entries, including bundled,
// since the cap is generous enough to preserve the core use case.
export const MAX_LISTING_DESC_CHARS = 250

function getCommandDescription(cmd: Command): string {
  const desc = cmd.whenToUse
    ? `${cmd.description} - ${cmd.whenToUse}`
    : cmd.description
  return desc.length > MAX_LISTING_DESC_CHARS
    ? desc.slice(0, MAX_LISTING_DESC_CHARS - 1) + '…'
    : desc
}
...
  return `- ${cmd.name}: ${getCommandDescription(cmd)}`
```

The frontmatter fields read for the listing are `description` (falling back to the first
markdown line when absent) and `when_to_use` — `src/skills/loadSkillsDir.ts`,
`parseSkillFrontmatter`: `whenToUse: frontmatter.when_to_use as string | undefined`. Nothing
else reaches the listing: not the body, not tags, not `metadata`.

**Budget.** `SKILL_BUDGET_CONTEXT_PERCENT = 0.01` of the context window in characters
(`DEFAULT_CHAR_BUDGET = 8_000`). When the full listing exceeds it, `formatCommandsWithinBudget`
keeps bundled (Anthropic-shipped) descriptions whole and trims user/project/plugin descriptions
to an equal share; when that share falls below `MIN_DESC_LENGTH = 20`, non-bundled skills are
listed **names only**.

**Vehicle.** A system-reminder-wrapped meta user message, not the tool schema —
`src/utils/messages.ts`, `case 'skill_listing'`:

```ts
content: `The following skills are available for use with the Skill tool:\n\n${attachment.content}`,
```

The reminder carries no selection instruction of its own: the header line and the bullets.

### The instruction

The Skill tool's description, `src/tools/SkillTool/prompt.ts`, `getPrompt`:

```
Execute a skill within the main conversation

When users ask you to perform tasks, check if any of the available skills match. Skills provide specialized capabilities and domain knowledge.

When users reference a "slash command" or "/<something>" (e.g., "/commit", "/review-pr"), they are referring to a skill. Use this tool to invoke it.
...
Important:
- Available skills are listed in system-reminder messages in the conversation
- When a skill matches the user's request, this is a BLOCKING REQUIREMENT: invoke the relevant Skill tool BEFORE generating any other response about the task
- NEVER mention a skill without actually calling this tool
- Do not invoke a skill that is already running
- Do not use this tool for built-in CLI commands (like /help, /clear, etc.)
- If you see a <command-name> tag in the current conversation turn, the skill has ALREADY been loaded - follow the instructions directly instead of calling this tool again
```

The system prompt adds one bullet when skills are present — `src/constants/prompts.ts`,
`getSessionSpecificGuidanceSection`:

```
/<skill-name> (e.g., /commit) is shorthand for users to invoke a user-invocable skill. When executed, the skill gets expanded to a full prompt. Use the Skill tool to execute them. IMPORTANT: Only use Skill for skills listed in its user-invocable skills section - do not guess or use built-in CLI commands.
```

An experimental variant exists behind `EXPERIMENTAL_SKILL_SEARCH` (`getDiscoverSkillsGuidance`),
where user/project skills are dropped from the listing and surfaced per turn instead:

```
Relevant skills are automatically surfaced each turn as "Skills relevant to your task:" reminders. If you're about to do something those don't cover — a mid-task pivot, an unusual workflow, a multi-step plan — call DiscoverSkills with a specific description of what you're doing. Skills already visible or loaded are filtered automatically. Skip this if the surfaced skills already cover your next action.
```

**The sentence this trial's own session received — "When the task at hand is one a listed
skill covers, call this tool first" — is not in this source tree.** The running build's Skill
description ("Invoke a skill. A skill is a packaged set of instructions…") post-dates the
snapshot. Both versions are request-keyed: neither tells the model to judge skills one at a
time or to include on a borderline call. There is no coordinator-mode variant. Subagents get
their own turn-0 listing keyed by `agentId`. MCP skill builders (`src/skills/mcpSkillBuilders.ts`)
feed the same `- name: description` formatter.

### Once, or re-presented

`src/utils/attachments.ts`, `getSkillListingAttachments`: the listing is **incremental per
agent**. A module-scope `sentSkillNames: Map<agentId, Set<name>>` records what has been
announced; each turn emits only names not yet sent (`newSkills = allCommands.filter(cmd =>
!sent.has(cmd.name))`, and no attachment when that is empty). So the full listing appears once
at turn 0, then a delta only when a skill is added mid-session. On `--resume`,
`suppressNextSkillListing()` marks everything as sent and emits nothing. **After compaction the
listing is NOT re-sent** — `src/services/compact/compact.ts`:

```ts
// Intentionally NOT resetting sentSkillNames: re-injecting the full
// skill_listing (~4K tokens) post-compact is pure cache_creation with
// marginal benefit. The model still has SkillTool in its schema and
// invoked_skills attachment (below) preserves used-skill content.
```

(A docstring in the same file says the opposite; the code comment beside the behaviour is the
one that holds.) Consequence: after a compact, the model keeps the tool and loses the
descriptions unless the summary carried them — a skill never used before the compact is
effectively invisible for the rest of the session.

### Ordering and ranking

None. Order is `loadAllCommands` concatenation (`src/commands.ts`): bundled → builtin-plugin →
skills-dir (user/project) → workflow → plugin commands → plugin skills; builtins are filtered
out. Inclusion (`getSkillToolCommands`): prompt-type, not `disable-model-invocation`, and
either from a skills dir/bundled or carrying an explicit description or `when_to_use`.

### What this means for authoring

- The engine matches on **one line of ≤250 characters**. A premise for a skill that the engine
  will read is that line; anything longer is cut with an ellipsis and the tail is never seen.
- The instruction is **request-keyed and turn-0-delivered**. The engine directs the model to
  match "the user's request"; it never tells it to re-check the list at a mid-task moment. A
  skill whose moment is agent-initiated (closeout "at the end of a unit of work", prompt-human
  "before ending a work session", check-triggers on a revived trigger) has no instruction on its
  side.
- The listing is a **menu**. The prior trial measured that shape at 0.49 recall against 0.76 for
  per-premise judgement — the single largest effect it found.

## Phase 1 — the instrument, recovered

[premise-selection.md](premise-selection.md) records that the fixture, runs and scorer were
deliberately removed. They were recovered from `e7005efbc^` for this trial:
`scripts/score_premises.py`, `internal/operations/testdata/premise_runs/README.md` (the 15
runs), and `premise_situations_mined.yaml` (the format). The scorer's invocation:

    score_premises.py <situations.yaml> <answers.txt> [corpus.yaml] [premised_corpus.yaml]

Answers are one `S01: name, name` or `S01: NONE` per line. It prints precision, recall, F1,
**F2 (headline, per the over-select ruling)**, silent drop, mean selected vs expected, false
fire on nothing-applies rows, and exact-set count. The runs it scored were produced by a
separate model judging each premise ALONE against every situation — the per-premise protocol,
the best-scoring configuration the prior trial found.

The corpus here, `internal/operations/testdata/premise_corpus_skills_v0.yaml`, carries both
`fragments` (v3's shape: ref, content, premise, tags — plus the skill's existing
`description`, which is what measurement #1 compares against) and `situations` (the
situations-file shape) so the recovered scorer reads it unchanged.

### Where the engine's matcher differs from the three measured properties

All three, in the direction the prior trial measured as worse:

| property (index prompt) | the engine's Skill matcher |
|---|---|
| judge each premise on its own | a flat bullet menu: "check if any of the available skills match" |
| borderline resolves toward including | no tie-break; "BLOCKING REQUIREMENT" on a match and "do not invoke a skill that is already running" both push a doubtful case toward not invoking |
| match the imminent action, not the context | "when users ask you to perform tasks" / "matches the user's request" — keyed to the request at turn start, delivered once, never re-surfaced |

That difference is itself a finding: a premise authored for ctxloom's index and the same text
placed in a skill's `description` are read under two different instructions, and the prior
trial's numbers say the second instruction under-selects.

### The prior trial's unrun control is this trial's measurement #1

The runs README names the experiment that separates premise from body knowledge: "replace every
premise with its fragment NAME alone and re-run. If recall holds, body knowledge is doing the
work and the premises are decoration." For a skill the honest control is its existing
`description` — that is what the engine already shows. So the design here is two arms on the
same situations and the same selector protocol: **description as premise** vs **authored
premise**. If the description arm matches the invocation moments as well as the authored arm,
the premise is decoration for that skill.

## Phase 2 — mine, author, score

### Subset

This repo's own `.claude/skills` — closeout, admit, unattended, prompt-human, design-by-test —
and the two ctxloom-shipped commands, check-triggers and recover.

### Mining

Two sources, both read-only: ~1,050 engine transcripts under
`~/.ctxloom/sessions/*/persist/transcripts/` and ~430 ctxloom transcript streams at
`~/.ctxloom/sessions/*/persist/transcript.jsonl`. A Skill `tool_use` record is the ground truth
for "invoked". For "should have", every user turn was swept for phrasings that name what a skill
does (not its name), and every turn with a hit and no Skill call in the response was read and
judged by hand. Agent-side moments (check-triggers, design-by-test) were found by searching the
assistant's own text for the situation the body describes.

Model invocations of the seven, across both sources: prompt-human 37, closeout 25, unattended
20, admit 11, design-by-test 1, recover 1, check-triggers 0. (`good-night` 12 and
`pending-decisions` 1 are earlier names.) The human typed `/recover` 17 times; no one ever
invoked check-triggers by any route.

**Labelled BLIND**: the situations were written and committed (`2c15d952b`) with every premise
slot empty, before any premise was authored. `expect` was judged against the skill bodies.

**Situation count**: 89 — 29 invoked, 43 should-have, 17 negative (11 mined, 6 authored).

**How should-have was judged**: (a) the human's turn, read on its own, asks for what the body
does, by purpose rather than by name ("wrap it up", "run them by me one at a time", "are we
ready to good night that?"); or (b) a hook or brief named the skill; or (c) the agent itself
described the situation the body exists for and then did the work by hand ("its Deferred
sibling's trigger has fired, so let me settle that first"). Two misses are confirmed by the human
in the transcript: "we have a skill similar to wrap it up that didn't just trigger" and "you
didn't trigger good night protocol and work that last night". Several prompt-human and admit
misses occur in sessions where the body had been loaded earlier; the engine's "already running"
rule explains the non-call, and the moment still applies.

What the invoked set says before any premise is scored: **nearly every model invocation follows
a user turn that names the skill's action near-literally, or a Stop hook that names the skill.**
Agent-initiated invocations exist but are rare: closeout on a gate notification (twice),
prompt-human as the tail of closeout (twice), admit on a sub-agent's deferral (once),
design-by-test before a dispatch (once). check-triggers, whose moment is always agent-side, was
never invoked.

_(Premises, score, and the per-skill verdict follow in the commits after this one.)_
