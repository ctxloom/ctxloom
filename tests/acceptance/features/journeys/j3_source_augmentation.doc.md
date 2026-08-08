# Setup that adds up instead of fighting

Your company has a way it wants projects configured. You have a way you like to
work. The tool you installed last week has setup steps of its own.

The usual outcome is that one of these wins and the others are silently
discarded — whichever source the tool happens to look at first. You get the
company's onboarding and lose your own defaults, or you keep your defaults and
quietly skip the step your security team added.

ctxloom composes them instead. When you run setup, the interview prompt is
ctxloom's own built-in guidance **plus** every trusted source's onboarding
**plus** every installed companion's setup steps. Nothing replaces anything.

## Where a contribution can come from

Two places, and they arrive by the same road:

- **A trusted repository** — your company's, your own, a third party's. A
  repository contributes by shipping an `agent-setup` command; if you trust the
  source, its steps join the interview.
- **An installed companion** — a tool that lives alongside ctxloom. A companion
  advertises what it ships, and an `agent-setup` command in that set is picked
  up exactly like a repository's.

There is no separate mechanism for companions and no extra command to run. Both
are read by the same pass, which is why adding a source is never a special case.

## What "composes" means in practice

Say your company's repository contributes an onboarding step, your personal
repository contributes your preferences, and you have a companion installed
that needs a setup step of its own. The assistant conducting your setup
interview receives all four things — ctxloom's built-in guidance and all three
contributions — in a stable order.

Trust is the gate. A repository's steps reach your setup interview when you
trust that repository, which is the same decision that governs everything else
it ships. An untrusted source contributes nothing, silently to the interview and
loudly where trust decisions are reported.

## Why this is worth caring about

The failure this prevents is not dramatic, which is exactly why it is worth
preventing: setup completes, reports success, and simply lacks the step someone
added on purpose. Nobody sees an error. The step is just not there, and it stays
not-there until somebody notices its absence months later.

An organization gets consistent onboarding without overwriting the way its
developers work, and a developer picks up a company standard without giving up
their own baseline.
