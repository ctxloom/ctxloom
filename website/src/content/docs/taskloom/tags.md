---
title: "Tags"
---

Your active list hits forty open tasks and stops being a list you read — it's a list you
scroll past. Somewhere in it is the one thing that's actually urgent, next to a dozen things
that aren't, and nothing on the page tells them apart except the words in the task text, which
means finding the urgent one means reading all forty again. A flat list scales to "a handful of
things," and most real backlogs are past that by week two.

Tags are how you carve that list back into something you can query instead of scan. Tag a task
`urgent` and `taskloom list --tag-query urgent` shows just that one, no matter how many others
pile up around it. Tag it `blocked` when it's stuck and it drops out of `--tag-query
"blocked/not"` without you having to remember which ones those were. An agent filing a
just-noticed follow-up can tag it on the way in (`task_add`'s `tags` parameter), so it's already
sorted before you ever see the list — and a tag it later needs to retract (a fix that turned out
not to be needed) comes off with `task_tag` the same way it went on, no separate "undo" concept
to learn.

## What a tag is

A tag is `(namespace:)key(=value)` — the namespace and the value are both optional. `urgent` is
a complete, valid tag: no namespace, no value, just a marker. `triage:kind=defect` is the fuller
shape: namespace `triage`, key `kind`, value `defect`. Both forms coexist on the same task and
are queried the same way; a plain word is just a tag with an empty namespace, not a different
kind of thing.

```
taskloom tag swift-amber-falcon --add urgent --add release
taskloom tag swift-amber-falcon --add triage:kind=defect
```

`--add` and `--remove` are both repeatable and can be combined in one call; `--add` is applied
before `--remove`, so naming the same tag in both takes it off. The MCP twin is `task_tag`, with
the same `add`/`remove` shape, for an agent doing the same thing without a shell.

## Filtering: `tag_query` is postfix, not a search string

`taskloom list --tag-query` (and `task_list`'s `tag_query` parameter) doesn't take a boolean
expression the way you'd type one — it takes a **postfix (RPN)** one: tags first, operator
after.

```
taskloom list --tag-query "urgent/release/and"    # tagged urgent AND release
taskloom list --tag-query "urgent/release"         # the same — a bare list is an implicit AND
taskloom list --tag-query "urgent/release/or"      # tagged urgent OR release
taskloom list --tag-query "urgent/not"             # NOT tagged urgent
```

Verified against a real `taskloom` build (see the four scenarios below, run against a scratch
project, `taskloom` v0.7.0):

```
$ taskloom list --tag-query "urgent/release/and"
[ ] frail-chop  To Do  Ship the release  [release, urgent]

$ taskloom list --tag-query "urgent/release/or"
[ ] frail-chop   To Do  Ship the release  [release, urgent]
[ ] overt-pasta  To Do  Fix flaky test    [urgent]
[ ] obese-coach  To Do  Write changelog   [release]

$ taskloom list --tag-query "urgent/not"
[ ] obese-coach  To Do  Write changelog   [release]
[ ] hilly-crane  To Do  Someday cleanup
```

The reason it's postfix rather than infix: a postfix grammar never needs parentheses or operator
precedence rules to stay unambiguous, which matters more than it sounds like it should once a
query has more than one operator — `urgent/release/and/blocked/or` ("(urgent AND release) OR
blocked") has exactly one reading, with nothing to get backwards. The cost is that a malformed
query — an operator with nothing left on its stack to apply to — fails loud instead of guessing:

```
$ taskloom list --tag-query "and"
Error: list tasks: tag query "and": tagma: postfix stack underflow at "and"
```

A single bare key with no operator tests presence, valued or not. Value comparisons use `=`
`!=` `>` `>=` `<` `<=` (numeric, or lexicographic for `type=semver` targets — see below) or `~`
(pattern match, `.` matching any single character, anchored to the whole value). `*` and `+` are
wildcards over the namespace half: `*:key` matches `key` in any namespace, including none;
`+:key` matches `key` in any *named* namespace only; `ns:*` matches any key under `ns`. A bare
atom with no namespace (`urgent`) matches *only* tags that also have no namespace — it does not
match `triage:kind=urgent`, even though the word is the same; a namespaced key always needs
`ns:key` to reach it.

## When a tag can carry only one value: `arity=scalar`

Some tags are naturally exclusive — a task has exactly one `triage:kind`, not several. Declare
that in `tag_schema` and re-tagging with a different value doesn't add a second tag next to the
first; it silently replaces it. Verified live:

```
$ taskloom tag hilly-crane --add "triage:kind=defect"
$ taskloom show hilly-crane
    tags: triage:kind=defect

