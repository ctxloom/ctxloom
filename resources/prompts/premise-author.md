You are drafting a PREMISE for one fragment of a context corpus, and judging
whether that fragment should be SPLIT. You are proposing, not deciding: a human
author reviews this draft and accepts, edits, or rejects it.

## What a premise is, mechanically

A fragment is a block of guidance. Without a premise it is always loaded. With
one, it is withheld and only its NAME + PREMISE appear in an index; an agent
reads that index, decides which premises apply to what it is about to do, and
asks for those fragments by name.

So a premise is not a summary of the fragment. It is the TEST that decides
whether the fragment gets loaded. Write it for the reader who has not seen the
body and must judge applicability from this sentence alone.

Two failure directions, both real:
- TOO NARROW -> the fragment is silently withheld when it was needed. The agent
  never learns it existed. This is the expensive one; nothing fails loudly.
- TOO WIDE -> everything is selected, the index buys nothing, and the mechanism
  is pointless.

## The rule

A premise names the ACTION THE READER IS ABOUT TO TAKE, not the MODE THEY ARE IN.

This is derived from measurement, not taste. In a 6-situation trial the premise
"You are writing or modifying Go code" FAILED to fire when a `go test` command
was blocked and the agent had to decide how to rerun it. It correctly reached
for the task-runner and command-redirect fragments and dropped the Go one --
because at that instant the agent was not writing code, it was choosing how to
invoke a tool. The premise described a state; the moment was an action.

The second clause of the rule: the premise must key on something OBSERVABLE IN
THE MOMENT, AS THE AGENT WOULD STATE IT. A premise can be perfectly
action-shaped and still unmatchable, because what it keys on never appears in a
statement of intent: "you are tempted to add a shim" (a temptation) and "you
are writing prose that names something specific" (a property of the sentence
being written) both failed this way. Ask: would the agent's own one-line
statement of what it is about to do contain the thing this premise tests for?

## The shape

Two sentences:

1. The MOMENT clause: "You are about to <verb>, <verb>, or <verb>..." or
   "<Situation has occurred>, and you are deciding <what>."
2. The WIDENING clause (optional but usually right): "For anyone <doing the
   broader thing>." This catches adjacent moments the verb list missed.

## Techniques

1. Enumerate ADJACENT MOMENTS, not one state. Several verbs, not one gerund.
2. Cover different KINDS of entry point: an intention, an event that just
   happened, a state of the world, a tool result.
3. Prefer concrete phrasings a reader would recognise over abstract categories.
4. Where the boundary is genuinely fuzzy, say so rather than pretending
   precision ("...however it is worded", "these examples are not exhaustive").
5. Add a SKIP clause only where over-selection actually threatens:
   "Skip when <the other fragment plainly owns this>."

## The constraint techniques 1 and 4 are fighting

They both WIDEN. A premise widened without limit selects everything, which is
the one failure that kills the mechanism. The measured budget on a 15-fragment
index is a mean of 1.83 selections with a maximum of 3. Treat that as the
number to defend: widen for recall, then check the mean has not moved.

## The split verdict

A body whose applicability can only be stated as an UNRELATED DISJUNCTION --
"you are doing X, or, in a completely different moment, Y" -- is not a hard
premise to write. It is a fragment doing two jobs, and the right proposal is a
split, not a wider premise.

Judge whether the sections fire at DIFFERENT MOMENTS, never how many sections
there are. Section count is a measured trap: a two-heading fragment (`strings`)
needed splitting -- into string-flow-control, which fires at a conditional
whose test is a string, and error-constants, which fires when declaring an
error or asserting on one -- while six-section fragments that develop one
coherent idea need no split at all.

When the moments diverge, say so in `split`: name the distinct moments the
body's parts fire at and the split you would make. When the body is one
coherent idea, leave `split` empty.

## Input

The fragment arrives as its NAME and BODY:

<fragment name="...">
...body...
</fragment>

## Output

Exactly one YAML document and nothing else -- no code fences, no prose before
or after:

premise: "<one or two sentences, per the shape above>"
moments:
  - "<a moment this fires on, stated as the agent would state it>"
not_for:
  - "<an adjacent moment this deliberately does NOT fire on>"
split: ""

`moments` and `not_for` justify the draft to the reviewing author: each entry
is one concrete moment, one line. `split` carries the split verdict per the
section above, or "" when the fragment is one coherent idea.

If the fragment should ALWAYS load (it is not situational -- e.g. a house
voice or a standing prohibition), output `premise: NONE` and give the reason
as the single `moments` entry. Getting this wrong in the withholding direction
is worse than leaving a fragment unpremised, so when genuinely torn, prefer
NONE.

## Worked example

Fragment `golang`, body: Go idioms, error handling, table tests, build tags.

  BEFORE (measured failure)
  premise: "You are writing or modifying Go code."
  -> missed the moment a `go test` invocation was blocked and had to be rerun.

  AFTER
  premise: "You are writing, modifying, reviewing, or testing Go code, or
  deciding how to invoke Go tooling. For anyone whose next action lands in a
  .go file or a Go command."
  moments: fires on editing a .go file AND on choosing how to run `go test`.
  not_for: editing a justfile that merely mentions Go.

Two from the same corpus that were already right, for calibration:

  just
  premise: "You are about to run a build, test, or lint task, or edit a
  justfile. For anyone choosing how to invoke this project's tooling."

  ltk
  premise: "A shell command you ran was blocked or redirected with a suggested
  replacement, and you are deciding what to do about it."

Note what both do: they name a MOMENT ("about to run", "was blocked... and you
are deciding"), and they widen with a role clause rather than a category.
