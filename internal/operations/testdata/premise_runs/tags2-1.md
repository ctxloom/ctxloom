# Applicability, premise and tags

You are an agent about to act. Below are 9
fragments of guidance you may load, and a list of SITUATIONS: statements of what an
agent is about to do.

Each fragment gives you TWO signals:

  PREMISE -- the condition under which the guidance applies.
  TAGS    -- the artifacts, tools and concepts the fragment concerns.

BOTH are valid grounds for including a fragment. Specifically:

  - If the PREMISE describes the moment, include it.
  - If the TAGS clearly match what the situation is about, that is ALSO
    sufficient grounds to include it, even where the premise reads as a
    near-miss.
  - Where the premise is a BORDERLINE call, let a tag match settle it toward
    INCLUDING. We would much rather offer a fragment that turns out to be
    unnecessary than withhold one that was needed: an unnecessary fragment
    costs a little context, while a withheld one is never learned to exist and
    cannot be asked for.
  - Only when neither the premise nor the tags reach the moment, answer NONE.

Bear in mind the situations are terse -- one line each, where a real moment
carries much more. Judge what the line is plainly ABOUT, and do not require it
to spell out every condition a premise names.

Consider each fragment ON ITS OWN against every situation. You are not choosing
a best match; several fragments may apply to the same situation, and that is
expected.

Output one line per fragment, nothing else:

    <fragment-name>: <situation ids, or NONE>

## Fragments (9)

### close-the-turn
TAGS: turn, task, status, finding, deferral, report, backlog, tag
PREMISE: You are finishing a turn, or you have surfaced something you are not going to fix right now. For anyone about to leave an issue unresolved, or about to report what happened.

### config-hierarchy
TAGS: config, flag, environment variable, config key, precedence, override, schema
PREMISE: You are adding or changing a flag, an environment variable, or a config key; working out which layer won when a setting resolved unexpectedly; or reading configuration to decide how something is declared. For anyone touching how this family of binaries is configured, however the moment is worded.

### coordination-tools
TAGS: agent_run, agent_send, agent_recv, roster, artifact, child agent, run id, mailbox
PREMISE: You are calling one of the agent coordination tools and need to know its semantics -- what agent_run returns, how agent_recv delivers, how a child addresses its parent, how an artifact is fetched. For anyone who has already decided to delegate and now needs the mechanics. Skip this when the question is WHETHER to delegate or HOW to write the brief.

### delegation
TAGS: file, search, sub-agent, finder, lookup, investigation, context, web page
PREMISE: You are about to read several files, run a broad search, fetch a page, or take on a self-contained investigation yourself; or you are deciding who should do a piece of work. For anyone weighing doing it against handing it to a sub-agent.

### design-review-checkpoints
TAGS: design, signature, API surface, library, dependency, interface, struct, type
PREMISE: You are about to propose or begin an implementation, add or drop a library, or close a turn that produced code. For anyone about to settle an API surface the human has not seen yet.

### documentation
TAGS: markdown, document, plan, README, file, comment, changelog, tracking file
PREMISE: You are about to create a markdown file, a plan, a tracking or meta document, or to write a comment recording what changed or when. For anyone whose next write is documentation rather than code.

### error-constants
TAGS: error, constant, sentinel, assertion, test, message, literal
PREMISE: You are declaring an error value, or writing a test assertion against one. For anyone about to type the same message literal in more than one place.

### fail-loud-launch
TAGS: check, validation, startup, refusal, degraded, broken input, error class
PREMISE: You are writing a check that can find something wrong; deciding what a command should do when its input is broken, missing, or untrusted; or choosing between refusing and carrying on with less. For anyone deciding how loudly a failure should surface.

### general
TAGS: package, layer, module, boundary, dependency direction, cohesion, coupling, connascence
PREMISE: You are placing new code, changing a module boundary, or judging whether a change fits the system. For anyone deciding where something belongs, or what is now allowed to depend on what.

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
