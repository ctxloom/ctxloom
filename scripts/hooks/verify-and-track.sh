#!/usr/bin/env bash
# Stop hook: before a turn that CHANGED something is allowed to end, hand the
# agent back the project's close-out contract — verify, prove the tests can
# fail, then update the work tracking.
#
# Two guards decide whether this speaks at all, and both are load-bearing:
#
#   1. stop_hook_active. Claude Code sets it when the turn is already resuming
#      because a Stop hook blocked. Blocking again is how a Stop hook becomes
#      an infinite loop, so this exits first and unconditionally.
#   2. a turn that changed nothing. A turn that answered a question has
#      nothing to verify; nagging it burns tokens and trains the reader to
#      skim past the checklist on the turns where it matters.
#
# Exits 0 silently when either guard says stay quiet.

set -uo pipefail

input=$(cat)

if [ "$(jq -r '.stop_hook_active // false' <<<"$input" 2>/dev/null)" = "true" ]; then
  exit 0
fi

# Did THIS TURN change anything? -> `ctxloom hook turn-changed`, which reads
# the session transcript named by the payload and answers "changed" or
# "unchanged".
#
# The question used to be "is this checkout dirty", and that is the wrong
# proxy. It goes silent on precisely the sessions carrying the most
# close-out debt: a coordinator dispatches every edit into a separate git
# worktree, so its own tree stays clean-except-excluded for a whole night of
# work while it closes, cuts and files dozens of tasks. The turn is the unit
# of work, not the checkout the hook happens to run in — and measured on the
# turn, a subagent editing another worktree counts, while a conversational
# turn still does not.
#
# Only the exact word "unchanged" silences the contract. A missing binary, a
# crash, an unreadable transcript or any future word all leave it firing:
# silence is the failure this guard exists to prevent, and a spurious
# checklist is much the cheaper error.
if [ "$(printf '%s' "$input" | ctxloom hook turn-changed 2>/dev/null)" = "unchanged" ]; then
  exit 0
fi

read -r -d '' reason <<'EOF' || true
This turn changed files. Before ending it, close out the work — and report
honestly, including "not done" where that is the truth.

VERIFY THE CHANGE
- Build from THIS tree and drive that binary: `just build`, then `./ctxloom`.
  Never `ctxloom` from $PATH — it is deliberately stale and anything it says
  about this branch is a coincidence.
- Gate on the EXIT CODE, not on grepping output for "PASS" or "ok".
- `just test` only COMPILES the integration suite. A real gate is
  `just test-acceptance` (and `just test-integration` when that layer moved).
  Run the narrow package target while iterating; run the real gate before you
  claim the work is done.

MUTATION TESTING IS THE QUALITY GATE — scoped to what THIS TURN created
- The gate is not "the tests pass". It is: every test this turn wrote or
  changed has had a mutation KILLED against it. A new test with no killed
  mutation does not count as coverage and the work is not done.
- SCOPE IT TO YOUR OWN WORK. Mutate only the production code your new or
  changed tests NAME — not the package, not a sweep. A whole-package run is
  slow, mostly re-measures code you did not touch, and buries the one result
  that matters. If you changed nothing under test, there is nothing to mutate;
  say so rather than inventing a run.
- For each: break that code — in internal/ or cmd/, never in the test —
  confirm RED, revert, confirm green again.
- If nothing you can break makes a test fail, it is a tautology and worse than
  no test, because it reports coverage that does not exist. Say so and fix it.
- REPORT EVERY MUTATION: what you broke, which test died, and any that
  SURVIVED. A survivor is the most valuable result you can get — it names an
  untested claim you just shipped. Do not quietly drop it.

  Which mutation method applies:
- UNIT-testable code you changed -> `just test-mutation-diff <BASE>` scopes
  gremlins to changed files, which is exactly this rule. `just
  test-mutation-pkg <PKG>` exists but sweeps a whole package — use it only
  when the diff-scoped run cannot see your change.
- Anything driven through the CLI as a SUBPROCESS (the acceptance suite, and
  the integration tests using testenv) -> gremlins CANNOT see it. It is
  coverage-gated, and Go's -coverprofile does not cross a subprocess boundary,
  so those mutants report NOT COVERED and are never even attempted. Measured:
  16/16 acceptance scenarios exercised trust.go while its coverage read zero.
  There, HAND mutation is the only real method, not a poor substitute.
- HAND MUTATION MUST REBUILD. Integration and acceptance tests exec a
  PREVIOUSLY BUILT ./ctxloom, and `just test-pkg` does not rebuild. Edit
  production code, run `just build`, THEN the gate. Skip the build and the
  mutation silently survives against the old binary and you will conclude the
  test is vacuous when it is fine — or worse, that it is fine when it is
  vacuous.

