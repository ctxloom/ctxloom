# CLI UX Principles

A reference for designing and auditing command-line interfaces in this family
of tools. It is tool-agnostic: apply it to any CLI here (or elsewhere). The
principles are distilled from the CLIs that people actually enjoy living in —
git, gh, docker, kubectl, taskwarrior — and from the failure modes that make
CLIs hostile: help that inventories instead of teaching, defaults that hide
data silently, errors that describe the wall instead of the door.

Each principle states the rule, why it holds, and a concrete check you can run
against a real CLI. The checks at the end form an audit checklist.

## 1. Help teaches; it does not merely inventory

A `--help` screen that lists flags alphabetically documents the parser, not
the tool. The best help answers the question the user actually arrived with:
*"how do I do the thing?"* — and the fastest teacher is a worked example.

- `gh pr create --help` leads with runnable examples before the flag table.
- `git rebase --help` walks scenarios, not just options.
- A flag whose value has its own syntax (a query language, a selector, a
  format string) MUST carry inline examples of that syntax. `kubectl` explains
  `-l 'app=nginx,tier!=frontend'` right in the help; the user never needs the
  source or a web search to write their first selector.

**Check:** can a first-time user go from `tool --help` to a successful
non-trivial invocation without reading anything else? If any flag takes a
mini-language, does its help show at least three worked expressions?

## 2. Progressive disclosure: simple things simple, complex things possible

The 90% path must need zero flags. Power appears in layers: a bare command
does the obvious thing; flags refine it; an advanced sub-language (queries,
selectors, format templates) is available but never required. Never make a
beginner pay attention-tax for an expert feature — and never hide the expert
feature so well it can't be found (see §5).

- `git log` works bare; `--graph --oneline --all` refines; `--pretty=format:`
  is the expert layer.
- `docker ps` works bare; `--filter status=exited` refines.

**Check:** what does the bare command with no arguments do? If the answer is
"error" or "nothing useful," the defaults are wrong.

**A bare LEAF and a bare NAMESPACE are different questions.** The check above is
about leaves — `git log`, `docker ps`, commands that *do* something. A namespace
(`ctxloom bundle`, `ctxloom trust`) is a container, and for a container the
useful answer is **help**: it teaches what the noun can do, which is the one
thing a user standing at that level actually needs.

So: a bare leaf does the obvious thing; a bare namespace teaches. Neither is
"nothing useful", and printing an inventory is not an upgrade on either.

**One exception, and ctxloom itself is it.** A namespace that carries its own
`RunE` is not a namespace for this purpose — it is a leaf that happens to have
children. Bare `ctxloom mcp` starts a stdio MCP server, and that invocation is
materialized into users' engine settings (`agent.CtxloomMCPArgs`). The rule is
therefore about namespaces with no behaviour of their own, which is the only kind
where "teach" is the useful answer.

Making a bare namespace *list* was considered in full and rejected. Of fifteen
production CLIs surveyed, twelve have a bare-noun case at all (`cargo`, `npm` and
`kubectl` have no noun namespaces to invoke bare). Of those twelve: five print
help (`gh`, `docker`, `flyctl`, `wrangler`, `stripe`), four error (`gcloud`,
`az`, `aws`, `terraform`), and three list (git's noun-commands, `systemctl`,
heroku by doctrine). The nearest cobra-based cousins, `gh` and `docker`, both
print help. The guideline literature does not settle it either:
Heroku's style guide mandates bare-lists while 12-Factor CLI Apps — written by
the Heroku CLI's own author — argues the opposite. And listing would cost the
noun its teaching surface at its most-typed spelling to save one word on nouns
that already carry a two-letter `ls` alias.

## 3. Sensible defaults — and defaults that hide data must say so

Defaults encode the tool's opinion of the common case, and a *view* default
(show active, hide finished; show local, hide remote) is usually right. But a
default that silently subtracts from an answer is a trap: the user asked a
question, got a truncated answer, and was not told.

