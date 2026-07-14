---
title: "A team lead shares a skill with the team"
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

A team's standards are only real if every teammate's assistant actually follows
them. Writing the convention down in a wiki does nothing for the assistant that
never reads the wiki; pasting it into one engineer's prompt does nothing for the
other five. The standard that lives in one place and reaches everyone by hand is
the standard that quietly drifts, one copy at a time, until no two assistants
agree on how the team works.

ctxloom's answer is to make the standard part of the project itself. Carol, the
team lead, authors a skill or a fragment *in the repository* — the same repo
everyone already clones and pulls. When Bob pulls, his assistant gains it. No
one exports a prompt, no one re-pastes anything, and — this is the part that
separates J2 from every remote-source journey — no one reviews it. The project
is the team's own; content authored inside it is **first-party** and trusted on
that basis alone. There is nothing to sign and nothing to approve, because the
team already owns what it wrote (see [Review and trust](/concepts/review-and-trust/)
for the first-party exemption, and [A prompt is executable code](/security/prompts-are-code/)
for why *remote* content does not get that pass).

This journey proves the authoring loop end to end: a skill authored in-project
reaches a teammate untouched by review; verbose guidance distilled once is
delivered in its compact form; and an edit propagates so the new version
arrives and the stale one is gone — the case the whole suite exists to catch,
because "the change silently didn't arrive" is the failure that erodes trust in
the whole mechanism.

> A team's standards only help if they reach every engineer's assistant the same
> way. When the lead writes down how the team works — a commit convention, a
> review checklist, a house pattern — it should land in every teammate's
> assistant automatically, just by being part of the project. No one copies
> prompts around; authoring it once, in the project, is enough. And because it
> is the team's own project, it is trusted as first-party: no signing, no
> review — the team already owns what it wrote.

## Carol authors a skill and a teammate gains it

Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.

- ✓ Carol is working in the team's project
- ✓ Carol authors a "conventional-commits" skill
- ✓ she commits it to the project
- ✓ Bob pulls the project
- ✓ Bob's assistant can invoke the conventional-commits skill
- ✓ it reached him without any review, because the project is first-party

<div class="living-doc-grid">
<div class="ldc">

```gherkin
Carol is working in the team's project
```

</div>
<div class="ldc">

```text
Created profile "default" with bundles: team-standards
Saved to: /tmp/ctxloom-integration-2108150703/project/.ctxloom/profiles/default.yaml
```

</div>
<div class="ldc">

```gherkin
Carol authors a "conventional-commits" skill
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
she commits it to the project
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
Bob pulls the project
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
Bob's assistant can invoke the conventional-commits skill
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
J2-CONVENTIONAL-COMMITS-V1-USE-IMPERATIVE-MOOD-IN-THE-SUBJECT-LINE
```

</div>
<div class="ldc">

```gherkin
it reached him without any review, because the project is first-party
```

</div>
<div class="ldc">

```text
Nothing is pending review.
```

</div>
</div>

This is the core loop, stripped to its essentials. Carol writes a
`conventional-commits` skill into the team's project and commits it; Bob pulls;
Bob's assistant can invoke it. The load-bearing clause is the last one — *it
reached him without any review*. Compare this with J1, where a remote source's
content is held at the gate until a human approves it. Here there is no gate to
clear, because the content is local to a project the team owns: the trust
resolver's **local** rule allows first-party content outright, ahead of any
signing or approval check. The team's own repository is not a stranger handing
you a prompt; it is the team, and you already trust the team.

## Carol distills verbose guidance and teammates receive the compact form

Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.

- ✓ Carol has authored a verbose fragment in the project
- ✓ Carol distills the fragment
- ✓ she commits both forms
- ✓ Bob pulls the project with distilled context enabled
- ✓ Bob's assistant receives the distilled guidance
- ✓ it does not receive the verbose original

<div class="living-doc-grid">
<div class="ldc">

```gherkin
Carol has authored a verbose fragment in the project
```

</div>
<div class="ldc">

```text
Created profile "default" with bundles: team-standards
Saved to: /tmp/ctxloom-integration-2047290163/project/.ctxloom/profiles/default.yaml
```

</div>
<div class="ldc">

```gherkin
Carol distills the fragment
```

</div>
<div class="ldc">

```text
Distilled guidance (mock-model)
```

