---
description: You are writing a check that can find something wrong; deciding what a command should do when its input is broken, missing, or untrusted; or choosing between refusing and carrying on with less. For anyone deciding how loudly a failure should surface.
tags:
  - ctxloom
  - workflow
  - trust
---
# Refuse when something is amiss; `--degraded` is the way through

**The default posture is REFUSE, not proceed.** When ctxloom finds something
wrong at startup — broken config, an unresolvable profile or bundle, a failed
hook apply, an invalid document — it ABORTS the launch and says what is wrong
and how to fix it. It does not quietly launch something lesser.

This is a deliberate pivot away from always-launch. Always-launch produced
CONFIDENT WRONG WORK: a bad or missing agent name silently degraded to
`host`+`none`, discarding the runtime and permissions that were asked for, with
only a stderr warning nobody read. The agent then ran unisolated while everyone
believed otherwise. A launch that succeeds without doing the thing is worse
than one that refuses, because nothing downstream can tell the difference.

## `--degraded` always reaches a working LLM

`--degraded` (and `CTXLOOM_DEGRADED=1`; the flag wins) lowers every finding
from an error to a WARNING and continues. That is a promise, not a
best-effort: **degraded mode always gets the user into a working LLM.** If a
degraded run can still refuse to launch for an ordinary finding, degraded mode
is broken.

So the two modes divide cleanly:

    default      refuse, and explain the remedy
    --degraded   warn, and launch anyway

## The one exception: launching is itself the danger

Security findings where starting the LLM is the harm do NOT yield to
`--degraded`. A breached trust or isolation boundary — credentials reachable
that should not be, an ownership mismatch on the runtime axis, unverifiable
content admitted to an execution surface — stays fatal in both modes.

The test is not "is this serious?" but **"does launching cause the harm?"** A
profile that fails to parse is serious and still degradable: the user gets a
working LLM with less context. A container runtime that cannot provide the
isolation it claimed is not degradable, because the launch IS the exposure.
`--degraded` falls back to the HOST, never to a different ownership mode.

## How to write one of these, mechanically

Report the finding; let `strictness` decide its fatality:

    strictness.FailOnce(strictness.ClassConfig, "<the remedy>", ...)

`internal/profiles/profiles.go` already does exactly this for the
empty-profile cases. `strictness` owns the fatal-vs-warn decision centrally and
`SetDegraded` flips it process-wide, which is what makes the guarantee above
hold everywhere at once.

Therefore, at the site of a check:

- **Do NOT branch on degraded state.** No `if strictness.Degraded()`, no
  `strict bool` parameter, no per-call fatality argument. A check that decides
  its own fatality is a second policy that will disagree with the first.
- **Do state the REMEDY, not just the complaint.** The refusal is the entire
  user interface for the failure. "invalid profile" is a dead end; "give the
  profile something to select (parents, bundles, fragments, select_tags) or
  delete it" is error correction. Write the sentence that ends the problem.
- **Do make sure the remedy is followable.** A message advising an action the
  caller cannot take — telling them to drop a flag they never passed — is
  proof the check is firing outside its own design premise. Fix the premise,
  not the wording.
- **Report every problem, not the first.** `FailOnce` records and the startup
  gate decides; a user fixing four things one launch at a time is paying for
  our convenience.

## Testing it

Pin BOTH arms, and mutate each. The same bad input must REFUSE by default
(nonzero exit, nothing launched) and WARN-then-launch under `--degraded`. A
test covering only the refusal lets degraded mode silently start refusing too,
which breaks the guarantee for every existing user; a test covering only the
degraded arm lets the refusal rot into a no-op. Assert the EFFECT — exit status
and whether an engine actually started — never the message text alone.
