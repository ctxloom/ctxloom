---
title: "Trust states and the gate"
---

The failure you are trying to avoid is silent: a hook you never read, wired into your harness
by a bundle update you never saw, firing before every shell command. Nothing crashes. Nothing
warns. It just runs.

So the gate has exactly one bias, and it is not convenience: **if ctxloom cannot positively
justify exposing an item, it withholds it.** Unsigned, signed by a stranger, changed since
you approved it, or simply not evaluable — all of these land in the same place, and that
place is "the agent never sees it".

## Three states

Every remote item — fragment, command, MCP server, hook — is in exactly one of three states.
There is no fourth.

**pending** — never reviewed, or its content changed since a human approved it. Withheld from
the agent. This is the implicit state of anything with no valid countersignature over its
current bytes. Unsigned content lands here. So does content signed by a key you don't trust.

**approved** — a human countersigned *this exact content* with their own SSH key. The
signature **is** the approval record. There is no ledger row to forge: an approval you cannot
sign is not an approval. It binds the item's raw bytes and, when one exists, its distilled
bytes, as independent signatures — change either exposed form and that signature stops
verifying, and the item returns to pending.

**rejected** — a human declined it, also by countersigning. Two signatures: one against the
**ref** (sticky — it survives the content changing underneath) and one against the **content**
with the ref deliberately omitted, so a renamed or moved identical copy stays rejected
wherever it turns up.

Note what is *not* a state. "Signed" is not a state. A publisher signature is an **input** to
the decision, never a property of the item. A signed item whose key you do not trust is not a
fourth thing — it is pending.

## The decision function

One resolver owns every exposure decision. It is fed the exact **bytes** about to be exposed
— never a precomputed hash, because a hash can only be *compared* against a file that
anything can write, while bytes can be *verified*. First match wins. The default is withhold.

Before step 1 runs, the approvals stores must be readable. A store that was never created is
fine — that is a fresh project. A store that *exists* and cannot be read is a **fault, not an
empty set**: it might be hiding a rejection. On that fault every item is denied, including
local and builtin content, and a fatal trust-store finding is raised. Fix or remove the store,
then re-review.

1. **rejected** — a rejection covers this ref, or covers exactly these bytes → **deny**
2. **local** — authored in this project, any kind including executables → **allow**
3. **builtin** — shipped inside the binary → **allow**
4. **trusted signer** — a key you trust for the `publish` namespace signed exactly these
   bytes, verified at load, before any YAML parse → **allow**
5. **approved** — a countersignature from a key you trust for the `approve` namespace
   verifies over exactly these bytes, at this ref, in this form → **allow**
6. **otherwise** — pending → **deny**

### Rejection is step 1 for a reason

Rejection beats everything below it: the local exemption, the builtin exemption, a trusted
publisher, and ctxloom's own release key. You can reject a builtin. You can reject something
Trent signed. Step 1 is evaluated even when a publisher signature is absent or failed to
verify, because **a rejection is of bytes, not of provenance**.

This is the structural consequence of "signed does not mean safe". A signature authenticates;
it never authorizes. If signatures could outrank a human's refusal, the refusal would be
decorative.

### Builtins go through the gate

Bundles compiled into the binary are allowed by default — trusting the ctxloom binary trusts
what it ships — but they are allowed at their **own step, below rejection**, rather than
skipping the gate. They are routed through the same decision function as everything else,
under a synthetic `builtin:ctxloom` identity that is explicitly excluded from step 4 so it can
never be laundered into a "verified publisher". Builtins are deliberately **not** signed:
signing bytes embedded in the binary that verifies them is circular.

### A candidate signature is not a verdict

A content hash still exists in this system, but only as an **index** — the filename under
which a candidate countersignature is looked up, so the lookup is a directory glob rather
than a scan. Finding a file at the right index proves nothing. It must still cryptographically
verify against the reconstructed payload, from a key trusted for the right namespace, or the
item resolves pending. A hand-crafted file at the right filename is inert.

Every degradation in this system points the same way. A malformed `allowed_signers`, a
corrupted countersignature, a deleted store: all of them produce *fewer* trusted decisions —
more review — never more exposure.

## Where the gate is enforced

A denial is fail-closed and **silent to the agent**: the item is simply absent. Nothing about
withheld content is ever injected into agent context. You get one aggregate, content-free line
on stderr — `N item(s) awaiting review — run 'ctxloom review'`.

| Choke | Covers | On deny |
|---|---|---|
| Content gate | fragments, commands | absent from assembled context |
| Executable gate — MCP | bundle MCP servers | omitted from backend settings |
| Executable gate — hooks | bundle hooks | omitted from backend settings |
| Executable gate — command export | command slash-commands | not exported |
| Tooling collection | a bundle's `tooling` command | withheld from Containerfile proposals |
| Listing stamp | JSON listings | stamped `trusted: false` |

**Ungated by design:** profile *definitions*. A profile is orchestration — a list of what to
compose. Its constituent items still gate at their own chokes, and executables a profile
declares directly pass the same executable gate.

## What the gate does not decide

The lockfile grants nothing. It pins **which commit** of a bundle is installed and that is
all — it is dependency management, not a security surface. A pull of an unsigned, badly
signed, or rejected bundle **succeeds**; its content is withheld when it would be exposed.
Verification happens at the exposure choke, never at fetch or lock time.

`ctxloom remote upgrade` moves the lockfile to the newest commit each constraint allows, with
no gate at the lock layer. The changed content then fails to verify against the approval you
gave the old content, re-gates to pending, and is withheld until you review it — as a diff
against what you approved.

Templating cannot smuggle content past the gate either: the signed payload is the
**pre-substitution** bytes.

## Recording a decision

`ctxloom review` is the porcelain — see [Review and trust](/concepts/review-and-trust/) for
the ceremony. `ctxloom trust <ref>` and `ctxloom blacklist <ref>` are the scriptable plumbing
beneath it, writing the same countersignatures through the same path.

The normative account of all of the above — storage formats, identity rules, lifecycle, and
every known gap — lives in
[docs/trust-model.md](https://github.com/ctxloom/ctxloom/blob/main/docs/trust-model.md).

Next: [Key management](/security/key-management/).
