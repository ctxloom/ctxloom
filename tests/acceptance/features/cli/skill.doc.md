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
now exists end to end: author a package, sync its manifest, materialize it
into an engine's own skills directory with its files' permissions intact,
curate which skills a profile actually exports, and move a package between
machines through export/import — with a signature that is reported honestly,
never silently upgraded into trust it did not earn.
<!-- /doc:intro -->

<!-- doc:scenario: Alice authors a skill package and its manifest, listing, and show all reflect the real tree -->
A skill is a directory, not a single text blob — there is no `content:` field
to fill in. `ctxloom skill create` scaffolds `SKILL.md` with frontmatter that
already passes validation (the `name` field is generated to match the
directory), and everything after that is ordinary file authoring: this
scenario adds a `scripts/` file itself, exactly as a human author would.

`ctxloom skill sync` is the step that actually matters for safety later: it is
what computes the per-file manifest (a sha256 and a POSIX mode for every file)
and writes it into `bundle.yaml`. Until sync has run once, a skill's `files:`
map is empty and a fresh parse of the tree is trusted unconditionally — after
sync, any drift between what's recorded and what's on disk is a loud withhold,
never a silent pass-through. The manifest recorded here is the same one a
later export/import round-trip signs and verifies.
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

<!-- doc:scenario: On kiro, a bundle-authored skill wins over the builtin command of the same name -->
kiro is the one engine where this split actually collides on disk: both a
slash command and a true Agent Skill land in the same `.kiro/skills/`
directory, because kiro has only one native surface for both ideas. ctxloom
ships built-in slash commands (`/recover`, `/check-triggers`, `/discover`)
under exactly those bare names — so a project that authors its own skill
sharing one of those names is a real, not contrived, collision.

The resolution is deliberate and one-directional: the richer native package
(the skill) wins, the command's rendering of that name is dropped before it
is ever written, and ctxloom says so out loud rather than silently shadowing
one file with another.
<!-- /doc:scenario -->

<!-- doc:scenario: A skill's signature is reported honestly on import, and its files always land byte-for-byte -->
Importing a skill archive is deliberately never a trust decision by itself.
The tree lands — reviewable, exactly like any other freshly-pulled content —
regardless of whether its signature checks out; what varies is only how
honestly ctxloom reports what it found. A signature from a publisher this
machine actually trusts (Trent, here) is reported "verified"; the identical
signature from a key nobody has chosen to trust (Mallory) is reported
"unverified" — same mechanism, same package, different trust root, and the
files that landed are byte-for-byte identical either way. Trust is a human
decision layered on top of an honest report, never an automatic upgrade one
way or the other.
<!-- /doc:scenario -->

<!-- doc:scenario: A skill package tampered with after signing fails verification even though it was legitimately signed -->
A signature covers exact bytes, not a name or a good intention. Here Trent
legitimately signs the package, and only afterward does its `SKILL.md` change
on disk — the same shape a compromised build step or a corrupted transfer
would produce. Re-exporting after the change packs the *new* bytes, and the
old signature — which only ever covered the original manifest — no longer
verifies against them. The report is the same honest "unverified" the
untrusted-signer case gets, because to the verifier the two situations are
the same fact: these exact bytes are not covered by a signature this machine
can stand behind.
<!-- /doc:scenario -->

<!-- doc:outro -->
What this journey does not do: drive a live model to actually invoke a skill
mid-conversation. That is a real engine's own progressive-disclosure
behavior, out of scope for a hermetic acceptance run — this journey's promise
stops at "the right bytes, in the right place, with the right permissions,
and an honestly-reported signature," which is exactly the boundary ctxloom
itself owns.
<!-- /doc:outro -->

## Where it lands

| Engine | Skill folder |
|---|---|
| claude-code | `.claude/skills/<name>/SKILL.md` |
| kiro | `.kiro/skills/<name>/SKILL.md` |
| opencode | `.opencode/skill/<name>/SKILL.md` |
| codex | `.codex/skills/<name>/SKILL.md` |

Note `opencode` uses `skill/`, singular, where everyone else uses `skills/`.
That is the kind of detail that costs an afternoon when you are placing files by
hand, and it is the reason this is worth automating rather than documenting.

The shape is the same everywhere: a directory, a `SKILL.md`, and whatever else
the skill needs beside it. A skill is not a single file — it is a small tree, and
it arrives as one.
