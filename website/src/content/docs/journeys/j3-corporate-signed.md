---
title: "Skills my company has validated"
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

The gap between "we wrote a standard" and "the standard is enforced" is
provenance. A company can publish a `secure-coding` bundle, but that is worthless
if anyone who can push to a repo can silently rewrite it — changing how every
engineer's assistant behaves, or slipping in an executable that runs on their
machines. What a company actually needs is a guarantee: what reached my assistant
came from who I think it did, unchanged, and only what I allowed.

Note what this is **not**. J3 proves *provenance and integrity*, not *secrecy*.
ctxloom does not encrypt your context and there is no eavesdropper in this story
— no Eve trying to read the guidance. The adversary is **Mallory**, and she does
not want to read anything; she wants to *change what reaches the assistant*. Every
guarantee below is aimed at her: at making sure that a signature is checked over
the exact bytes about to be exposed, that tampering is caught loudly rather than
degraded quietly, that executables face the same gate as prose, and that a
publisher — or a whole compromised key — can be pulled back after the fact.

The trust primitive underneath is a signature over bytes, from a key you chose to
trust. Contrast this with [J2](/journeys/j2-team-authoring/), where content is
trusted for being *first-party* — authored in the team's own project. Here the
content is remote: it earns exposure only because the company's key signed it and
Alice trusts that key for publishing. The full model — the resolver, the
namespaces, the storage — is in [Trust states and the gate](/security/trust-states/),
[Review and trust](/concepts/review-and-trust/), and [Key management](/security/key-management/).

> A company needs to guarantee that the guidance reaching its engineers'
> assistants is guidance the company actually approved — and that nothing else
> slips in. That is the difference between "we wrote a standard" and "the
> standard is enforced." Without it, anyone who can publish can silently rewrite
> how every engineer's assistant behaves, or worse, ship an executable that runs
> on their machines. ctxloom answers one question crisply: did what reached my
> assistant come from who I think, unchanged — and only what I allowed?

## Alice references a bundle from the company repo and its guidance reaches her assistant

Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.

- ✓ Trent's company publishes a "secure-coding" bundle, signed with the company key
- ✓ Alice trusts the company key
- ✓ Alice references the company's secure-coding bundle from her project
- ✓ Alice starts a session
- ✓ her assistant receives the company's secure-coding guidance, because the company key signed it

<div class="living-doc-grid">
<div class="ldc">

```gherkin
Given Trent's company publishes a "secure-coding" bundle, signed with the company key
```

</div>
<div class="ldc">

```text
Created profile "default" with bundles: seed
Saved to: /tmp/ctxloom-integration-1418880727/project/.ctxloom/profiles/default.yaml
```

</div>
<div class="ldc">

```gherkin
And Alice trusts the company key
```

</div>
<div class="ldc">

```text
Trusted trent@example.com for publish.v1.ctxloom.dev (SHA256:NGzFC45UqnDYj9uDxmvauI7laCltRoOsQNcllzeZx4A) — wrote /tmp/ctxloom-integration-1418880727/project/.ctxloom/allowed_signers
```

</div>
<div class="ldc">

```gherkin
When Alice references the company's secure-coding bundle from her project
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
And Alice starts a session
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
Then her assistant receives the company's secure-coding guidance, because the company key signed it
```

</div>
<div class="ldc">

```text
# Example Fragment

Add your content here.

---

J3-COMPANY-SECURECODING-MARKER

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
```

</div>
</div>

Before any of the adversarial cases, the happy path: the reference mechanic
itself. Alice does not fork or copy the company's bundle — she *references* it
from her own project, pulling one specific bundle out of another repository's
history. Its guidance flows to her assistant for exactly one reason, stated in
the Gherkin's `Then`: the company key signed it and Alice trusts that key. This
is the baseline every later scenario perturbs — tamper with the bytes, ship an
executable, retract the version, revoke the key — and watches the guarantee hold.

## Content Mallory altered after it was signed is refused, loudly

Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.

