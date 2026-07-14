---
title: "Setting up ctxloom on a project"
tableOfContents: false
---
<!-- GENERATED (prototype) by scripts/living-docs-prototype/gen_doc_page.py
     from tests/acceptance/features/j1_setup.feature +
     tests/acceptance/features/j1_setup.doc.md, using evidence captured
     from a PASSING acceptance run. Do not hand-edit; edit the narration
     companion or the .feature file and regenerate. -->
<style>
:root { --sl-content-width: 90rem; }
.sl-markdown-content :is(p, ul, ol, blockquote) { max-width: 52rem; }
.living-doc-grid {
  display: grid;
  grid-template-columns: minmax(0, 26rem) minmax(0, 1fr);
  gap: 0.4rem 1.5rem;
  align-items: start;
  margin: 0.75rem 0 1.75rem;
  max-width: none;
}
.living-doc-grid > .ldc { min-width: 0; overflow-x: auto; }
.living-doc-grid > .ldc > :first-child { margin-top: 0; }
.living-doc-grid > .ldc > :last-child { margin-bottom: 0; }
/* Wrap the LEFT column only (the cucumber steps: odd grid children). Long
   Given/When/Then lines wrap to the column width instead of overflowing.
   Expressive Code puts the text in <pre><code>…<div class="ec-line"><div
   class="code">, all defaulting to white-space:pre, so the override reaches
   every level. The RIGHT column (even children — captured terminal output) is
   deliberately left with its default horizontal-scroll behaviour. */
.living-doc-grid > .ldc:nth-child(odd) :is(pre, code, .ec-line, .code) {
  white-space: pre-wrap !important;
  overflow-wrap: anywhere;
  word-break: break-word;
}
@media (max-width: 720px) {
  .living-doc-grid { grid-template-columns: 1fr; gap: 0.2rem; }
}
</style>

:::note
This page is generated from a Gherkin acceptance journey (`j1_setup.feature`) plus real terminal output captured from an actual passing run of it — not hand-written. See [the living-docs proposal](https://github.com/ctxloom/ctxloom/blob/main/docs/living-docs-plan.md) for how.
:::

Every engineer's coding assistant is a blank slate until something fills it
in. Left alone, that happens by accident: whatever the assistant picked up
from this repo's files, whatever the developer happened to paste in, whatever
the model already assumed. Two engineers on the same team end up with two
different assistants, and neither knows the standards the other is quietly
working from.

ctxloom's first job is to close that gap on purpose. A project names the
sources it wants — a developer's own repository of fragments and skills, the
company's shared one — and `ctxloom init` wires them into the project's
configuration. But naming a source is not the same as trusting it: a bundle
can carry a shell command that runs inside the developer's own harness (see
[A prompt is executable code](/security/prompts-are-code/)), so nothing
reaches the assistant on the strength of an address alone. It reaches the
assistant because a key the developer trusts signed it — the developer's own
key for their own work, the company's key for the company's.

This journey proves that chain end to end, in five moves: setup wires the
sources in; a restart — not the setup session itself — is what actually hands
their content to a running assistant; unsigned or untrusted content is held
back rather than delivered; and a human reviews what was held, item by item,
before anything crosses that line.

> A developer's assistant is only as good as the context it is handed. Left to
> chance, every engineer's assistant behaves differently and none of them knows
> the team's standards. ctxloom's first job is to make the right context — the
> developer's own, the team's, the company's — actually reach the assistant,
> automatically, on every session, without copying anything by hand. And only
> the context the developer trusts: everything is signed, and reaches the
> assistant because a key the developer trusts signed it.

## After setup, trusted sources are part of the configuration

Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.

- ✓ Alice has a fresh project directory
- ✓ her personal ctxloom repository is signed with her own key
- ✓ her company's ctxloom repository is signed with the company key, which Alice trusts
- ✓ Alice runs the ctxloom setup for claude-code
- ✓ she adds her personal repository as a source
- ✓ she adds her company's repository as a source
- ✓ her project is configured for claude-code
- ✓ her personal repository's context is part of her configuration, because it is signed with her own key
- ✓ her company repository's context is part of her configuration, because she trusts the company key

