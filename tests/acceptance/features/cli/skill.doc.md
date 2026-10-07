<!--
skill.feature narration companion.

Prose ONLY. It never restates what the Gherkin already says business-readably,
and carries no assertions of its own — skill.feature next to it is the single
source of truth for what this journey promises. Marker convention: an opening
prose block, one block per scenario keyed to that scenario's exact name (a
Scenario Outline's marker uses the Outline's own name once — not repeated per
Examples row), and a closing block — the same convention j000200_setup.doc.md and
j000400_multi_engine.doc.md already use.
-->

<!-- doc:intro -->
ctxloom used to have exactly one item called a "skill" — a slash command, the
thing a developer types `/name` to invoke. Every engine that has adopted the
Agent Skills convention (agentskills.io: a `SKILL.md` directory an assistant
loads *on its own*, by progressive disclosure, never typed by a human) uses
the word "skill" for that different thing. ctxloom's old name collided with a
real capability it did not otherwise expose at all.

This journey is the proof that the collision is resolved and the real thing
now exists end to end: author a package, materialize it
into an engine's own skills directory with its files' permissions intact,
curate which skills a profile actually exports, and move a package between
machines through export/import, byte for byte.
<!-- /doc:intro -->

<!-- doc:scenario: Alice authors a skill package and its listing and show reflect the real tree -->
A skill is a directory, not a single text blob — there is no `content:` field
to fill in. `ctxloom skill create` scaffolds `SKILL.md` with frontmatter that
already passes validation (the `name` field is generated to match the
directory), and everything after that is ordinary file authoring: this
scenario adds a `scripts/` file itself, exactly as a human author would.

Nothing records the package's files anywhere else, so there is nothing to
keep in step: `ctxloom skill show` lists what is in the directory now — every
file with its sha256 and POSIX mode.
<!-- /doc:scenario -->

<!-- doc:scenario: A curated skill materializes into claude's native Agent Skills directory with its exec bit intact -->
Materializing a skill is not "copy the SKILL.md file" — the whole package
travels, and a script's executable bit is load-bearing: a skill that shells
out to a script an engine can't execute is a skill that silently doesn't
work. This scenario proves both halves at once: the frontmatter/body content
landed at `.claude/skills/reviewer/SKILL.md`, and the sibling script landed
next to it with its `+x` bit still set — the same tree -> archive -> extract
-> materialize path the export/import scenarios below exercise for real
distribution.

A profile's `skills:` list is what makes this an intentional export rather
than an accident of "every bundle this profile touches exports everything":
naming the skill here is what turns delivery on.
<!-- /doc:scenario -->

<!-- doc:scenario: Profile skill curation exports only the curated skill, not the bundle's other one -->
The flip side of the previous scenario: once a profile's `skills:` list is
non-empty, it is exhaustive, not additive. The bundle here ships two skills,
`reviewer` and `planner`; naming only `reviewer` in the profile's curated list
means `planner` is not exported at all — not disabled per engine, not written
and then hidden, simply never materialized. A team that wants to hand one
engine a narrower slice of a shared bundle's skills does not need a second
bundle to do it.
<!-- /doc:scenario -->

<!-- doc:scenario: A skill's files survive export and import byte-for-byte -->
Importing a skill archive lands the package's tree in another bundle exactly
as it was authored: every file, byte for byte, its script still executable.
The archive goes through the hardened extractor first, so a malformed or
hostile archive is refused before anything lands.
<!-- /doc:scenario -->

<!-- doc:outro -->
What this journey does not do: drive a live model to actually invoke a skill
mid-conversation. That is a real engine's own progressive-disclosure
behavior, out of scope for a hermetic acceptance run — this journey's promise
stops at "the right bytes, in the right place, with the right permissions,"
which is exactly the boundary ctxloom
itself owns.
<!-- /doc:outro -->

## Where it lands

| Engine | Skill folder |
|---|---|
| claude-code | `.claude/skills/<name>/SKILL.md` |
| opencode | `.opencode/skill/<name>/SKILL.md` |
| codex | `.codex/skills/<name>/SKILL.md` (harpless static path — see codex.CodexHookWriter.SettingsPath) |

codex's row is the long one because codex is the only engine whose config home
ctxloom relocates: its skills hang off `$CODEX_HOME`, and ctxloom points that at
a project-scoped directory in the gitignored state tier rather than at a
`.codex` in your project root.

Note `opencode` uses `skill/`, singular, where everyone else uses `skills/`.
That is the kind of detail that costs an afternoon when you are placing files by
hand, and it is the reason this is worth automating rather than documenting.

The shape is the same everywhere: a directory, a `SKILL.md`, and whatever else
the skill needs beside it. A skill is not a single file — it is a small tree, and
it arrives as one.