- ✓ Trent's company publishes a "secure-coding" bundle, signed with the company key
- ✓ Alice trusts the company key
- ✓ Mallory alters the company's secure-coding bundle after it was signed
- ✓ Alice syncs her project
- ✓ her assistant does not receive the altered guidance
- ✓ Alice is warned that the content's signature does not verify

<div class="living-doc-grid">
<div class="ldc">

```gherkin
Given Trent's company publishes a "secure-coding" bundle, signed with the company key
```

</div>
<div class="ldc">

```text
Created profile "default" with bundles: seed
Saved to: /tmp/ctxloom-integration-601601241/project/.ctxloom/profiles/default.yaml
```

</div>
<div class="ldc">

```gherkin
And Alice trusts the company key
```

</div>
<div class="ldc">

```text
Trusted trent@example.com for publish.v1.ctxloom.dev (SHA256:QY0fAfqxhYYF1qv74svWhQYMeOkCr7bmKyo37kcIJd8) — wrote /tmp/ctxloom-integration-601601241/project/.ctxloom/allowed_signers
```

</div>
<div class="ldc">

```gherkin
And Mallory alters the company's secure-coding bundle after it was signed
```

</div>
<div class="ldc">

```text
added bundle: file:///tmp/ctxloom-integration-601601241/remote-3427465789/remote.git@bundles/secure-coding
Modified profile "default"
```

</div>
<div class="ldc">

```gherkin
When Alice syncs her project
```

</div>
<div class="ldc">

```text
ctxloom: warning: remote bundle "file:///tmp/ctxloom-integration-601601241/remote-3427465789/remote.git@bundles/secure-coding" has a signature that does not verify over its content; withholding it: signature does not cover these bytes: signed by trent@example.com: verify: ssh: signature did not verify
ctxloom: warning: skipping unresolved bundle "file:///tmp/ctxloom-integration-601601241/remote-3427465789/remote.git@bundles/secure-coding": bundle not found: file:///tmp/ctxloom-integration-601601241/remote-3427465789/remote.git@bundles/secure-coding
ctxloom: aborting startup: 1 fatal finding(s); fix them, or rerun with --degraded (env CTXLOOM_DEGRADED=1) to launch anyway:
  - [trust-store] remote bundle "file:///tmp/ctxloom-integration-601601241/remote-3427465789/remote.git@bundles/secure-coding" has a signature that does not verify over its content; withholding it: signature does not cover these bytes: signed by trent@example.com: verify: ssh: signature did not verify
    fix: re-pull the bundle, or investigate the source — its signature does not cover its bytes
```

</div>
<div class="ldc">

```gherkin
Then her assistant does not receive the altered guidance
```

</div>
<div class="ldc">

```text
# Example Fragment

Add your content here.

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
```

</div>
<div class="ldc">

```gherkin
And Alice is warned that the content's signature does not verify
```

</div>
<div class="ldc">



</div>
</div>

This is the case that separates a real signature check from a decorative one. A
trusted key genuinely signed the *original* bundle — but Mallory changed the
bytes afterward. A naive system that trusted the repository, or trusted a
remembered "this bundle is fine" verdict, would ship her edit. ctxloom re-derives
the exact bytes it is about to expose and checks the signature over *those*, so
the altered content fails verification.

Crucially, this is not J1's benign "held for your review." A missing signature is
quiet — content simply waits. A signature that is *present but does not verify* is
tampering, and it is refused **loudly**: Alice is warned that the content's
signature does not verify, because a broken signature on content that claims to be
signed is a security event, not a to-do item.

## A trusted company's MCP server and hook reach the assistant's configuration

Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.

- ✓ Trent's company publishes a "secure-coding" bundle, signed with the company key
- ✓ Alice trusts the company key
- ✓ the company's bundle ships an MCP server and a hook
- ✓ Alice starts a session
- ✓ the MCP server appears in her assistant's configuration
- ✓ the hook appears in her assistant's configuration

