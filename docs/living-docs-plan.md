# Living docs: generating a website page from a passing @doc journey

> STATUS: proposal + working prototype (this branch). Nothing here is wired
> into `just build`, `just gen-docs`, or CI. The generator is a standalone
> script; running it is a manual step until a real integration is decided.

## The problem

Today, a docs page and the behavior it describes are two unrelated artifacts
that happen to agree right now. A feature can regress — or never have worked
the way the page claims — and the only thing that notices is a human
happening to try it. There is no structural link between "the docs say X"
and "X is true of the running system."

Acceptance journeys tagged `@doc` (`tests/acceptance/features/j000200_setup.feature`,
`j000300_source_augmentation.feature`) already assert real, user-visible behavior
end to end: real CLI invocations against a real built `ctxloom` binary, real
trust/signing machinery, and — for the scenarios that exercise agent
delivery — a real assembled payload via the mock LLM backend. That is already
a stronger truth source than hand-written prose. This proposal turns it into
one.

## What "living" means here, concretely

A docs page is assembled from three ingredients that already exist for
independent reasons, combined by a generator:

1. **The Gherkin itself** — already the business-readable spec. Verbatim,
   not reworded.
2. **Captured terminal evidence from an actual passing run** — the CLI
   stdout/stderr for each step, and, for scenarios that go through the mock
   engine, the mock's own record of what it received (the
   `internal/lm/backends/mock.go` `"=== Prompt ==="` payload — the thing the
   whole suite is built to prove was assembled correctly).
3. **Human narration** — prose that goes *beyond* the Gherkin: the why, the
   connective reasoning, what a reader would otherwise have to reverse
   engineer from the harness code.

If (1) and (2) can't be produced — the scenario doesn't exist, or it exists
but fails — there is nothing for (3) to attach to, and no page. That is the
enforcement mechanism: **a page can only be regenerated from a passing run.**
A red scenario doesn't produce a wrong page; it produces no page (the
generator hard-errors — see "Honesty enforcement" below).

## Investigation findings

### godog attachment support: YES, natively, in the pinned version

`github.com/cucumber/godog v0.15.1` (this repo's pinned version — confirmed
in `go.mod` and in the module cache) ships first-class cucumber-message
attachments:

- `godog.Attach(ctx, godog.Attachment{Body, FileName, MediaType}) context.Context`
  and `godog.Attachments(ctx) []Attachment` (`attachment_test.go`,
  `suite.go:84`).
- Step functions, and `ctx.Before` / `ctx.After` / `ctx.StepContext().Before`
  / `ctx.StepContext().After` hooks, can all return `(context.Context, error)`
  to thread attachments forward (`internal/formatters/fmt_output_test.go`
  demonstrates this exact pattern for `StepContext().After`).
- The `cucumber` formatter (`internal/formatters/fmt_cucumber.go`) serializes
  every attachment as a step-level `embeddings: [{name, mime_type, data
  (base64)}]` entry in its JSON output, and `godog.Options.Format` accepts
  multiple comma-separated formatters (`run.go:174`, `ifmt.MultiFormatter`) —
  so a real run could add `,cucumber:<file>.json` alongside the existing
  `"pretty"` formatter without disturbing the human-readable console output
  at all.

So the "is this even a channel that exists" question is settled: yes. This
prototype **uses the native attachment call** (`godog.Attach`) at the same
seam a `cucumber`-formatter-based pipeline would use, but does **not**
additionally require running with the `cucumber` formatter to get the
evidence out to disk — see the next section for why.

### The concrete capture seam

`tests/acceptance/world.go`'s `InitializeScenario` already wires
per-scenario `ctx.Before` / `ctx.After` hooks (constructing/tearing down the
`World`). This prototype adds one more registration,
`registerDocCaptureHooks(ctx)`, in a new file,
`tests/acceptance/steps_doc_capture.go`. It is **inert unless
`CTXLOOM_DOC_CAPTURE_DIR` is set** — every other suite run (`just
test-acceptance`, CI) pays nothing and behaves identically to before this
file existed.

When the env var is set, for every scenario carrying the `@doc` tag:

- `ctx.Before` seeds a `*docCapture` accumulator on the `World` (scenario
  name, feature URI, tags).
