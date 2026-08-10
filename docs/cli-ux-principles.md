# CLI UX Principles

A reference for designing and auditing command-line interfaces in this family of
tools. It is tool-agnostic: apply it to any CLI here, or elsewhere.

The principles come from CLIs people are happy to live in — git, gh, docker,
kubectl, taskwarrior — and from the specific ways CLIs turn hostile. Help that
inventories the parser instead of teaching the tool. Defaults that drop data
without saying so. Errors that describe the wall rather than the door.

Each principle states the rule, why it holds, and a check you can run against a
real CLI. The checks are collected at the end as an audit checklist.

---

## Decisions: what ctxloom implements

Three rules govern the command surface. Everything below this section is the
reasoning behind them.

| You type | You get |
| --- | --- |
| `ctxloom <leaf>` | it acts |
| `ctxloom <noun>` | it shows you the noun's safe default — usually a listing |
| `ctxloom <anything> help` | it teaches |

**A bare leaf acts.** `ctxloom run` runs. Unchanged.

**A bare noun gives its safe, sane default.** Usually that is a listing:
`ctxloom bundle` prints the bundle listing rather than a help screen, following
`git remote`, `systemctl` and the heroku CLI. Where a listing is not the right
answer, a summary or another read-only view of the named resource serves the
same purpose. `<noun> list` and its `ls` alias keep working — the bare form is
an additional spelling, not a replacement, so no existing invocation breaks.

The preference order is explicit, because it decides every borderline noun:

1. A safe, sane default for the named resource — a listing, a summary, or
   another read-only view. **Always preferred where one exists.**
2. Help, as the fallback, when the noun has no such default. A noun with nothing
   sensible to show still teaches rather than failing.
3. Never an error. Erroring is not on the ladder.

A safe default beats help; help beats an error. The bare form always answers
with something worth having.

The ladder has no exception for a namespace that could plausibly act. `mcp` is
the case that tests it: it holds a stdio server, which is behaviour a namespace
could carry directly. It does not. The server is the `serve` leaf, one spelling,
symmetric with `acp serve`, and bare `ctxloom mcp` lists the configured servers
like every other noun.

That is a deliberate cost. The machine surface a caller invokes must be a leaf
whose whole job is that surface, because the alternative puts two audiences on
one spelling: a person exploring, and a protocol client whose transport is the
same stdout. Only one of them can be served, and serving the machine makes the
noun the one place in the tree where typing it does something a person did not
ask for. `ctxloom mcp` off a terminal therefore refuses and names `mcp serve` —
a listing written into a JSON-RPC pipe is indistinguishable from a hang.

**`--help` is a universal suffix, and it is always present.** Append it to
anything, at any depth: `ctxloom --help`, `ctxloom bundle --help`, `ctxloom
trust signer --help`, `ctxloom run --help`. There is no command where it is
missing and no level where it stops working. `-h` works everywhere it does
today.

It has to be the flag rather than a bare `help` word, for addressability. A bare
word in suffix position competes for the same slot as a positional argument, so
on a leaf taking open-ended text — `ctxloom search help`, `ctxloom run help` —
one meaning has to lose, and whichever loses becomes silently unreachable. A
flag lives in a different namespace from operands and collides with nothing.

**There is no bare `help` command at any level, including the root and the
namespaces**, where no positional would have collided with it. The carve-out is
declined on purpose: an affordance present at some levels and absent at others
is one the user has to model rather than simply use, and this rule's whole value
is that it needs no model. One spelling everywhere beats one spelling plus an
exception, even an exception in the user's favour.

Nothing is exempt from the flag, including the namespaces above. A noun whose
bare form answers with a listing still teaches under `--help`: `ctxloom mcp`
lists, `ctxloom mcp --help` teaches. The ladder is about what a bare invocation
does; it is never an exception to help.

### Why this split

The two audiences want opposite things from the same keystrokes, and they can
both be served because they type different things.