$ taskloom tag hilly-crane --add "triage:kind=chore"
$ taskloom show hilly-crane
    tags: triage:kind=chore
```

The second `--add` didn't accumulate — `defect` is gone, replaced by `chore`, with no `--remove`
in sight. This is the one sharp edge in the whole tag system worth remembering: for a
scalar-declared target, "add a different value" *is* "replace the value," not "add another
tag." (Adding the exact same value again is a genuine no-op, not a re-write.) The log itself
stays append-only underneath this — the fold that produces `taskloom show`'s answer records
both the retracting untag and the new tag as separate events, so nothing is destructively
edited on disk, but the *view* you see collapses to one current value, which is the entire
point of declaring scalar in the first place: `triage:kind` answering "defect and chore, at
once" would be a task that doesn't know what it is.

`taskloom`'s own built-in default schema already declares this for its triage vocabulary, with
no `.taskloom/config.yaml` required — `triage:kind` (defect/capability/chore, scalar, enum-
checked), `triage:effort` (a 0–5 range, scalar), and `triage:blocks-release` (scalar, and
compared as a real SemVer rather than lexicographically, so `<=0.7.0` orders the way a human
expects). A project can declare its own targets the same way to get the same guarantees for its
own vocabulary.

## Guardrails that reject bad tags outright, at write time

A value outside a declared enum or numeric range is rejected, not silently accepted and
ignored by the query engine later:

```
$ taskloom tag hilly-crane --add "triage:kind=bogus"
Error: add tags: tag "triage:kind=bogus"'s value "bogus" is not one of triage:kind's declared enum values [defect capability chore]
```

Two more things can never be written as a tag, on any project: the query grammar's own operator
words (`and`, `or`, `not`, matched case-insensitively — a tag named `or` would parse as an
operator and become permanently unqueryable), and anything under the `tagma.` namespace, which
is reserved for the schema declarations themselves.

```
$ taskloom tag hilly-crane --add "or"
Error: add tags: tag "or" is a reserved operator word in tagma's query grammar (and/or/not, matched case-insensitively) — it would always parse as an operator, never as a matchable tag, and so would be permanently unqueryable except by quoting it at query time
```

## Ranking a pile of tags into one order: `--sort priority`

Once tasks carry structured tags, `taskloom list --sort priority` (and `task_list`'s
`sort="priority"`) folds them into one derived, 0–5, rank-normalized score per task — not a
number you assign by hand, but one computed from `tag_schema`'s declared `priority_fn` /
`decay_fn` formulas over each task's actual tag values, plus a couple of taskloom-provided
built-ins like `age_days`. Every returned task carries its own `derived_priority` so you can see
the number the sort used, not just trust the order. taskloom's built-in default derives priority
from a handful of independently-checkable factual flags — `triage:crashes`,
`triage:data-loss`, `triage:no-workaround`, `triage:security=<cwe>`, `triage:regression`,
`triage:exploited-in-wild` — rather than a hand-picked severity label, on the theory that "does
it crash" is something two people agree on and "how severe is this, 1–5" usually isn't.

## Discovering what's already in use before coining something new

`taskloom tags` lists every tag currently in use, with active/total counts per tag — the fastest
way to notice that `blockd` (typo, count 1) and `blocked` (the real tag, count 12) both exist
before you add a third spelling. The MCP twin is the `taskloom://tag-vocabulary` resource. For
the schema itself — which targets are declared scalar, their enums, their ranges, their formulas
— read `taskloom://tag-schema`, or the `tag_schema` field in the [config
reference](/taskloom/reference/config/).

## Reference

- **[`taskloom tag`](/taskloom/reference/cli/taskloom_tag/)** / **[`taskloom
  tags`](/taskloom/reference/cli/taskloom_tags/)** — CLI commands, generated from the binary.
- **[`taskloom list --tag-query`](/taskloom/reference/cli/taskloom_list/)** — the full query
  grammar, in `--help` form.
- **[MCP tools reference](/taskloom/reference/mcp-tools/)** — `task_tag`, `task_list`'s
  `tag_query` and `sort` parameters, and the `tag-schema` / `tag-vocabulary` resources, generated
  from the tool registrations.
- **[Configuration](/taskloom/reference/config/)** — `tag_schema`'s full declaration grammar
  (`arity`, `enum`, `range`, `priority_fn`, `decay_fn`, `hide`, `type`).
