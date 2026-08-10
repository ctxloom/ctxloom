<!--
J001900 narration companion (j001900_diagnosis.feature) — FLOWS-UNIFIED.md's U5.

This file is prose, plus one table: the boundary verifier as this journey
actually MEASURED it, rather than as FLOWS-UNIFIED Appendix A.2 predicted it.
Three cells moved. Every claim below traces to a named scenario in the sibling
feature file; nothing is asserted from a document, including the document this
journey was written from.

Numbering note: FLOWS-UNIFIED calls this journey "proposed J001400". J001400 was taken
by bundle distribution before this was written, so it is J001900 here. Nothing was
renumbered.
-->

<!-- doc:intro -->
Every product that delivers content to somewhere else eventually gets this
support ticket, and it is always the same ticket: *it worked on Friday.* No
error, no red, no crash — the thing simply stopped arriving, and now somebody
has to work out which of eight hops between "a human wrote it down" and "the
model read it" quietly dropped it on the floor.

ctxloom's answer to that ticket is supposed to be a rule, and the rule is
strong: every stage boundary NAMES ITS INSPECTOR. Content is authored, then
packaged, then attested, then distributed, then admitted, then composed, then
delivered, then ingested — and at each of those seams there is meant to be one
command you can run that tells you, in words, whether the content made it
across and why not. A boundary that has no such command is not an untested
boundary. It is a defect, because it converts a one-minute question into a
day of bisecting by hand.

This journey walks the seams one at a time. Each scenario plants a cause at
exactly one hop and then asks the inspector that owns that hop to say so out
loud. That shape is the whole point: it is easy to write a diagnosis test that
proves content did not arrive, and worthless, because "did not arrive" is the
symptom the user already reported. The only assertion that helps is whether
the tool NAMED THE CAUSE.

Which is also why nothing here asserts an exit code. An inspector that exits 0
while naming nothing is precisely the failure under test — asserting "the
command succeeds" would be asserting the bug.
<!-- /doc:intro -->

## What the walk found

Six hops answer. Six do not. The three cells that moved against the
prediction are marked.

| Boundary | Predicted | Measured | The inspector's actual words |
|---|---|---|---|
| B1 authored → packaged | OK | **green** | `search` names no packaged item, and does not answer with silence |
| B2 packaged → attested | PARTIAL | **red** | nothing, anywhere, says "unsigned" |
| B3 attested → distributed | OK | **red** ← moved | `bundle list` renders a held bundle as an ordinary entry |
| B4 distributed → admitted | OK | **green** for "pending"; **red** for "by whom" |`1 item(s) pending review`, and no signer information at all |
| B5 admitted → composed | OK | **green**, all three inspectors | `profile show`, `agent show`, and `run --dry-run` ← moved |
| B6 composed → delivered | OK | **red** ← moved | `manage check` never mentions a materialized surface |
| B7 delivered → ingested | DEFECT | **red** | nothing, by design-so-far |
| M5 two machines | miss | **red** | `unknown flag: --compare` |

### B2 — the cause, and the decision it forced

Carol edited the runbook on Friday and did not re-sign it. On Monday the
revision is withheld. That much was expected.

What was not expected was what Alice was left with. A first pass recorded here
that she "keeps serving Friday's superseded copy" — but that was measured from
a sync that never advanced a pin at all, so the attestation boundary was never
reached. Once the sync genuinely advanced, the answer was the opposite and
much worse: she was left with **nothing**. The revision was refused as
tampered, and the copy that *did* verify went out of reach along with the pin
that named it. Silent capability loss, at exactly the moment a signature stops
verifying.

That is now fixed, by a decision rather than a discovery: `deps upgrade`
**refuses to advance a pin onto content whose publisher signature does not
verify**. The lockfile keeps the last commit that verified, Alice goes on
being served that content, and the sync says so out loud — naming the bundle,
the fact that the new bytes cannot be verified, and the pin it is keeping her
at. She is running slightly old, verified content, and she knows it. The
alternative, advancing and withholding, made the failure unrecoverable and
silent.

The scenario asserts each half separately, and deliberately: the pin holding,
the old content still arriving, and Alice being told are three different ways
this can go wrong, and a scenario checking only that the revised bytes were
absent would pass for all of them.

One detail is worth keeping, because it is this journey's own thesis turned on
the product: the sync used to end by pointing at `ctxloom review`, and
`ctxloom review` answered "Nothing is pending review." Content withheld as
tampered is *deliberately* never offered for review, so the only remedy the
product named was a circle. A remedy that cannot act is worse than none, and
the scenario now asserts against exactly that.

