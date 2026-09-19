# Companion loadout standard

A **companion** is a standalone binary — `taskloom`, `ltk` — that ctxloom discovers on
PATH and that contributes content, tools and hooks to a session without any ctxloom code
change. This is the contract between the two: what a companion must emit, how ctxloom
asks for it, and what each side may assume.

It is a standalone document because the contract is a CROSS-PROCESS one. It was previously
stated in four places — `internal/shared/companionloadout`'s package doc, each companion's
own `loadout.yaml` header, `docs/signature-envelope.spec.md` §4.3, and
`internal/core/config/companions.go` — and a contract asserted in four places with nothing
reconciling them is how the two sides drift.

## The probe

ctxloom execs the companion at boot:

    <bin> loadout --format json

The three strings are a wire contract and are exported from
`internal/shared/companionloadout` (`Subcommand`, `FormatFlag`, `FormatJSON`) so BOTH
sides build the argv from one declaration. This is not tidiness. They were once bare
literals on both sides with no shared constant and no test exercising the real pair, and
because a broken probe took a silent bare-return path, renaming either side alone passed
the entire suite while removing ALL companion contribution in production.

A second, separate probe — `<bin> version --format json` — is owned by
`internal/shared/cliversion`, and is the same implementation the agent-image version key
reads. Two probes disagreeing about a companion's version surfaces as an image that never
rebuilds.

## What the companion emits

A `bundles.Bundle` document: the same shape a remote bundle takes, seeded into the trust
gate under `ctxloom:companion@<name>`, and taking the same review path when unsigned. It
carries whatever a bundle carries — fragments, commands, skills, MCP servers, hooks.

Each companion OWNS its loadout and embeds it (`go:embed` cannot reach outside the
embedding file's package, so the file lives beside the binary's own source, with its
detached signature alongside). The loadout is the single source of truth for what a
companion tells ctxloom about itself; ctxloom ships no built-in copy.

## Signing

The signed artifact is the pair `(content bytes, detached signature)` — the same rule
every other channel follows. The loadout is the one surface where the EMITTER controls the
bytes, which is why it travels as a JSON envelope rather than a sibling file
(`signature-envelope.spec.md` §4.3).

Editing a loadout means re-signing it. `just sign-loadouts` produces the signature, and the
`loadout-signatures` pre-commit hook refuses a loadout whose signature is missing from the
index — a loadout without its signature is withheld, not shipped unsigned.

## Failure is a warning, never a stall

The probe is time-bounded (`companionProbeTimeout`), and a wedged companion degrades to a
warning rather than blocking startup. A second bound (`companionProbeWaitDelay`) covers a
companion that spawned a grandchild inheriting stdout, which would otherwise hold the pipe
open past the child's death and stall boot forever despite the kill.

The failure that must NOT be tolerated is the quiet one: a companion contributing nothing
because a probe broke looks identical to a companion that legitimately contributes nothing.
`CompanionStatus.Executed` exists so callers stop inferring that from an empty version.

## Conditional loadout content

A loadout fragment MAY declare a `premise`, and ctxloom honours it exactly as it honours
one on any other fragment. Nothing about the loadout format, the probe or the envelope
changes to allow this — a loadout IS a bundle, and `premise` has always been part of a
bundle fragment.

A fragment WITHOUT a premise is unconditional, which is what makes this additive: every
loadout that has never heard of premises keeps behaving as it did.

A fragment WITH one is withheld from unconditional context, offered in the premise index
(`ctxloom fragment premises`) under its companion ref
(`ctxloom:companion@<name>#fragments/<frag>`), and materialized as a native skill package
where the engine has that surface and ctxloom will not be present to serve it. See
`docs/architecture/core/premise-selection.md` for which of those paths is preferred and why.

**Write the premise as an applicability condition, not a capability.** "You are about to
create, read or close a task" is a premise; "manages your tasks" is a description of the
tool. The agent matches it against what it is ABOUT TO DO.

Measured when taskloom adopted one: the unconditional companion floor under every profile
fell from 14,323 bytes to 3,475, and this project's coordinator context from 53,576 to
42,728. Both by 10,848, matching taskloom's separately measured section size. `ltk`
declares no premise and is still delivered unconditionally.

This is worth doing because a loadout is otherwise the ONE class of content progressive
disclosure cannot reach: it is not profile-selected, so nothing else gates it, and it sits
in every session regardless of relevance.