<div class="living-doc-grid">
<div class="ldc">

```gherkin
Given Trent's company publishes a "secure-coding" bundle, signed with the company key
```

</div>
<div class="ldc">

```text
Created profile "default" with bundles: seed
Saved to: /tmp/ctxloom-integration-2859684672/project/.ctxloom/profiles/default.yaml
```

</div>
<div class="ldc">

```gherkin
And Alice trusts the company key
```

</div>
<div class="ldc">

```text
Trusted trent@example.com for publish.v1.ctxloom.dev (SHA256:GCrYGG9HepAho4gYfrUlbFiQLSC7D7E5pt8PG6UZP5E) — wrote /tmp/ctxloom-integration-2859684672/project/.ctxloom/allowed_signers
```

</div>
<div class="ldc">

```gherkin
And the company's bundle ships an MCP server and a hook
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
When Alice starts a session
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
Then the MCP server appears in her assistant's configuration
```

</div>
<div class="ldc">

```text
{
  "mcpServers": {
    "ctxloom": {
      "_ctxloom": "ctxloom-auto",
      "args": [
        "mcp"
      ],
      "command": "/home/babbitt/workspace/worktrees/ctxloom--living-docs/ctxloom",
      "cwd": "${CLAUDE_PROJECT_DIR}"
    },
    "demo-server": {
      "_ctxloom": "bundle:file:///tmp/ctxloom-integration-2859684672/remote-3243944918/remote.git@bundles/secure-coding",
      "args": [
        "J3-MCP-EXEC-MARKER"
      ],
      "command": "/bin/echo"
    },
    "taskloom": {
      "_ctxloom": "bundle:ctxloom:companion@taskloom",
      "args": [
        "mcp"
      ],
      "command": "taskloom"
    }
  }
}
```

</div>
<div class="ldc">

```gherkin
And the hook appears in her assistant's configuration
```

</div>
<div class="ldc">

```text
{
  "hooks": {
    "PostToolUse": [
      {
        "hooks": [
          {
            "command": "ctxloom hook stamp-plan",
            "type": "command"
          }
        ],
        "matcher": "Edit|Write"
      }
    ],
    "PreToolUse": [
      {
        "hooks": [
          {
            "command": "ltk evaluate",
            "type": "command"
          }
        ],
        "matcher": "Bash|PowerShell|Edit|Write|MultiEdit|NotebookEdit"
      }
    ],
    "SessionStart": [
      {
        "hooks": [
          {
            "command": "ctxloom hook session-bind",
            "type": "command"
          },
          {
            "command": "echo J3-HOOK-EXEC-MARKER",
            "type": "command"
          }
        ]
      }
    ]
  },
  "statusLine": {
    "command": "/home/babbitt/workspace/worktrees/ctxloom--living-docs/ctxloom hook hud",
    "type": "command"
  }
}
```

</div>
</div>

Trust is not only about prose. A bundle can ship **executables** — MCP servers
the assistant can call, and hooks that run on events in the harness — and these
are the highest-stakes thing a bundle carries, because they run code. This
scenario proves the delivery side: a trusted publisher's MCP server and hook
reach the engine's *generated configuration*, not just its context. They pass
through a dedicated executable trust gate that makes the same decision, from the
same trusted-key signature, as the content gate does for prose — so trusting the
company to write your guidance and trusting it to wire an MCP server are one
decision, made deliberately, not two with different rigor.

## A rejected executable is withheld even from a trusted company

Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.

- ✓ Trent's company publishes a "secure-coding" bundle, signed with the company key
- ✓ Alice trusts the company key
- ✓ the company's bundle ships an MCP server and a hook
- ✓ Alice has rejected the hook
- ✓ Alice starts a session
- ✓ the MCP server still appears in her configuration
- ✓ the hook is absent, because she rejected it

<div class="living-doc-grid">
<div class="ldc">

```gherkin
Given Trent's company publishes a "secure-coding" bundle, signed with the company key
```