- `ctx.StepContext().After` fires after **every** step (not just the ones a
  feature author remembered to instrument — this is the load-bearing design
  choice: no step in `steps_j000200_setup.go` / `steps_j000300.go` needed to change).
  It reads `World.env.LastOutput()` (the CLI stdout+stderr the harness
  already tracks) and whichever mock-recorded slot the scenario populated
  (`j000200RestartRecorded` / `j000300Recorded`), de-duplicating so a no-op step
  doesn't inherit the *previous* step's output as if it had produced it
  too — a real bug this prototype hit and fixed (see "Bugs found" below).
  It both calls `godog.Attach` (the native channel) **and** appends to the
  in-memory accumulator.
- `ctx.After` (scenario end) flushes the accumulator to
  `<dir>/<scenario-slug>-<pickle-id>.json` — one file per scenario (per
  Examples row, for Outlines).

Direct-JSON-per-scenario, rather than parsing a `cucumber:<file>.json` run
artifact, is a **prototype simplification**, not a rejection of the native
channel — the same `godog.Attach` call populates both; a future iteration
could drop the direct-JSON path entirely and have the generator parse the
`cucumber` formatter's `embeddings` instead, if a single combined artifact
per suite run turns out to matter more than one-file-per-scenario. Both are
real files; nothing here is invented for the prototype's convenience.

**Files:**
- `tests/acceptance/steps_doc_capture.go` (new)
- `tests/acceptance/world.go` (added 4 fields to `World`, one line
  registering the hook)

### The mock engine's recorded payload

`internal/lm/backends/mock.go`'s `recordMockInput` writes exactly:

```
=== Arguments ===
mode=...
fragments=...
cwd=...
=== Env ===
...
=== Context ===
<assembled context>
=== Prompt ===
<prompt text>
```

to `CTXLOOM_MOCK_RECORD_FILE`. This is the gold artifact for delivery
scenarios — it is not a proxy (exit code, file-exists) but literally what an
agent process received. J000200's own step assertions already check `Contains`
against this text; the capture sidecar just also preserves it for the page.

### Site conventions (matched exactly)

- Pages: `website/src/content/docs/**/*.md`, Starlight content collection.
- Frontmatter: only `title` in quotes; no other required fields (confirmed
  across `security/trust-states.md`, `getting-started/binary-trust.md`,
  `concepts/review-and-trust.md`).
- Auto-generated pages carry two things the hand-written pages don't
  (confirmed in `reference/cli/ctxloom_review.md`, produced by
  `just gen-docs`):
  1. An HTML comment naming the generator and source, with a "do not edit,
     edit X instead" instruction.
  2. A Starlight `:::note` admonition telling the *reader* the page is
     generated and where from.
  This prototype's generator reproduces both conventions verbatim.
- Sidebar (`website/astro.config.mjs`) is a hand-maintained tree; this
  prototype's page is **not** added to it (see "What integration would still
  need").

## Format decision: code blocks, not SVG — validated, not just assumed