Someone who knows the tool types a noun fifty times a day and already knows what
it holds. Making them append ` ls` charges three keystrokes, every time, for
information they were always going to ask for. The bare noun should just answer.

Someone meeting the tool for the first time needs to be taught, and needs it
everywhere, without knowing in advance which commands are generous. That is
exactly what a universal suffix provides: one spelling, guessable from anywhere,
identical at every level. Its value comes from being unconditional. A help
affordance present on most commands is one the newcomer still has to test for,
so "always" is the load-bearing word — not a quality bar, but the property that
makes the rest of the design safe.

That is what makes bare-listing affordable. The teaching surface is not lost
when the bare form stops printing help, because teaching moved somewhere
explicit and permanent.

## 1. Help teaches; it does not merely inventory

A `--help` screen that lists flags alphabetically documents the parser, not the
tool. Good help answers the question the user arrived with, which is almost
always "how do I do the thing?" A worked example answers it fastest.

- `gh pr create --help` leads with runnable examples before the flag table.
- `git rebase --help` walks scenarios, not just options.
- A flag whose value has its own syntax (a query language, a selector, a format
  string) must carry inline examples of that syntax. `kubectl` explains
  `-l 'app=nginx,tier!=frontend'` right there in the help, so the user never
  needs the source or a web search to write their first selector.

**Check:** can a first-time user get from `tool --help` to a successful
non-trivial invocation without reading anything else? If a flag takes a
mini-language, does its help show at least three worked expressions?

## 2. Progressive disclosure: simple things simple, complex things possible

The 90% path should need zero flags. Power arrives in layers. A bare command
does the obvious thing, flags refine it, and an advanced sub-language (queries,
selectors, format templates) is there for the people who need it. Don't charge
beginners attention-tax for expert features, and don't hide the expert features
so thoroughly that nobody finds them (see §5).

- `git log` works bare; `--graph --oneline --all` refines it; `--pretty=format:`
  is the expert layer.
- `docker ps` works bare; `--filter status=exited` refines it.

**Check:** what does the bare command with no arguments do? If the answer is
"error" or "nothing useful," the defaults are wrong.

### A bare leaf and a bare namespace are different questions

The check above is about leaves. `git log` and `docker ps` *do* something, so
bare means do the obvious thing.

A namespace is a container, and the question is what a container should say when
you name it and nothing else. ctxloom's answer is in the decisions above: it
lists. The reasoning is that the most-typed spelling should answer the most
common question, and someone who types a noun on its own almost always wants to
know what they have.

The field is genuinely split here, so it is worth knowing what you are choosing
between rather than assuming a consensus exists. Of fifteen production CLIs
surveyed, twelve have a bare-noun case at all; `cargo`, `npm` and `kubectl` have
no noun namespaces to invoke bare. Of those twelve, three list (git's
noun-commands, `systemctl`, heroku by doctrine), five print help (`gh`,
`docker`, `flyctl`, `wrangler`, `stripe`), and four error (`gcloud`, `az`,
`aws`, `terraform`). The published guidance splits the same way: Heroku's style
guide mandates bare-lists, while 12-Factor CLI Apps, written by the Heroku CLI's
own author, argues against them.

Erroring is the one option nothing here recommends. It spends the user's
keystroke to tell them they wasted it, which is the worst available answer to a
question the tool could simply have answered.

Whichever of the two useful answers you pick, the constraint is the same: the
one you don't give the bare form has to live somewhere unconditional. Listing
without a universal help suffix strands the newcomer; help without a short
listing path taxes the expert forever.

## 3. Sensible defaults — and defaults that hide data must say so

Defaults encode the tool's opinion about the common case, and a *view* default
(show active, hide finished; show local, hide remote) is usually the right
opinion. A default that silently subtracts from an answer is a trap: the user
asked a question, got a partial answer, and wasn't told.