</div>
<div class="ldc">

```text
Created profile "default" with bundles: seed
Saved to: /tmp/ctxloom-integration-3978424675/project/.ctxloom/profiles/default.yaml
```

</div>
<div class="ldc">

```gherkin
And Alice trusts the company key
```

</div>
<div class="ldc">

```text
Trusted trent@example.com for publish.v1.ctxloom.dev (SHA256:T5Ox/cAGIVmlWEbB/ye3dxDsF78YsIaVSel9Fbzmuw8) — wrote /tmp/ctxloom-integration-3978424675/project/.ctxloom/allowed_signers
```

</div>
<div class="ldc">

```gherkin
And the company's bundle ships an MCP server and a hook
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
And Alice has rejected the hook
```

</div>
<div class="ldc">

```text
Rejected secure-coding#hooks/session_start/0
  repo:  file:///tmp/ctxloom-integration-3978424675/remote-1058734875/remote.git
  store: user
  UNSIGNED — recorded locally, not shareable (no signing key was available)
  ref block: recorded (sticky — survives content changes)
  content:   rejected in form(s) raw (blocks this content even if renamed/moved)
ctxloom: warning: withheld file:///tmp/ctxloom-integration-3978424675/remote-1058734875/remote.git@bundles/secure-coding#hooks/session_start/0: rejected
```

</div>
<div class="ldc">

```gherkin
When Alice starts a session
```

</div>
<div class="ldc">

```text
Materialized default → out (claude-code)
  wrote context
  wrote mcp
  wrote settings
  wrote skills
ctxloom: warning: withheld file:///tmp/ctxloom-integration-3978424675/remote-1058734875/remote.git@bundles/secure-coding#hooks/session_start/0: rejected
```

</div>
<div class="ldc">

```gherkin
Then the MCP server still appears in her configuration
```

</div>
<div class="ldc">

```text
{
  "mcpServers": {
    "ctxloom": {
      "_ctxloom": "ctxloom-auto",
      "args": [
        "mcp"
      ],
      "command": "/home/babbitt/workspace/worktrees/ctxloom--living-docs/ctxloom",
      "cwd": "${CLAUDE_PROJECT_DIR}"
    },
    "demo-server": {
      "_ctxloom": "bundle:file:///tmp/ctxloom-integration-3978424675/remote-1058734875/remote.git@bundles/secure-coding",
      "args": [
        "J3-MCP-EXEC-MARKER"
      ],
      "command": "/bin/echo"
    },
    "taskloom": {
      "_ctxloom": "bundle:ctxloom:companion@taskloom",
      "args": [
        "mcp"
      ],
      "command": "taskloom"
    }
  }
}
```

</div>
<div class="ldc">

```gherkin
And the hook is absent, because she rejected it
```

</div>
<div class="ldc">

```text
{
  "hooks": {
    "PostToolUse": [
      {
        "hooks": [
          {
            "command": "ctxloom hook stamp-plan",
            "type": "command"
          }
        ],
        "matcher": "Edit|Write"
      }
    ],
    "PreToolUse": [
      {
        "hooks": [
          {
            "command": "ltk evaluate",
            "type": "command"
          }
        ],
        "matcher": "Bash|PowerShell|Edit|Write|MultiEdit|NotebookEdit"
      }
    ],
    "SessionStart": [
      {
        "hooks": [
          {
            "command": "ctxloom hook session-bind",
            "type": "command"
          }
        ]
      }
    ]
  },
  "statusLine": {
    "command": "/home/babbitt/workspace/worktrees/ctxloom--living-docs/ctxloom hook hud",
    "type": "command"
  }
}
```

</div>
</div>

The counterweight to the previous scenario: a trusted signature is permission,
never obligation. Alice can reject a single executable — here, the hook — and her
rejection outranks the company's trusted signature on it. When she starts a
session, the MCP server (which she did not reject) still appears, but the hook is
absent because she rejected it. Rejection beating a trusted publisher, on the
very item with the most blast radius, is the structural expression of "signed
does not mean safe": a signature authenticates who wrote something; it never
overrides a human's refusal to run it.

