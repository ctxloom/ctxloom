---
name: design-by-test
description: Decide an architectural question by writing the test FIRST, because the test is where the interface gets proposed while it is still cheap to argue with. Use when planning any change that introduces or alters a signature, a boundary, a contract, or a data path — before writing the implementation, and before dispatching an implementer. Not a testing practice; a design one.
metadata:
  type: workflow
---

# Decide it in a test, before the implementation decides it for you

Write the failing test FIRST. Not for coverage — coverage is what `closeout`
verifies afterwards, and a killed mutation there is stronger evidence than test
ordering ever was. Write it first because **the test is where the design
decision is made, and writing it first is what makes that decision reviewable
while it is still cheap to change.**

## Why this is a PLANNING skill and not a testing one

A signature written inside an implementation is a fait accompli. By the time
anyone reads it, the callers exist, the tests are shaped around it, and the
question "should it look like this?" costs a refactor to ask.

The same signature written in a test, before the implementation, is a
PROPOSAL. It costs one edit to change. That is the entire difference, and it
is why `closeout` cannot cover this: closeout audits what was decided. This
decides.

    func TestStartOwnedRun_RefusesWhileDraining(t *testing.T) {
        c := newCoordinator(t)
        c.BeginDrain()                       // <- proposes: one-way, no args
        _, err := c.StartOwnedRun(ctx, id)   // <- proposes: refusal is an error,
        require.ErrorIs(t, err, ErrDraining) //    not a bool, not a panic
    }

Four design decisions are visible there before a line of production code
exists: drain is a coordinator method not a package function; it takes no
deadline; refusal surfaces as a typed error; and the error is comparable with
`errors.Is`. Each is arguable in ten seconds at this stage, and expensive to
revisit once `StartOwnedRun` is written.

## The rule

1. **Write the test that names the behaviour.** Compile it. Watch it FAIL.
2. **COMMIT IT RED, on its own.** This is not ceremony — it is what puts the
   proposal in history where a reviewer, or you tomorrow, can see what was
   decided and when. A design that only ever appears already-implemented was
   never reviewed, it was announced.
3. Then implement until it passes.

The red commit is the artifact. Without it the claim "this was designed" is
unverifiable, which is the same failure mode as an unchecked comment.

## The payoff that is actually about architecture

**If you cannot write the test, you have found an open question — RAISE IT, do
not guess.**

That is the whole reason this skill exists. An unwritable test is a design hole
made visible at the cheapest possible moment. Concretely, if you find yourself
unable to proceed because you do not know:

- what the function should be CALLED, or which type owns it — the boundary is
  unsettled;
- what it should RETURN when the interesting case happens — the contract is
  unsettled;
- what to construct to reach the code at all — the dependencies are wrong, and
  the test is telling you so before the implementation buries it;
- whether the caller is even allowed to be in that state — you have found a
  missing invariant;

then STOP and put the question to whoever owns it. Do not resolve it by writing
an implementation and letting the shape fall out. An implementation always
produces AN answer; that is precisely why it is the wrong instrument for
choosing one.

## What this does NOT ask of you

- Not coverage-driven development. Do not write a test per function.
- Not every change. A deletion, a comment fix, a rename with no new surface:
  skip it. This applies where a SIGNATURE, BOUNDARY, CONTRACT or DATA PATH is
  introduced or altered.
- Not a substitute for mutation. Mutation is POST FACTO by nature and belongs
  in `closeout`, which already requires it. Do not run one here; you have
  nothing to mutate yet.

## Dispatching

If you are handing this to someone else, the ordering requirement goes IN THE
BRIEF, and require the red commit as evidence. An instruction to "write tests"
reliably produces implementation-first work with tests appended — and the
result is indistinguishable at merge unless the history shows the red state.
