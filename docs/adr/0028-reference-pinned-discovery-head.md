# 0028 — Referenced content is read at its pinned SHA; discovery reads HEAD

**Date:** 2026-06-02.

## Status

Accepted.

**Updated by [0033](0033-version-constraint-dependency-model.md):** referenced content is still read at a pinned commit, but that commit now lives in the **lockfile** as the resolution of the ref's version *constraint* — it is no longer carried in the manifest ref itself.

## Context

ctxloom is a package manager for context. A profile *references* bundles (and
parent profiles); those references are pinned to a commit SHA in
`.ctxloom/lock.yaml`, and the pinned content is read out of a local full clone of
the remote (`.ctxloom/cache/repos/<host>/<owner>/<repo>`). Remote bundles are
never extracted to on-disk YAML under `.ctxloom/bundles`; the read-path loader is
*seeded* from the lockfile and reads each bundle's bytes from the clone's object
store at its pinned SHA.

Two different intents both look like "read a bundle," and they want different
versions:

- **Use what I depend on**, reproducibly: assembling context for a run, listing
  the fragments my profiles can pull, showing a bundle I've installed. These
  should be stable — the version I locked.
- **Discover what's available**: searching the library, browsing a remote's
  catalog to find something to install. These should be current — the latest on
  the remote.

Without a rule, the same operation could read either version, producing
incoherence in both directions: a `list` pinned to old SHAs would hide fragments
that exist upstream and that I could install, while a `list` at HEAD would show
fragments that assembly (pinned) will not actually use.

## Decision

**The lens is chosen by whether the operation touches a reference or discovers
something not yet referenced.**

> A *reference* — a declared dependency, or the content reachable through one —
> is read at its **pinned SHA**.
> *Discovery* of content not yet referenced is read at the remote's **HEAD** (the
> local clone's HEAD, as of the last `remote update`).

This splits "looking at bundles" cleanly: looking at *the library* (to find
something) is HEAD; looking at *the bundles you reference* is pinned. They are
not in tension.

Surface classification:

| Surface | Reference or discovery | Lens |
| --- | --- | --- |
| `run` context assembly (profile → bundles) | your references | pinned |
| `run -f <frag>` / `-r <saved-prompt>` | a reference | pinned |
| `fragment list` / `prompt list`, completion | items of referenced + local bundles | pinned |
| `bundle list`, `fragment/prompt/bundle show` | your referenced/installed content | pinned |
| `search_library`, browse a remote catalog | un-referenced content | HEAD |
| `bundle/profile install <remote>/<x>` | *creates* a reference | resolve at HEAD, then pin |
| `remote upgrade` (ADR [0027](0027-mirror-established-tool-naming.md)) | advance references | HEAD → pin |

Supporting mechanics this rule depends on:

- **Clones are full and eager.** `remote add` clones the remote at add time
  (friction up front), with full history — not a shallow `Depth:1` clone, because
  reading a bundle at an arbitrary historical SHA requires every commit to be
  present. `remote update` refreshes the clones; the clone is otherwise static.
- **Pinned retrieval reads the object at the SHA**, not a working-tree checkout
  *to* the SHA. One clone (working tree at default-branch HEAD) serves every pin
  by reading `<SHA>:path` from the object store, so multiple bundles in one repo
  pinned at different SHAs are all readable without checkout thrashing.
- **apt-style verbs** (ADR 0027): `remote update` refreshes the clones (the
  index); `remote upgrade` advances pins to current HEAD (re-pin / "upgrade the
  installed set").

Local, hand-authored bundles under `.ctxloom/bundles` sit outside both lenses —
they have no remote and are always read directly from disk.

## Consequences

- `fragment list` / `prompt list` / completion / `show` read the pinned (seeded)
  view — they reflect what assembly will actually use, not upstream drift. This
  is the load-bearing call: those are "your referenced bundles," not the library.
- Discovery (`search_library`, catalog browse) reads HEAD, so newly published
  upstream content is findable and installable even though nothing is pinned to
  it yet.
- `install` resolves the ref at HEAD then writes the SHA, so the moment something
  becomes a reference it is pinned; advancing pins is the explicit `remote
  upgrade` step, never implicit.
- `list` can legitimately differ from what is newest upstream. That is intended:
  it shows your locked set, and `remote update` + `remote upgrade` is the path to
  move it forward. Help text and command names carry the apt analogy so this is
  unsurprising.
- The read-path loader stays a single seeded (pinned) loader for referenced
  content; discovery is a separate HEAD-reading path. The two are not merged —
  conflating them is exactly the incoherence this ADR exists to prevent.

**Revive trigger:** if a surface starts to feel mis-lensed — e.g. users expect
`fragment list` to show what is installable across remotes (discovery) rather
than what they reference, or a discovery surface is found reading pinned content
— reclassify that one surface against the reference-vs-discovery test rather than
abandoning the rule. The split itself only fails if "reference" and "discovery"
stop being distinguishable for an operation.