## Trent retracts a bundle and it stops reaching engineers on the next sync

Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.

- ✓ Trent's company publishes a "secure-coding" bundle, signed with the company key
- ✓ Alice trusts the company key
- ✓ Alice already receives the company's secure-coding guidance
- ✓ Trent retracts that version of the bundle
- ✓ Alice syncs her project
- ✓ Alice is told the content was retracted
- ✓ her assistant no longer receives it

<div class="living-doc-grid">
<div class="ldc">

```gherkin
Given Trent's company publishes a "secure-coding" bundle, signed with the company key
```

</div>
<div class="ldc">

```text
Created profile "default" with bundles: seed
Saved to: /tmp/ctxloom-integration-2234869503/project/.ctxloom/profiles/default.yaml
```

</div>
<div class="ldc">

```gherkin
And Alice trusts the company key
```

</div>
<div class="ldc">

```text
Trusted trent@example.com for publish.v1.ctxloom.dev (SHA256:W9yLBuGJPfs+f1FVb/oJvjyzAKPJ/n8FhlDOJSaqRhk) — wrote /tmp/ctxloom-integration-2234869503/project/.ctxloom/allowed_signers
```

</div>
<div class="ldc">

```gherkin
And Alice already receives the company's secure-coding guidance
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

```text
# Example Fragment

Add your content here.

---

J3-COMPANY-SECURECODING-MARKER

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
```

</div>
<div class="ldc">

```gherkin
When Trent retracts that version of the bundle
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
And Alice syncs her project
```

</div>
<div class="ldc">

```text
Materialized default → out (claude-code)
  wrote context
  wrote mcp
  wrote settings
  wrote skills
ctxloom: warning: withheld file:///tmp/ctxloom-integration-2234869503/remote-565302010/remote.git@bundles/secure-coding#fragments/guidance: retracted by the publisher (found to be incorrect guidance; do not use)
```

</div>
<div class="ldc">

```gherkin
Then Alice is told the content was retracted
```

</div>
<div class="ldc">

```text
Pulling dependencies...

Pulled 1 items:
  Retracted: 1
    - file:///tmp/ctxloom-integration-2234869503/remote-565302010/remote.git@bundles/secure-coding: retracted (found to be incorrect guidance; do not use)
ctxloom: warning: withheld file:///tmp/ctxloom-integration-2234869503/remote-565302010/remote.git@bundles/secure-coding#fragments/guidance: retracted by the publisher (found to be incorrect guidance; do not use)
ctxloom: warning: withheld file:///tmp/ctxloom-integration-2234869503/remote-565302010/remote.git@bundles/secure-coding#fragments/guidance: retracted by the publisher (found to be incorrect guidance; do not use)
```

</div>
<div class="ldc">

```gherkin
And her assistant no longer receives it
```

</div>
<div class="ldc">

```text
# Example Fragment