Then there is the trap, which has its own scenario because Alice will
absolutely fall into it. Her first instinct is `review --list`, and
`review --list` shows nothing — not because nothing is wrong, but because
*unsigned* is a different state from *pending*. The most obvious inspector at
this hop is silent by design, and any diagnosis that stops there stops in the
wrong place. That scenario's failure message says so, so that if
`review --list` ever does start naming the runbook, whoever sees the red
learns it is good news rather than a regression.

### B3 and B6 — two hops the boundary table credits with work they do not do

Both were predicted OK. Both are red, and for the same underlying reason: the
nominated inspector reports on something adjacent to the hop rather than on
the hop.

`ctxloom deps hold` genuinely works — the freeze is real, the older guidance
keeps arriving and the newer does not. But `bundle list` renders the held
runbook as an ordinary entry at `(v1.0.0)` and says nothing about the hold. A
deliberate freeze and a broken sync produce identical output. That distinction
is the entire job of this hop's inspector.

`manage check` reports the project path, whether MCP auto-registration and
the statusline are on, one line per engine, and which companion binaries it
found. It never mentions a materialized surface, so it cannot report one as
stale. It is an inspector of WIRING, not of DELIVERY. The fixture proves the
divergence is real before the inspector ever runs — it asserts the file on
disk holds last week's bytes and not this week's — so the red is a product
answer, not a harness artifact.

### B5 — the hop that moved the right way

FLOWS-UNIFIED's U5 arc lists `run --dry-run` as "absent". It is not: `-n`
renders the composed context, deploy guidance and all. B5 has three working
inspectors, not two. The document should be corrected; the scenario should
not.

It is kept green on purpose. A diagnosis walk with no working hops teaches
nothing about which hops are broken — the green ones are what make the reds
legible as gaps rather than as a suite that does not work.

### B7 — the question every real diagnosis ends on

Every inspector above can be green and the assistant can still be blind,
because nothing in ctxloom can tell a user whether the engine READ the file it
was handed. A vendor changing its surface format, a config key moving, an
engine silently ignoring a path — from here all three look identical, and all
three look identical to everything being fine.

This is the one that cannot be worked around by a careful operator. Every
other hop in this table can be checked by hand if you know enough: read the
lockfile, diff the signature, cat the materialized file. B7 cannot, because
the fact you need is inside a process ctxloom does not own. J000400's live table
proves ingestion in CI; it is not a tool anybody can run on a Monday morning.

The scenario probes `doctor`, `manage check`, `agent show` and
`session list`, and fails quoting all four, so the red is itself the record of
what the product says instead of an answer.

### M5 — where the ticket actually started

"It is reaching her assistant and not his." That is a comparison, and every
diagnostic ctxloom has is single-machine. Alice has the answer sitting in
front of her in two files and no way to ask the tool which one differs and
where. The scenario probes three plausible spellings and asserts only that
*something* answers, so whichever design eventually wins can satisfy it
without this file having pre-decided the flag.

<!-- doc:outro -->
The honest summary is that ctxloom is good at the hops where it owns both
sides of the seam and weak at the hops where it owns one. Authoring,
composition and admission — where the content is entirely inside ctxloom's
world — answer clearly. Attestation, distribution state, and delivery — where
the answer depends on correlating two things ctxloom holds separately — go
quiet. Ingestion, where the far side is a vendor process, has no answer at
all.

That is a coherent shape, and it points at a coherent fix: the missing
inspectors are almost all the same missing capability, which is comparing two
states ctxloom already stores and reporting the difference in words. Signed
bytes versus current bytes is B2. Locked version versus available version, and
by whose decision, is B3. Composed context versus materialized file is B6.
Her delivered context versus his is M5. Four gaps, one shape.

B7 is the exception and stays the hard one, because the second state lives in
somebody else's process.

Two of the twelve scenarios are still `@wip`, each carrying its own untag
condition in the feature file; the other ten pass. Every scenario here started
`@wip`, including the ones believed to pass, and that was deliberate: this file
is a to-do list meant to be walked one scenario at a time, and a scenario that
arrived already green would be indistinguishable from one nobody had looked at
yet. Untagging is the record that somebody looked — each of the ten names the
mutation it was confirmed to catch.
<!-- /doc:outro -->
