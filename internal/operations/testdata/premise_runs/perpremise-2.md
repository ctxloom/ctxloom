# Per-premise applicability

You are given 9 PREMISES. Each states the condition under which one
piece of guidance applies. You are also given a list of SITUATIONS: statements of
what an agent is about to do.

Consider each premise ON ITS OWN. For that premise, go through every situation and
decide independently: does this premise describe that moment? Other premises are
irrelevant to the judgement -- you are not choosing the best match, you are deciding
whether THIS one applies. Several premises may apply to the same situation; that is
normal and expected.

Output one line per premise, nothing else:

    <premise-name>: <comma-separated situation ids, or NONE>

## Premises (9)

### green-is-not-passing
You have just written or changed a test; you are reading a gate result and deciding what it proves; or you are about to describe something as verified, working, or done. For anyone about to treat a green result as evidence, however that moment arises. More than one fragment may apply here; this one is about whether the evidence is real.

### is-this-a-standard
You are about to build infrastructure -- a protocol, a validator, a health check, an auth flow, a parser, a retry policy. For anyone about to write something a well-known standard, or a library this project already wires in, may already cover.

### isolation-axes
You are creating, configuring, or delegating to an agent; choosing a runtime or a workspace for one; or relying on isolation you did not explicitly specify. For anyone whose next action puts work in someone else's hands.

### ltk
A command you ran was blocked, refused, or redirected with a suggested replacement; or you are about to invoke a build, test, lint, or release tool directly rather than through the project's task runner. For anyone deciding how to run a command here.

### planning-and-brainstorming
You are turning a request into a sequence of work, weighing more than one approach, or deciding what must happen before what. For anyone about to commit to a plan.

### preexisting-ownership
A test, gate, or check is failing and you are deciding whose problem it is, or about to attribute it to something other than your own change. For anyone about to write or think 'pre-existing', 'unrelated', 'flaky', or 'not mine'.

### problem-solving
Something is failing and you are considering a workaround, a retry, a sleep, a threshold change, a disabled check, or a fallback; or you are deciding whether to chase the root cause or patch the symptom. For anyone about to route around a defect rather than fix it.

### prompt-authoring
You are about to write the brief, instructions, or task description a sub-agent will work from, or deciding what a child agent must return. For anyone composing instructions for an agent that will not see this conversation.

### prototype
You are about to preserve an old code path, add a shim, fallback, version check, or deprecation; hesitating to break existing callers; or choosing between a compatible fix and a correct one. For anyone weighing what already exists against doing it properly.

## Situations (59)

S01: I need to understand how an existing piece of this code works before I change anything.
S02: I need to find where a symbol, function, or setting is defined or referenced.
S03: I have made a change and need to confirm it actually produced the effect I intended.
S04: I need to see what branch I am on and whether my changes have landed.
S05: I need to see what this tool currently offers, or read its help before invoking it.
S06: I need to read the configuration or schema to see how something is declared.
S07: I am about to run the full acceptance suite to decide whether this work is done.
S08: I want to run one package's tests to iterate quickly on the change I just made.
S09: I am about to hand a substantial, context-heavy piece of work to a sub-agent.
S10: I am about to stage files and commit what I have done so far.
S11: Something is broken and I am about to change production code to correct it.
S12: I need to reach context or history from a session other than the one in front of me.
S13: I am about to build the binary so I can drive the code I just changed.
S14: I am about to modify source to implement the behaviour we agreed on.
S15: A decision or finding just landed and I need to write it somewhere durable.
S16: I am about to push commits or publish a release to somewhere other people will see.
S17: I am about to merge finished work into the integration branch.
S18: My test is green and I am about to break the code deliberately to see whether it notices.
S19: A gate came back red and I need to work out why before I touch anything.
S20: I am about to run the linter and the architectural gates over what I changed.
S21: I am about to sign content, or decide whether something unsigned can be trusted.
S22: I need to read the existing plan or design before proposing what happens next.
S23: I need a real number for how long something takes or how much it costs.
S24: I am about to delete code that is duplicated, stale, or no longer reachable.
S25: I have something long running and I am deciding what to do while it finishes.
S26: I need to read the output of a run to find out what actually happened.
S27: Before I write something new I want to know whether it already exists here.
S28: Work has merged and I am about to remove its worktree and delete the branch.
S29: There is work I cannot do now and I need it to survive this conversation.
S30: I need to see everything still open across branches and worktrees and decide what to do with it.
S31: I need to put something back the way it was before my change.
S32: I am about to cut a worktree and a branch for a new piece of work.
S33: Measurements look wrong and I want to check whether the machine itself is the problem.
C01: The user asked which time zone the server stamps its logs in; I'll read one line and tell them.
C02: I'm renaming a local variable inside a single function so it reads better.
C03: I'm fixing a typo in a comment inside the justfile.
C04: Someone asked what a CLI flag does and I'm reading the help output to answer.
C05: I'm reading a design document to find out what was already decided.
C06: I'm re-reading the reply I just drafted to check it says what I meant.
D01: I'm about to act on a claim I have not checked, and deciding whether to verify it first.
D02: I'm about to build something, and deciding whether to read the existing code first or work from my idea of it.
D03: I need a real number to settle this, and I'm deciding what to measure.
D04: Something failed and I'm deciding whether to chase the root cause or patch the symptom.
D05: A decision just got made and I'm deciding whether to write it down or let it live in this conversation.
D06: I found something wrong that is not what I was working on, and I'm deciding whether to fix it now or file it.
D07: I have several pieces of work and I'm deciding what order to do them in and what can overlap.
D08: I hit a constraint that changes the design, and I'm deciding whether to present it before building.
D09: I have more than one workable approach and I'm choosing between them.
D10: I'm deciding which package, layer, or component this responsibility belongs to.
D11: Before writing something new I'm checking whether a standard, helper, or seam already covers it.
D12: A sub-agent or tool reported success and I'm deciding whether to take its word or check underneath.
D13: A test is green and I'm deciding whether it actually proves anything.
D14: I have realised something I said earlier was wrong, and I'm deciding how to correct it.
D15: I have been asked to do something I think is a bad idea, and I'm deciding whether to say so.
D16: Someone else has uncommitted work in the way, and I'm deciding how to proceed without destroying it.
D17: A gate finished and I'm working out what its result actually tells me.
D18: Two sources disagree and I have to work out which is true before continuing.
D19: I'm deciding whether this code is still reachable or can be deleted.
D20: I'm deciding whether a change is safe to merge or needs more thought.