</div>
<div class="ldc">

```gherkin
she commits both forms
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
Bob pulls the project with distilled context enabled
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
Bob's assistant receives the distilled guidance
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
it does not receive the verbose original
```

</div>
<div class="ldc">



</div>
</div>

Standards want to be thorough; context windows want them short. Distillation
resolves that tension at authoring time rather than at read time: Carol writes
the full, explanatory version once, distills it into a compact form, and commits
**both**. A teammate running with distilled context enabled receives the compact
guidance; the verbose original stays in the repo as the human-readable source of
truth but never spends the teammate's tokens.

The captured evidence here exercises the real distill machinery — the bundle is
parsed, the distiller is invoked, the compact form is saved and content-hashed —
not a canned string. (Distillation is not fragment-only: the same
compact-form-served behavior holds for skills; a fragment is used here as the
common case.) For the mechanism itself, see [Distillation](/guides/distillation/).

## Carol changes a skill and the change reaches teammates, not the old version

Every step below actually ran against a real `ctxloom` binary; nothing here is hand-written.

- ✓ Bob's assistant already has Carol's conventional-commits skill
- ✓ Carol edits the skill
- ✓ she commits the change
- ✓ Bob pulls the project again
- ✓ Bob's assistant has the updated skill
- ✓ it no longer has the previous version

<div class="living-doc-grid">
<div class="ldc">

```gherkin
Bob's assistant already has Carol's conventional-commits skill
```

</div>
<div class="ldc">

```text
Created profile "default" with bundles: team-standards
Saved to: /tmp/ctxloom-integration-412830316/project/.ctxloom/profiles/default.yaml

Materialized default → out (claude-code)
  wrote context
  wrote mcp
  wrote settings
  wrote skills
```

</div>
<div class="ldc">

```gherkin
Carol edits the skill
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
she commits the change
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
Bob pulls the project again
```

</div>
<div class="ldc">



</div>
<div class="ldc">

```gherkin
Bob's assistant has the updated skill
```

</div>
<div class="ldc">

```text
J2-CONVENTIONAL-COMMITS-V2-INCLUDE-A-SCOPE-IN-THE-SUBJECT-LINE
```

</div>
<div class="ldc">

```gherkin
it no longer has the previous version
```

</div>
<div class="ldc">



</div>
</div>

Authoring once is the easy half; keeping everyone current is the half that
actually earns trust. A propagation mechanism that delivers the *new* content
but leaves the *old* content lingering somewhere is worse than none — the team
would be split between two versions of a standard and not know it. So this
scenario asserts both directions at once: after Carol edits the skill and Bob
pulls again, Bob's assistant has the updated version **and no longer has the
previous one**. The stale copy is not merely superseded in some index; it is
gone from what the assistant sees. This is the "does the new content actually
arrive, cleanly" case the suite is built around.

## Carol's own active assistant picks up the change she just made

*Tags: @future*

```gherkin
  Scenario: Carol's own active assistant picks up the change she just made
    Given Carol has an active session in the project
    When Carol edits a fragment
    Then her own assistant reflects the change without her copying anything
```

> **Not captured in this build.** This scenario was not exercised in the run that generated this page (for example, a `@live` scenario without credentials in this environment). The Gherkin below is still the live spec — just without a proof-of-passing run attached yet.

This scenario is deliberately **not** part of the green run — it is marked
`@future`, and you will see it below without captured evidence. Whether Carol's
*own already-running* assistant reflects an edit she just made, without any
restart, is engine-dependent: some engines live-reload their context, others fix
it at launch and need a fresh session (the same two-phase shape J1's restart
scenario makes explicit). Greening this honestly means pinning that behavior per
engine first, so it is tracked rather than faked. The Gherkin stays here as the
recorded intent; the empty proof is the honest state of it today.

J2 is the first-party half of the trust model: content the team authors in its
own project, trusted because the team owns it, propagating to every teammate
without ceremony. The moment content comes from *outside* that boundary — a
personal repo, a company repo, a stranger's bundle — the ceremony returns, and
that is the subject of the other journeys: [Setting up ctxloom on a
project](/journeys/j1-setup/) for adding and reviewing remote sources, and
[Skills my company has validated](/journeys/j3-corporate-signed/) for the signed,
company-published case.