The rule: **filtering defaults may hide; they may not hide silently when the
user is searching.** When an explicit query (a search term, a tag/label
filter) matches items that a view default then suppresses, say so — a one-line
count and the flag that reveals them:

    12 results shown; 30 more match but are completed — add --all to include them

git models this well: `git status` hints at every hidden state transition
("use git add ..." / "use git restore ..."); `git branch` shows local branches
by default but `-a` is famous *because the default output implies its own
incompleteness*. taskwarrior prints `(2 tasks hidden by filter)` style
footnotes. The anti-pattern is an empty result from data that exists.

**Check:** construct a query that matches only items the default view hides.
Does the tool print `(nothing)` — or does it tell you what it hid and how to
see it?

## 4. Consistent grammar: one way to say each thing, everywhere

Verbs, nouns, and flag names form a vocabulary; every inconsistency is a fact
the user must memorize. Within one tool — and across a family of tools —
the same concept must use the same word and the same flag:

- If `--json` selects machine output in one command, no sibling command may
  spell it `--format json` or `-o json`.
- If plural nouns list (`tool tags`, `tool statuses`), that pattern must hold
  for every listable concept.
- Repeatable flags, value separators, and case-sensitivity rules must match
  across commands.
- Argument order follows a stable convention (`tool verb <id> [modifiers]`).

kubectl's `get/describe/delete <kind>` grid is the exemplar: learn the grammar
once, guess the rest correctly. The measure of consistency is exactly that —
**guessability**.

**Check:** take a flag from one subcommand and predict its name and behavior
on a sibling. Were you right?

## 5. Discoverability: every feature reachable from `--help` alone

A feature that ships but cannot be found from the help tree does not exist.
Two obligations:

1. **The help tree advertises capabilities.** The root help and the relevant
   subcommand help must mention each capability at least once — if tasks can
   carry tags, `list --help` must show how to filter by them.
2. **The tool can enumerate its own vocabulary.** Any closed or user-grown set
   the user must supply values from — statuses, tags/labels, remotes, contexts
   — needs a read command that lists what exists (`git remote`, `git tag`,
   `docker context ls`, `kubectl get namespaces`). Write-only vocabularies
   (you can attach a label but never list the labels in use) force users to
   keep external notes, and stale/typo'd values accumulate invisibly.

**Check:** for every value-set a flag accepts, is there a command that lists
the current values? Grep the help tree for each major feature: does it appear?

## 6. Errors are teachers: what failed, why, and the next command

An error message is the UI moment with the user's fullest attention. Spend it.
The template: **what was wrong → why → the concrete next step** (a flag, a
corrected invocation, or a help page to read).

- git: `did you mean 'git status'?`; push failures print the exact
  `git push --set-upstream ...` to run next.
- A parse error in a query language must point back at the query syntax help,
  ideally with a valid example — the user is one line away from success.
- Validation errors name the flag to use, not just the invariant violated
  ("pass --add <tag> and/or --remove <tag>", not "at least one tag required").

**Check:** trigger each error path deliberately. Does every message contain an
actionable next step, in the tool's own flag vocabulary?

## 7. Streams and exit codes are contracts

- **stdout is data; stderr is commentary.** Results go to stdout; hints,
  warnings, progress, and provenance notes go to stderr, so pipes stay clean.
  A hint (per §3) must never corrupt `tool list --json | jq`.
- **Exit codes are truthful.** 0 means the operation succeeded *and delivered
  its effect*; a run that succeeded at doing nothing the user asked for is not
  a success. Never print an error and exit 0.
- **Output is quiet by default, informative on demand.** No banner, no
  self-congratulation; one line per fact.

### The exit-code ladder

ctxloom's own codes, in full. A wrapped engine's exit code passes through
unchanged, so these are ctxloom's answers only when ctxloom is the one
answering.

| Code | Meaning |
| ---- | ------- |
| `0` | Success: the command ran and delivered its effect. |
| `1` | ctxloom's own error. The fallback for anything unclassified. |
| `2` | **Refused.** The command ran to completion and *deliberately did not do* something it was asked to do. Nothing failed. |
| `3` | Startup aborted over collected fatal findings (strict mode). |

