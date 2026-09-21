# Setup that adds up instead of fighting

Your company has a way it wants projects configured. You have a way you like to
work. The tool you installed last week has setup steps of its own.

The usual outcome is that one of these wins and the others are silently
discarded — whichever source the tool happens to look at first. You get the
company's onboarding and lose your own defaults, or you keep your defaults and
quietly skip the step your security team added.

ctxloom composes them instead. When you run setup, the interview prompt is
ctxloom's own built-in guidance **plus** every installed companion's setup
guidance. Nothing replaces anything.

## Where a contribution can come from

From a **companion** — a tool that lives alongside ctxloom: the one your
company ships to standardize its projects, the one you installed for yourself,
a first-party tool like reprise. A companion advertises what it ships in its
loadout, and the setup guidance it declares there is a typed field of that
loadout, not a command with a special name that has to be spelled exactly
right to be noticed.

There is no separate mechanism per companion and no extra command to run. Every
companion is read by the same pass, which is why adding one is never a special
case.

## What "composes" means in practice

Say your company's companion contributes an onboarding step, your own tooling's
companion contributes your preferences, and you have a first-party companion
installed that needs a setup step of its own. The assistant conducting your
setup interview receives all four things — ctxloom's built-in guidance and all
three contributions — in a stable order.

Execution is the gate. A companion's guidance reaches your setup interview when
ctxloom is allowed to run that companion — a binary signed by a publisher you
trust — which is the same decision that governs everything else it ships. A
companion nobody vouched for contributes nothing, silently to the interview and
loudly where that refusal is reported.

## Why this is worth caring about

The failure this prevents is not dramatic, which is exactly why it is worth
preventing: setup completes, reports success, and simply lacks the step someone
added on purpose. Nobody sees an error. The step is just not there, and it stays
not-there until somebody notices its absence months later.

An organization gets consistent onboarding without overwriting the way its
developers work, and a developer picks up a company standard without giving up
their own baseline.