<div class="living-doc-grid">
<div class="ldc">

```gherkin
Alice has a fresh project directory
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
her personal ctxloom repository is signed with her own key
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
her company's ctxloom repository is signed with the company key, which Alice trusts
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
Alice runs the ctxloom setup for claude-code
```

</div>
<div class="ldc">

```text
Created profile "default" with bundles: seed
Saved to: /tmp/ctxloom-integration-1147116789/project/.ctxloom/profiles/default.yaml
```

</div>
<div class="ldc">

```gherkin
she adds her personal repository as a source
```

</div>
<div class="ldc">

```text
Pulling dependencies...

Pulled 1 items:
  Installed: 1
```

</div>
<div class="ldc">

```gherkin
she adds her company's repository as a source
```

</div>
<div class="ldc">

```text
Pulling dependencies...

Pulled 2 items:
  Installed: 1
  Skipped (already installed): 1
```

</div>
<div class="ldc">

```gherkin
her project is configured for claude-code
```

</div>
<div class="ldc">

```text
version: 6
llm:
    configs:
        claude-code:
            type: claude-code
    defaults:
        primary: claude-code
        fast: claude-code
agents:
    default:
        engine: claude-code
        profiles:
            - default
default_agent: default
```

</div>
<div class="ldc">

```gherkin
her personal repository's context is part of her configuration, because it is signed with her own key
```

</div>
<div class="ldc">

```text
Materialized default → out (claude-code)
  wrote context
  wrote mcp
  wrote settings
  wrote skills
```

</div>
<div class="ldc">

```gherkin
her company repository's context is part of her configuration, because she trusts the company key
```

</div>
<div class="ldc">



</div>
</div>

This scenario is deliberately narrow: it proves the **posture**, not
**delivery**. After Alice runs setup and adds both repositories as sources,
`ctxloom manage config show` and a materialized profile already reflect that
her personal repository is signed with her own key and her company's is
signed with a key she trusts. That is enough for both to be exposed — the
[three-state gate](/security/trust-states/) allows content the moment a
trusted signature covers its exact bytes, with no separate "turn it on" step.

What this scenario does *not* claim is that a live, running assistant has
already seen this content — a session's context is fixed at the moment it
launches. That is the next scenario's job.

## Setup configures the agents, then a restart delivers their context

Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.

- ✓ her personal ctxloom repository is signed with her own key
- ✓ her company's ctxloom repository is signed with the company key, which Alice trusts
- ✓ Alice runs the ctxloom setup for claude-code
- ✓ she adds her personal and company repositories as sources
- ✓ the setup interview composes her agents' profiles from the sources' fragments
- ✓ ctxloom offers to restart into her newly configured session
- ✓ Alice accepts the restart
- ✓ the restarted mock engine receives her personal repository's fragments
- ✓ the restarted mock engine receives her company repository's fragments

<div class="living-doc-grid">
<div class="ldc">

```gherkin
her personal ctxloom repository is signed with her own key
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
her company's ctxloom repository is signed with the company key, which Alice trusts
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
Alice runs the ctxloom setup for claude-code
```

</div>
<div class="ldc">

```text
Created profile "default" with bundles: seed
Saved to: /tmp/ctxloom-integration-329197080/project/.ctxloom/profiles/default.yaml
```

</div>
<div class="ldc">

```gherkin
she adds her personal and company repositories as sources
```

</div>
<div class="ldc">

```text
Pulling dependencies...

Pulled 2 items:
  Installed: 1
  Skipped (already installed): 1
```

</div>
<div class="ldc">

```gherkin
the setup interview composes her agents' profiles from the sources' fragments
```

</div>
<div class="ldc">

```text
Profile: default
Path: /tmp/ctxloom-integration-329197080/project/.ctxloom/profiles/default.yaml
Default: yes
Description: J1 default profile
Bundles:
  - seed
  - file:///tmp/ctxloom-integration-329197080/remote-266736921/remote.git@bundles/src
  - file:///tmp/ctxloom-integration-329197080/remote-1688341036/remote.git@bundles/src
```