**Decision: plain fenced code blocks (` ```text `) for every capture in this
prototype. No SVG was produced, and none was needed.**

The lean going in was "code blocks for hermetic scenarios, SVG only for a
real `@live` colored terminal recording." Running the actual suite confirmed
this rather than just asserting it:

- Every `@doc` scenario in J000200 that isn't `@live` drives the CLI through
  `TestEnvironment.Run`, which uses `exec.Command` with `bytes.Buffer` stdout/
  stderr — not a pty. No TTY means the CLI's own color/isatty detection
  never fires, so the captured text contains **zero ANSI escape codes**,
  confirmed by inspecting the actual capture files. There is nothing for an
  SVG renderer to add: the payload already *is* plain text, and a code block
  is that text, verbatim, diffable in git, greppable, selectable, and
  requiring no renderer at all under the site's strict CSP.
- One `@doc` scenario (`j000300`'s "Trusted sources augment the setup
  interview") *does* drive a real pty
  (`driveDiscoverySessionViaMock` → `TestEnvironment.RunPTY`), because it has
  to answer an interactive prompt. Its captured mock-recorded payload was
  inspected for ANSI content: none appeared in this run (the discovery
  session's own output — not captured here, the *mock's received input* was
  — would be the place color could appear, and wasn't examined for this
  reason: it's out of scope for a J000200-focused page). If a *future* `@live`
  scenario capture — a real engine's own colored terminal session — turns
  out to carry meaningful ANSI, SVG becomes worth the renderer investment
  then, on that evidence. Building it speculatively now, with no real
  colored capture to render, would mean shipping a code path this
  prototype cannot validate. **No SVG is included, on purpose — producing
  one now would mean fabricating or cherry-picking non-representative
  input, which the task rules out.**

## The narration companion format

One new file per feature: `tests/acceptance/features/j000200_setup.doc.md`
(prototype ships this one; `j000300_source_augmentation.doc.md` would follow the
same shape, not authored in this pass). The `.feature` file is untouched —
it stays a clean spec, no doc-comments bolted in.

Marker convention (HTML comments, invisible if the file were ever rendered
raw):

```
<!-- doc:intro -->
  ... opening prose, appears once, before the first scenario ...
<!-- /doc:intro -->

<!-- doc:scenario: <exact Scenario or Scenario Outline name from the .feature> -->
  ... prose specific to this scenario, rendered directly above its
      Gherkin + captured evidence ...
<!-- /doc:scenario -->

<!-- doc:outro -->
  ... closing prose / links, appears once, after the last scenario ...
<!-- /doc:outro -->
```

Matching is by **exact scenario name string**, not by order or line number —
so narration and Gherkin can be edited independently and a rename doesn't
silently misattach prose to the wrong scenario (it just drops the orphaned
block, which is easy to spot in review). A scenario with no matching
`doc:scenario` block still renders in full (Gherkin + evidence); narration is
strictly additive, never required for the page to build.

(This repo's actual `j000200_setup.doc.md` explains its own marker syntax
descriptively rather than reproducing the literal marker text — the first
prototype draft had the *literal* marker syntax written out inside its own
explanatory header comment, which the parser's regex then matched *first*,
before the real content. A real bug, fixed by not spelling out the literal
syntax inside a comment the parser would also scan — see "Bugs found.")

## The generator

`scripts/living-docs-prototype/gen_doc_page.py` (~250 lines, stdlib-only
Python, no dependencies). Pipeline:

```
parse_feature(path)      -> {name, description, scenarios: [{keyword, name, tags, body}]}
parse_narration(path)    -> {intro, outro, scenarios: {name: prose}}
load_captures(dir)       -> {scenario_name: [capture_json, ...]}   # 1 file per Examples row

for each scenario in feature order:
    caps = load_captures[scenario.name]
    assert_all_passed(caps)          # <-- the honesty gate; raises SystemExit on any non-"passed" step
    render narration (if present) + fenced Gherkin + captured evidence (if any) or a
    "not captured in this build" note (if the scenario simply wasn't run — e.g. @live, no creds)

write frontmatter + generated-by banner + :::note + intro + description-blockquote + scenarios + outro
```

Two implementation details worth calling out because they were real bugs,
not hypothetical ones (see below): (1) the `.feature` parser must treat a
new `Scenario:`/`Scenario Outline:` line as a scenario boundary even with no
preceding `@tag` line — most scenarios in this codebase are separated only
by a `#`-comment, not a tag; (2) every code fence is emitted via a
`safe_fence()` helper that counts the longest run of backticks already
present in the content and fences with one more than that — captured mock
payloads can themselves contain fenced markdown (an onboarding skill's own
```-fenced example), which will otherwise prematurely close the wrapping
fence and corrupt the page.

## Honesty enforcement, precisely

- **Ran and failed → hard stop.** `assert_all_passed` raises `SystemExit`
  (nonzero exit) the instant it finds a captured step whose status isn't
  `"passed"`, for *any* capture file matching *any* scenario in the feature.
  The generator produces **no output file at all** in this case — not a
  partial page, not a page with a red banner. This is deliberate: a
  generator that degrades gracefully on a failure is a generator a team
  will eventually trust past what it's proving.
- **Never run in this pass (e.g. `@live`, no credentials) → rendered, but
  clearly marked.** This is different from "ran and failed." The Gherkin
  still renders (it's still the live spec) inside a `:::note[Not captured
  in this build]` admonition explaining why there's no proof-of-passing
  attached *yet*. A reader can tell "this claim has receipts" from "this
  claim doesn't have receipts in this build" apart, at a glance, per
  scenario — never conflated into one global "this page might be stale"
  disclaimer.
- **Staleness detection** (for a real pipeline, not built in this
  prototype): the generated page's HTML comment banner should also record
  the commit SHA the capture run was built at (trivial addition — the
  capture JSON already has everything else, this just adds the version
  string `just build` already stamps into the binary). A CI check comparing
  that recorded SHA against the feature file's git blame / last-modified
  commit would flag "this page's evidence predates the current spec" —
  i.e., the Gherkin changed since the page was last regenerated. This is
  the same shape as any generated-artifact-drift check; it isn't built here
  because it belongs in the CI-integration follow-up, not the prototype.

## Bugs found while building this (all fixed, all instructive)

1. **CLI-output misattribution across no-op steps.** `TestEnvironment.LastOutput()`
   persists until the next command runs. A step that runs no CLI command
   (several J000200 steps are intentionally no-ops — see
   `steps_j000200_setup.go`'s "ctxloom offers to restart..." comment) would
   otherwise inherit the *previous* step's captured output as if it had
   produced it. Fixed by tracking the last-attached output on `World` and
   only attributing genuinely new output to a step
   (`World.docLastCLIOutput`).
2. **Feature parser scenario-boundary bug.** The first parser version only
   treated an `@tag` line as a new-scenario boundary. Most scenarios in
   `j000200_setup.feature` are preceded only by a `#`-comment (e.g. `# LOCKED —
   ...`), not a tag, so consecutive untagged scenarios were silently
   concatenated into one giant scenario body. Fixed by also breaking on a
   bare `Scenario:`/`Scenario Outline:` line.
3. **Marker self-collision.** The narration file's own explanatory header
   comment, describing the marker syntax, contained the literal marker text
   as an example — which the parser's regex matched *before* the real
   marker further down the file, rendering the literal placeholder text
   ("...") as the page's intro. Fixed by describing the syntax without
   reproducing it verbatim.
4. **Fence-collision risk (caught, not yet triggered on this page).**
   `j000300`'s captured mock-recorded payload (not used on the J000200 page shipped
   here) contains an onboarding skill's own ` ``` `-fenced example. A naive
   ` ```text ` wrapper would have been closed early by that inner fence.
   Fixed with `safe_fence()` before it could corrupt any page — caught by
   grepping the actual captured JSON for backtick runs, not by inspection of
   the rendering.

None of these would have been caught by design review alone; all four
surfaced only once real captured data was run through the real generator —
which is itself an argument for why this pipeline is worth building: docs
generation, like the docs it produces, benefits from being exercised against
reality rather than assumed correct.

## What a real pipeline integration would still need

- **Where the capture step lives in CI.** The capture run
  (`CTXLOOM_DOC_CAPTURE_DIR=... just test-acceptance` or similar) would need
  to be a distinct CI step/job, its artifact directory uploaded, and the
  generator run against it — separately from (and after) the acceptance gate
  that decides whether the build is green at all. Generating docs is not a
  test; it should never be allowed to fail the build for a reason other than
  "the docs generator itself is broken."
- **`@live` scenarios never get captured in CI** (no credentials there
  either, same as this sandbox) — meaning their pages will *always* show
  "not captured in this build" in an automated pipeline, forever, unless a
  separate credentialed capture run is added as its own job and its capture
  directory merged in before generation. That's a real, not hypothetical,
  gap: the `@live` "assistant can see every source" / "assistant follows
  composed guidance" scenarios are exactly the ones a reader would most want
  proof of.
- **Sidebar wiring.** This prototype's page is not added to
  `astro.config.mjs`'s sidebar tree (orphaned but built and reachable by
  direct URL) — deliberately, per this task's "do not ship a generator into
  the build pipeline" constraint. A real integration needs a decision on
  whether generated journey pages get their own sidebar section (e.g.
  "Journeys") and whether that list is itself generated (probably yes, once
  there's more than one).
- **Multi-feature / multi-page generation** — this prototype takes one
  `--feature`/`--narration` pair per invocation; a real pipeline would glob
  every `@doc`-tagged feature and its `.doc.md` companion.
- **Staleness CI check**, as described above — not built, straightforward
  to add once the capture step has a stable home in CI.
- **Deciding the `cucumber`-formatter-vs-direct-JSON question for real**,
  per the "concrete capture seam" section above, once there's a second
  consumer of the captured evidence (this prototype only has one: this
  generator) to weigh the tradeoff against.