Add your content here.

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
```

</div>
</div>

Publishers make mistakes, and the question is whether they can take one back. A
signature, once made, is valid forever — so revocation cannot mean "un-sign." It
means the publisher records that a specific version is withdrawn, and engineers
stop receiving it on their next sync, with a notice.

This is a **working guarantee**, and it is worth being precise about why, because
it was not always. Retraction is evaluated at exposure time against a *local*
record — ctxloom never dials the network to decide whether to show you content —
and that local record is now written both on a fresh pull and, the part that had
been missing, when re-syncing refs that were *already installed*. Previously
retraction had no effect on content that had already been distributed through any
CLI path: the very case that matters most. That product gap was fixed, and this
scenario is the proof that Trent's retraction now actually reaches an engineer who
already had the bundle — Alice is told the content was retracted, and her
assistant no longer receives it.

## When the company key is compromised, revoking it stops all of its content at once

Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.

- ✓ Trent's company publishes a "secure-coding" bundle, signed with the company key
- ✓ Alice trusts the company key
- ✓ Alice receives several bundles the company signed with its key
- ✓ the company key is compromised
- ✓ Alice revokes her trust in the company key
- ✓ Alice syncs her project
- ✓ her assistant no longer receives any content signed by that key
- ✓ that content is held for her review, as if it had never been signed

<div class="living-doc-grid">
<div class="ldc">

```gherkin
Given Trent's company publishes a "secure-coding" bundle, signed with the company key
```

</div>
<div class="ldc">

```text
Created profile "default" with bundles: seed
Saved to: /tmp/ctxloom-integration-669546259/project/.ctxloom/profiles/default.yaml
```

</div>
<div class="ldc">

```gherkin
And Alice trusts the company key
```

</div>
<div class="ldc">

```text
Trusted trent@example.com for publish.v1.ctxloom.dev (SHA256:hhPF/qyc+iyM/bm32373amMsKJMI/yhHcGKo/P4PetI) — wrote /tmp/ctxloom-integration-669546259/project/.ctxloom/allowed_signers
```

</div>
<div class="ldc">

```gherkin
And Alice receives several bundles the company signed with its key
```

</div>
<div class="ldc">

```text
Pulling dependencies...

Pulled 3 items:
  Installed: 2
  Skipped (already installed): 1
```

</div>
<div class="ldc">

```gherkin
When the company key is compromised
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
And Alice revokes her trust in the company key
```

</div>
<div class="ldc">

```text
removed 1 entry for trent@example.com from /tmp/ctxloom-integration-669546259/project/.ctxloom/allowed_signers
```

</div>
<div class="ldc">

```gherkin
And Alice syncs her project
```

</div>
<div class="ldc">

```text
Materialized default → out (claude-code)
  wrote context
  wrote mcp
  wrote settings
  wrote skills
ctxloom: warning: withheld file:///tmp/ctxloom-integration-669546259/remote-1589912223/remote.git@bundles/extra-a#fragments/guidance: awaiting review — run 'ctxloom review'
ctxloom: warning: withheld file:///tmp/ctxloom-integration-669546259/remote-2861520963/remote.git@bundles/extra-b#fragments/guidance: awaiting review — run 'ctxloom review'
ctxloom: warning: withheld file:///tmp/ctxloom-integration-669546259/remote-3857941511/remote.git@bundles/secure-coding#fragments/guidance: awaiting review — run 'ctxloom review'
```

</div>
<div class="ldc">

```gherkin
Then her assistant no longer receives any content signed by that key
```

</div>
<div class="ldc">

```text
# Example Fragment

Add your content here.

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
```

</div>
<div class="ldc">

```gherkin
And that content is held for her review, as if it had never been signed
```

</div>
<div class="ldc">

```text
3 item(s) pending review (0 update(s)):

file:///tmp/ctxloom-integration-669546259/remote-1589912223/remote.git@bundles/extra-a (remote: company-extra-a)
  new      fragments/guidance

file:///tmp/ctxloom-integration-669546259/remote-2861520963/remote.git@bundles/extra-b (remote: company-extra-b)
  new      fragments/guidance

file:///tmp/ctxloom-integration-669546259/remote-3857941511/remote.git@bundles/secure-coding (remote: company)
  new      fragments/guidance