- Watch for this project's characteristic shape: exit 0, a success message,
  and zero bytes written. Assert the EFFECT, never the command's own report of
  its effect.

UPDATE THE DOCUMENTATION AND COMMENTS YOU INVALIDATED
- Stale documentation is worse than none. A comment or doc is what the next
  reader trusts INSTEAD of re-deriving the thing, so a wrong one actively
  misleads where silence would at least have made them look.
- Nothing tests a comment. No gate will ever catch this for you, which is why
  it is on this checklist and not in CI.
- Sweep what your change falsified: doc comments on the symbols you touched,
  the package doc, the `docs/**` pages describing that behaviour, help text,
  and generated examples.
- A rule with two descriptions has one that is wrong and no way to tell which.
  If you moved a rule, formula, default or threshold, grep for its OLD
  spelling and fix EVERY description of it — not just the one beside your
  edit. The same goes for a helper someone hand-copied: delete the copy.
- Cite by SYMBOL, not file:line. A stale symbol reference fails loudly the
  moment someone greps for it; a stale line number silently points at
  unrelated code and is believed.
- State the invariant, not the history. No dates, no commit SHAs, no incident
  counts, no session names — those belong in the task log.
- Measured, and the reason this section exists: one change to how a trust key
  is composed left FOUR hand-written copies of the retired rule and THREE
  comments asserting it as current behaviour. One of those named that formula
  as the ENTIRE defense against a rejection-escape attack — so an auditor
  checking that defense would have verified a rule the code had stopped using
  and concluded it held. Every copy was found only because a gate went red.
  The comments would never have gone red at all.

UPDATE THE WORK TRACKING
- taskloom: re-scan for open tasks covering what you just touched
  (`taskloom list --term <symbol|path>`). Close what this work satisfied,
  quoting what the task asked for and what actually landed. Where it satisfied
  a task only in PART, edit the task down to what REMAINS — never leave the
  finished half in it.
- File anything deferred, skipped, or discovered-and-not-fixed as its own
  task, with enough context to act on cold. A workaround is not done until the
  root cause is filed and the human is told.
- Update the relevant `*.plan.md` and any other tracking the turn invalidated.
- If a task or plan turned out to be STALE, correct it rather than working
  around it — a stale task gets the same work done twice.
- RE-TAG WHAT YOU MOVED. If you renamed, split or deleted a file, fix the
  `touches:` tags naming it; if you changed or removed a symbol's contract, fix
  the `sig:` tags naming it. Find them with
  `taskloom list --term <old path or symbol> --json`. These tags are what tells
  the next dispatch which tasks collide, so a stale one either hides a real
  collision or invents one — and nothing else in this checklist will catch it,
  because a tag has no compiler and no gate. See docs/task-tagging-standard.md.

FIX WHAT YOU FOUND — filing is the exception, not the default
- A defect, stale task, red gate, surviving mutation or untested claim you
  turned up along the way is now YOURS, and the default disposition is FIX IT
  THIS TURN. "Noted", "worth a look", "pre-existing", "not mine", "from
  another commit" are not dispositions — the code is collectively owned, and
  provenance is context, never an excuse.
- You may defer to a task ONLY for one of two reasons, and you must say WHICH:
    1. IT NEEDS A HUMAN DECISION. Not "it is debatable" — a real fork whose
       answer changes what gets built, that you should not pick alone: a
       threshold or ratchet value, a security posture, a schema or on-disk
       format, adding or dropping a dependency, anything user-visible.
       Tag the task `human` so these are findable as one queue, and state the
       question in the task rather than only the problem.
    2. IT IS A GENUINELY LARGE LOAD. Hours of work, a migration, new
       infrastructure, or it needs an environment you do not have (a live
       daemon, credentials). Say what makes it large.
- Everything else gets fixed now. "It is adjacent but not my current task" is
  NOT a reason — a red lint gate three `gofmt -w` away, a one-line silent
  no-op, a stale task whose premise you just disproved: fix them.
- Filing several small things in one turn and fixing none of them is the
  failure this rule exists to stop. Accumulating a backlog reads as progress
  and is not.
- If you do defer, the task must be actionable COLD: what is broken, how it
  was found, how to reproduce, and — for a `human` one — the actual question.
  A finding that lives only in this turn's prose is lost.

Do not declare the work done while a found item is still open and unnamed.
If you have already done all of this, say so briefly and finish.
EOF

jq -n --arg r "$reason" '{decision: "block", reason: $r}'