Filtering defaults may hide. They may not hide silently when the user is
searching. If an explicit query — a search term, a tag or label filter — matches
items the view default then suppresses, say so with a count and the flag that
reveals them:

    12 results shown; 30 more match but are completed — add --all to include them

git models this well. `git status` hints at every hidden state transition ("use
git add ...", "use git restore ..."). `git branch` shows local branches by
default, and `-a` is famous precisely because the default output implies its own
incompleteness. taskwarrior prints `(2 tasks hidden by filter)` footnotes. The
anti-pattern is an empty result sitting on top of data that exists.

**Check:** construct a query that matches only items the default view hides.
Does the tool print `(nothing)`, or does it tell you what it hid and how to see
it?

## 4. Consistent grammar: one way to say each thing, everywhere

Verbs, nouns, and flag names form a vocabulary, and every inconsistency in it is
one more fact the user has to memorize. Within a tool, and across a family of
tools, the same concept takes the same word and the same flag.

- If `--json` selects machine output in one command, no sibling may spell it
  `--format json` or `-o json`.
- If plural nouns list (`tool tags`, `tool statuses`), that holds for every
  listable concept.
- Repeatable flags, value separators, and case-sensitivity rules match across
  commands.
- Argument order follows a stable convention (`tool verb <id> [modifiers]`).

kubectl's `get/describe/delete <kind>` grid is the exemplar: learn the grammar
once and guess the rest correctly. Guessability is what consistency is for.

**Check:** take a flag from one subcommand and predict its name and behavior on
a sibling. Were you right?

## 5. Discoverability: every feature reachable from help alone

A feature that ships but can't be found from the help tree may as well not
exist. Help here means every spelling that reaches it — the `help` suffix,
`--help`, and `-h` — since a user who can only find one of them still has to
find the feature through it. That creates two obligations.

1. **The help tree advertises capabilities.** Root help and the relevant
   subcommand help mention each capability at least once. If tasks can carry
   tags, `list --help` shows how to filter by them.
2. **The tool can enumerate its own vocabulary.** Any closed or user-grown set
   the user has to supply values from — statuses, tags, remotes, contexts —
   needs a read command that lists what exists, the way `git remote`, `git tag`,
   `docker context ls` and `kubectl get namespaces` do. Write-only vocabularies,
   where you can attach a label but never list the labels in use, push users
   into keeping notes outside the tool, and typo'd values pile up unseen.

**Check:** for every value-set a flag accepts, is there a command that lists the
current values? Grep the help tree for each major feature: does it appear?

## 6. Errors are teachers: what failed, why, and the next command

An error message arrives with the user's fullest attention, so it should be
worth reading. The template is what was wrong, why, and the concrete next step:
a flag, a corrected invocation, or a help page.

- git prints `did you mean 'git status'?`, and push failures print the exact
  `git push --set-upstream ...` to run next.
- A parse error in a query language points back at the syntax help, ideally with
  a valid example. The user is one line away from success.
- Validation errors name the flag to use rather than the invariant violated:
  "pass --add <tag> and/or --remove <tag>", not "at least one tag required".

**Check:** trigger each error path deliberately. Does every message contain an
actionable next step, in the tool's own flag vocabulary?

## 7. Streams and exit codes are contracts

- **stdout is data; stderr is commentary.** Results go to stdout. Hints,
  warnings, progress and provenance notes go to stderr, so pipes stay clean. A
  hint from §3 must never corrupt `tool list --json | jq`.
- **Exit codes are truthful.** 0 means the operation succeeded *and delivered
  its effect*. A run that succeeded at doing nothing the user asked for is not a
  success. Never print an error and exit 0.
- **Output is quiet by default, informative on demand.** No banner, no
  self-congratulation, one line per fact.

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

The ladder is categorical, not ordered by severity. Read it as a set of distinct
answers to "what happened?", never as a scale. A startup abort (`3`) is a far
more serious outcome than a mistyped flag (`1`), and code that treats a higher
number as a worse outcome has misread this vocabulary.

`2` exists because both neighbouring codes lie about a refusal. `1` reports a
deliberate decision as a fault, sending the user to hunt for a problem on their
machine that isn't there. `0` makes a refusal indistinguishable, to the script
that ran it, from a run that had nothing to do — the same silence the bullets
above forbid. The one command using it: `ctxloom remote upgrade`, declining to
advance a pin onto content whose publisher signature does not verify over its
bytes (`exitCodeRefused` in `internal/cli/startup_helpers.go`, beside
`exitCodeFatalFindings`).

A refused command still prints its full explanation on the way out. The code is
for the script, the message is for the human. Nothing prints an `Error:` line on
top of it, because a refusal is not an error report.

**Check:** pipe every read command through a consumer; does anything non-data
leak into stdout? Grep for error paths that forget a nonzero exit. For any
command that can decline part of what it was asked: does it exit `2`, and does a
test assert the code rather than only the message?

## 8. Human-first output with a machine escape hatch

Default output is a table or line format designed for eyes: aligned, scannable,
and free of decoration that survives a copy-paste badly. Every read command also
offers a stable machine form (`--json`) whose schema is the compatibility
surface. Never make humans parse JSON, and never make scripts parse the human
table.

**Check:** does every listing command have `--json`? Is the human output
self-describing — a count like `3 active`, not a bare `3` whose meaning the
reader has to guess?

## 9. Tags, labels, and filters: the full loop or nothing

Tagging systems depend on a closed loop of four verbs, and the better task and
label CLIs (taskwarrior, todoist, gh label, docker and kubectl labels) ship all
four.

1. **Attach and detach**, symmetric, on one surface.
2. **Enumerate**: list the tags in use, with counts (§5.2). The counts matter.
   They show which tags are alive and expose typo-twins, like `release` at 40
   next to `realease` at 1.
3. **Filter**: query items by tag, composable with the tool's other filters.
4. **Display**: tags visible on the item in normal listings, so the vocabulary
   reinforces itself without anyone having to look it up.

If filtering has its own expression syntax (boolean combinators, selectors),
that syntax is a feature inside a feature and needs its own teaching per §1:
inline examples covering AND, OR, NOT, and one composed expression. Unusual
syntaxes such as postfix or label selectors are fine, but only with
proportionally better inline documentation. Novelty gets paid for in help text.

**Check:** walk the loop. Attach a tag, list tags, filter by it, see it in
output, detach it. Any missing verb breaks the loop.

## 10. Never break the installed base casually

Renaming commands, repurposing flags, or flipping a long-standing default is a
different class of change from everything above, because it invalidates muscle
memory, scripts, and documentation at once. Additive fixes are always safe: a
new subcommand, a hint on stderr, richer help, a new flag. Subtractive or
semantic changes need a deprecation story and an explicit decision. They are
never routine polish.

**Check:** for each proposed change, would an existing script or habit behave
differently? If yes, it needs sign-off, not a patch.

---

## Audit checklist

- [ ] First-run test: `--help` alone gets a new user to a successful real invocation.
- [ ] Every mini-language flag (queries, selectors, formats) has ≥3 inline worked examples.
- [ ] Bare leaf acts; bare noun lists what it contains; neither errors.
- [ ] `help` works as a suffix at every level, alongside `--help` and `-h`.
- [ ] No default silently hides items an explicit query matched — hidden-match counts + revealing flag are printed.
- [ ] Flag names, output shapes, and argument order are guessable across sibling commands.
- [ ] Every value-vocabulary a flag accepts has an enumeration command.
- [ ] Every error message names the concrete next step in flag vocabulary.
- [ ] stdout carries only data; hints/warnings/provenance go to stderr; exit codes truthful.
- [ ] Every listing has `--json`; human output is self-describing.
- [ ] Tag/label loop closed: attach, enumerate (with counts), filter, display.
- [ ] No command renames / default flips without an explicit compatibility decision.