Run 'ctxloom review' in a terminal to review interactively, or use the
plumbing per item: ctxloom trust <bundle-ref>#<kind>/<name> / ctxloom blacklist <ref>.
```

</div>
</div>

Retracting one version is the routine case; a stolen key is the emergency. If the
company's signing key is compromised, retracting bundles one at a time is far too
slow — the attacker can sign anything. Revoking trust in the *key* is the blunt,
correct instrument: it invalidates everything that key ever signed, in one move.

What happens to that content is the elegant part. It does not error or vanish
into a special "revoked" limbo; it simply falls back to the path any unsigned,
untrusted content takes — held for Alice's review, *as if it had never been
signed*. The signature stops counting the instant the key is no longer trusted,
and the content lands exactly where content from a stranger would. One withdrawal
of trust, and every bundle that key signed is back behind the gate at once.

## Recording a team-wide review decision requires a signing key

Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.

- ✓ Trent's company publishes a "secure-coding" bundle, signed with the company key
- ✓ Alice trusts the company key
- ✓ Alice has no signing key available
- ✓ Alice tries to record a review decision into the team's shared store
- ✓ ctxloom refuses, because a team decision must be signed
- ✓ nothing is written to the team store

<div class="living-doc-grid">
<div class="ldc">

```gherkin
Given Trent's company publishes a "secure-coding" bundle, signed with the company key
```

</div>
<div class="ldc">

```text
Created profile "default" with bundles: seed
Saved to: /tmp/ctxloom-integration-3440954141/project/.ctxloom/profiles/default.yaml
```

</div>
<div class="ldc">

```gherkin
And Alice trusts the company key
```

</div>
<div class="ldc">

```text
Trusted trent@example.com for publish.v1.ctxloom.dev (SHA256:iQ6DfAFO++VQ0gH2KVc8rkLB4hFo8djSAGfkA9KKhR8) — wrote /tmp/ctxloom-integration-3440954141/project/.ctxloom/allowed_signers
```

</div>
<div class="ldc">

```gherkin
And Alice has no signing key available
```

</div>
<div class="ldc">

```text
Pulling dependencies...

Pulled 1 items:
  Installed: 1
ctxloom: warning: withheld file:///tmp/ctxloom-integration-3440954141/remote-3276345022/remote.git@bundles/bystander#fragments/guidance: awaiting review — run 'ctxloom review'
ctxloom: warning: withheld file:///tmp/ctxloom-integration-3440954141/remote-3276345022/remote.git@bundles/bystander#fragments/guidance: awaiting review — run 'ctxloom review'
```

</div>
<div class="ldc">

```gherkin
When Alice tries to record a review decision into the team's shared store
```

</div>
<div class="ldc">

```text
Error: no signing key available (ctxloom: cannot sign — no signing key found.

  Looked for: git config user.signingkey, then ssh-agent identities.
  SSH_AUTH_SOCK is not set — no ssh-agent to sign with

  ssh-add ~/.ssh/id_ed25519            # load a key you already have
  ssh-keygen -t ed25519-sk             # or a hardware key (recommended)

Publishing unsigned means every user of this bundle must review it by hand.
To publish unsigned anyway: ctxloom fragment push my-frag --no-sign
) — 'ctxloom review --project' requires one; run 'ssh-add ~/.ssh/id_ed25519' and try again, or review without --project
```

</div>
<div class="ldc">

```gherkin
Then ctxloom refuses, because a team decision must be signed
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
And nothing is written to the team store
```

</div>
<div class="ldc">



</div>
</div>

The last scenario guards the trust system against forging *its own records*. A
decision Alice records only for herself can fall back to an unsigned marker — it
is her call, on her machine. But a decision written into the **committable,
team-inherited store** — the one a teammate or CI will inherit as "the team
approved this" — must be signed, because an unsigned team decision is a decision
anyone who can write a file could forge. So with no signing key available, ctxloom
does not degrade to an unsigned team record; it **refuses**, and nothing is
written. A team-wide "we approved this" that cannot be attributed to a real key is
not a weaker approval — it is not an approval at all.

Taken together, J3 is the enforcement half of the trust model: not "we have a
standard" but "the standard came from who we trust, unchanged, only what we
allowed — and we can pull it back." It builds on the first-party trust of
[J2](/journeys/j2-team-authoring/) and the add-and-review flow of
[J1](/journeys/j1-setup/), and it is the concrete exercise of the model laid out
in [Trust states and the gate](/security/trust-states/), the
[threat model](/security/threat-model/) (including what ctxloom explicitly does
*not* defend against), and [Key management](/security/key-management/).