**The ladder is categorical, not ordered by severity.** Read it as a set of
distinct answers to "what happened?", never as a scale: `3` (a startup abort)
is a far more serious outcome than `1` (a mistyped flag). Code that treats a
higher number as a worse outcome is wrong about this vocabulary.

`2` exists because the two neighbouring codes both lie about a refusal. `1`
reports a decision as a fault and sends the user hunting a problem on their own
machine that is not there. `0` makes an unattended run that refused
indistinguishable, to the script that ran it, from one that had nothing to do —
the same silence the bullet above forbids. Today's sole use: `ctxloom remote
upgrade` declining to advance a pin onto content whose publisher signature does
not verify over its bytes (`exitCodeRefused`, `internal/cli/startup_helpers.go`,
beside `exitCodeFatalFindings`).

A refused command still prints its full explanation on the way out; the code is
for the script, the message is for the human. Nothing prints an `Error:` line
on top of it — a refusal is not an error report.

**Check:** pipe every read command through a consumer; does anything
non-data leak into stdout? Grep for error paths that forget a nonzero exit.
For any command that can decline part of what it was asked: does it exit `2`,
and does a test assert the code rather than only the message?

## 8. Human-first output with a machine escape hatch

Default output is a table or line format designed for eyes: aligned, scannable,
no decoration that survives a copy-paste badly. Every read command also offers
a stable machine form (`--json`) whose schema is the compatibility surface.
Never make humans parse JSON, and never make scripts parse the human table.

**Check:** does every listing command have `--json`? Is the human output
self-describing (a count like `3 active`, not a bare `3` whose meaning the
reader must guess)?

## 9. Tags, labels, and filters: the full loop or nothing

Tagging systems live or die on a closed loop of four verbs, and the best
task/label CLIs (taskwarrior, todoist, gh label, docker/kubectl labels) ship
all four:

1. **Attach/detach** — symmetric add and remove on one surface.
2. **Enumerate** — list the tags in use, with counts (§5.2). Counts matter:
   they tell the user which tags are alive and expose typo-twins
   (`release` 40, `realease` 1).
3. **Filter** — query items by tag, composable with the tool's other filters.
4. **Display** — tags visible on the item in normal listings, so the
   vocabulary reinforces itself passively.

If the filter step has its own expression syntax (boolean combinators,
selectors), that syntax is a feature-within-a-feature and needs its own
teaching (§1): inline examples covering AND, OR, NOT, and one composed
expression. Unusual syntaxes (postfix/RPN, label selectors) are fine *only*
with proportionally better inline documentation — the cost of novelty is paid
in help text.

**Check:** walk the loop — attach a tag, list tags, filter by it, see it in
output, detach it. Any missing verb breaks the loop.

## 10. Never break the installed base casually

Renaming commands, repurposing flags, or flipping a long-standing default is a
different class of change from everything above: it invalidates muscle memory,
scripts, and documentation. Additive fixes (a new subcommand, a hint on
stderr, richer help, a new flag) are always safe; subtractive or semantic
changes need a deprecation story and an explicit decision — they are never
routine polish.

**Check:** for each proposed change, would an existing script or habit behave
differently? If yes, it needs sign-off, not a patch.

---

## Audit checklist

- [ ] First-run test: `--help` alone gets a new user to a successful real invocation.
- [ ] Every mini-language flag (queries, selectors, formats) has ≥3 inline worked examples.
- [ ] Bare invocation of each read command does something useful.
- [ ] No default silently hides items an explicit query matched — hidden-match counts + revealing flag are printed.
- [ ] Flag names, output shapes, and argument order are guessable across sibling commands.
- [ ] Every value-vocabulary a flag accepts has an enumeration command.
- [ ] Every error message names the concrete next step in flag vocabulary.
- [ ] stdout carries only data; hints/warnings/provenance go to stderr; exit codes truthful.
- [ ] Every listing has `--json`; human output is self-describing.
- [ ] Tag/label loop closed: attach, enumerate (with counts), filter, display.
- [ ] No command renames / default flips without an explicit compatibility decision.