</div>
<div class="ldc">

```gherkin
ctxloom offers to restart into her newly configured session
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
Alice accepts the restart
```

</div>
<div class="ldc">

```text
[mock] mode=1
[mock] fragments=1
[mock] context_length=2677
[mock] prompt=continue
ctxloom: companion ltk v0.7.0-7676b91-20260713T155949-dirty
ctxloom: warning: companion reprise (/home/babbitt/.cargo/bin/reprise): run version --format json: exit status 2
ctxloom: companion taskloom v0.7.0-7676b91-20260713T155952-dirty
ctxloom: starting session regal-local-scale
```

```text
=== Arguments ===
mode=1
fragments=1
cwd=/tmp/ctxloom-integration-329197080/project
=== Env ===
=== Context ===
# Example Fragment

Add your content here.

---

J1-COMPANY-REPO-CONTEXT-MARKER

---

J1-PERSONAL-REPO-CONTEXT-MARKER

---

# llm-tool-killer (ltk)

This project may run **ltk**, a pre-tool hook that inspects each shell
command before it executes and redirects it when a rule matches. Where
ctxloom shapes the context you see, ltk guides the commands you run.

## What it does

ltk parses the real command (resolving variables, unwrapping trivial
wrappers and sub-shells) and matches it against the project's rules in
`.ltk/config.yaml`. The first matching `deny` wins and returns a
`message`/`suggest` telling you what to run instead. Example:

    go test ./...   ->   blocked: "Run tests through the task runner."
                    ->   retry with `just test`

## How to work with it

- Treat a redirect as guidance, not a failure: read the suggestion and
  retry the command the way the rule asks.
- Prefer the project's task runner (e.g. `just <target>`) over invoking
  build/test/lint tools directly.
- **Agents do not cut releases.** ltk blocks `git tag` and release
  commands. Prepare the version bump and PR; a human (or CI) cuts the tag.

## What it is not

ltk is a cooperative redirect, not a sandbox. If explicitly instructed
to work around a rule the agent can, so it makes the easy, accidental
path the right one rather than enforcing hard isolation. For strict
"never" boundaries, run the agent in a container.

---

# taskloom

Persistent task tracking. Tasks live in a per-project append-only log
(~/.ctxloom/tasks/<project-id>.jsonl) and are keyed by harp IDs
(e.g. `swift-amber-falcon`). Statuses: `In Progress`, `To Do`,
`Deferred`, `Done`, `Archived`.

## MCP tools (served by `taskloom mcp`)

- `task_list({statuses?, term?, include_completed?, include_summary?})`
  — list/filter tasks. Set `include_summary: true` to also get
  per-status counts plus the in-progress harp IDs.
- `task_add({text, status?, trigger?})` — add a task with a fresh
  harp ID. Default status is `"To Do"`; `"Deferred"` requires a
  `trigger` (the condition that should revive it).
- `task_set_status({harp_id, status, trigger?})` — move a task
  between statuses.
- `task_edit({harp_id, text})` — replace a task's text in place.

Tasks are created and updated only through these tools (or the
`taskloom` CLI). The harp ID appears in `task_list` output so you can
reference a specific task in later calls.

## Plan stamping

When you edit a plan file (`CURRENT_PLAN.md`, `*-plan.md`,
`docs/*-plan.md`), the active session's harp name is auto-stamped
into the file's YAML frontmatter `sessions:` list. Plans and
sessions cross-reference without a separate database.
=== Prompt ===
continue
```

</div>
<div class="ldc">

```gherkin
the restarted mock engine receives her personal repository's fragments
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
the restarted mock engine receives her company repository's fragments
```

</div>
<div class="ldc">



</div>
</div>

The discovery session that walks Alice through setup is, itself, a running
assistant — and it cannot see what it is in the middle of installing. It
composes her agents' profiles from both sources' fragments as configuration
*to be written*, but a session's assembled context is fixed at launch; you
cannot hand a running assistant a fragment file it will only start
respecting after this conversation ends. So setup's last act (`ctxloom
init`'s `offerSessionRelaunch`) is to offer a **restart**: exit this session,
launch a fresh one against the configuration that was just written.

That two-phase shape — *configure, then restart to deliver* — is the whole
point of this scenario. The captured evidence below is the mock engine's own
record of what it received on that fresh launch: both the personal and
company markers, present because the restarted process resolved the same
composed profile from scratch, not because anything was injected after the
fact.

## The restarted assistant can see every source

*Tags: @live*

```gherkin
  Scenario: The restarted assistant can see every source
    Given her personal and company repositories are trusted, signed sources
    And each source carries a distinct marker phrase
    And Alice has completed setup and restarted into her configured session
    When she asks her assistant to repeat every marker phrase it can see
    Then its reply contains her personal repository's marker
    And its reply contains her company repository's marker
```

> **Not captured in this build.** This scenario was not exercised in the run that generated this page (for example, a `@live` scenario without credentials in this environment). The Gherkin below is still the live spec — just without a proof-of-passing run attached yet.

## Content ctxloom cannot verify is held, not delivered

**Example 1**

Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.

- ✓ a third-party ctxloom repository whose content is unsigned
- ✓ Alice adds it as a source
- ✓ Alice starts a session
- ✓ her assistant does not receive that repository's content
- ✓ Alice is told the content is held for her review

<div class="living-doc-grid">
<div class="ldc">

```gherkin
a third-party ctxloom repository whose content is unsigned
```

</div>
<div class="ldc">

```text
Created profile "default" with bundles: seed
Saved to: /tmp/ctxloom-integration-3448338260/project/.ctxloom/profiles/default.yaml
```

</div>
<div class="ldc">

```gherkin
Alice adds it as a source
```

</div>
<div class="ldc">

```text
Pulling dependencies...

Pulled 1 items:
  Installed: 1
ctxloom: warning: 1 item(s) awaiting review — run 'ctxloom review'
ctxloom: warning: 1 item(s) awaiting review — run 'ctxloom review'
```

</div>
<div class="ldc">

```gherkin
Alice starts a session
```

</div>
<div class="ldc">

```text
Materialized default → out (claude-code)
  wrote context
  wrote mcp
  wrote settings
  wrote skills
ctxloom: warning: 1 item(s) awaiting review — run 'ctxloom review'
```

</div>
<div class="ldc">

```gherkin
her assistant does not receive that repository's content
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
Alice is told the content is held for her review
```

</div>
<div class="ldc">



</div>
</div>

**Example 2**

Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.

- ✓ a third-party ctxloom repository whose content is signed with a key Alice does not trust
- ✓ Alice adds it as a source
- ✓ Alice starts a session
- ✓ her assistant does not receive that repository's content
- ✓ Alice is told the content is held for her review

<div class="living-doc-grid">
<div class="ldc">

```gherkin
a third-party ctxloom repository whose content is signed with a key Alice does not trust
```

</div>
<div class="ldc">

```text
Created profile "default" with bundles: seed
Saved to: /tmp/ctxloom-integration-238127705/project/.ctxloom/profiles/default.yaml
```

</div>
<div class="ldc">

```gherkin
Alice adds it as a source
```

</div>
<div class="ldc">

```text
Pulling dependencies...

Pulled 1 items:
  Installed: 1
ctxloom: warning: 1 item(s) awaiting review — run 'ctxloom review'
ctxloom: warning: 1 item(s) awaiting review — run 'ctxloom review'
```

</div>
<div class="ldc">

```gherkin
Alice starts a session
```

</div>
<div class="ldc">

```text
Materialized default → out (claude-code)
  wrote context
  wrote mcp
  wrote settings
  wrote skills
ctxloom: warning: 1 item(s) awaiting review — run 'ctxloom review'
```

</div>
<div class="ldc">

```gherkin
her assistant does not receive that repository's content
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
Alice is told the content is held for her review
```

</div>
<div class="ldc">



</div>
</div>

Unsigned content and content signed by a key Alice hasn't chosen to trust are
not two different problems — to the gate, they are the identical case. Both
resolve to an empty verified signer, and an empty verified signer is
withheld. There is no fourth, in-between state for "signed, but by someone I
don't yet trust": either a trusted key's signature verifies over these exact
bytes, or the content is pending, full stop.

Nothing about this fails loudly. Alice's assistant simply never receives the
held marker; the only signal is one aggregate, content-free line telling her
something is waiting on her review.

## Alice reviews held content and decides item by item

Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.

- ✓ two sources are held for Alice's review
- ✓ Alice reviews the held content
- ✓ she is shown each held item and where it came from
- ✓ she approves the first and rejects the second
- ✓ Alice starts a new session
- ✓ her assistant receives the item she approved
- ✓ her assistant never receives the item she rejected

<div class="living-doc-grid">
<div class="ldc">

```gherkin
two sources are held for Alice's review
```

</div>
<div class="ldc">

```text
Pulling dependencies...

Pulled 2 items:
  Installed: 1
  Skipped (already installed): 1
ctxloom: warning: 2 item(s) awaiting review — run 'ctxloom review'
ctxloom: warning: 2 item(s) awaiting review — run 'ctxloom review'
```

</div>
<div class="ldc">

```gherkin
Alice reviews the held content
```

</div>
<div class="ldc">

```text
2 item(s) pending review (0 update(s)):

file:///tmp/ctxloom-integration-1414300136/remote-1844452430/remote.git@bundles/src (remote: first)
  new      fragments/marker

file:///tmp/ctxloom-integration-1414300136/remote-3996048272/remote.git@bundles/src (remote: second)
  new      fragments/marker

Run 'ctxloom review' in a terminal to review interactively, or use the
plumbing per item: ctxloom trust <bundle-ref>#<kind>/<name> / ctxloom blacklist <ref>.
```

</div>
<div class="ldc">

```gherkin
she is shown each held item and where it came from
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
she approves the first and rejects the second
```

</div>
<div class="ldc">

```text
Rejected src#fragments/marker
  repo:  file:///tmp/ctxloom-integration-1414300136/remote-3996048272/remote.git
  store: user
  UNSIGNED — recorded locally, not shareable (no signing key was available)
  ref block: recorded (sticky — survives content changes)
  content:   rejected in form(s) raw (blocks this content even if renamed/moved)
ctxloom: warning: 1 item(s) awaiting review — run 'ctxloom review'
ctxloom: warning: 1 item(s) awaiting review — run 'ctxloom review'
```

</div>
<div class="ldc">

```gherkin
Alice starts a new session
```

</div>
<div class="ldc">

```text
Materialized default → out2 (claude-code)
  wrote context
  wrote mcp
  wrote settings
  wrote skills
ctxloom: warning: 1 item(s) awaiting review — run 'ctxloom review'
```

</div>
<div class="ldc">

```gherkin
her assistant receives the item she approved
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
her assistant never receives the item she rejected
```

</div>
<div class="ldc">



</div>
</div>

Held is not stuck — it is a queue with a name and a command. `ctxloom review
--list` shows Alice exactly what is pending and which remote each item came
from, so "something is awaiting review" becomes a specific, inspectable
list. From there the decision is per item, not per source: she can approve
the first and reject the second even though both arrived the same way,
because trust in this model is never "I trust this repository" — it is "I
approve *this exact content*."

Approving and rejecting are not mirror images of the same action. An
approval is a countersignature over content Alice explicitly reviewed — if
that content changes even one byte, the approval no longer covers it and the
item falls back to pending. A rejection is stickier by design: it is recorded
against the ref (so it survives the content changing under it) *and* against
the content with the ref stripped out (so the same bytes stay rejected even
if they resurface renamed, or from a different remote entirely).

J1 stops at the trust boundary: what reaches a real assistant, and why. What
a company's or a developer's source can additionally *contribute* to the
setup conversation itself — not just fragments the assistant reads later, but
onboarding steps the interview asks about right now — is a companion journey,
"Sources and companions shape how a project is set up"
(`j1b_source_augmentation.feature`; not yet published as its own page).

For the full trust model this journey exercises only a slice of, see
[Trust states and the gate](/security/trust-states/) and
[Review and trust](/concepts/review-and-trust/).
