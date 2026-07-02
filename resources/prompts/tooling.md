You are folding bundle-declared tooling into this project's **agent container
image**. The declarations below were collected from TRUSTED bundles only (the
trust gate withholds unreviewed content), but a Containerfile edit is still a
code-execution surface: **every change needs the user's explicit approval —
show the exact diff before writing anything.**

## Do this

1. **Locate the editable base Containerfile.** If config has no
   `isolation_base_containerfile` yet, run `ctxloom container scaffold` — it
   materializes the embedded default base (the same one the default auto-build
   was already using) and wires it into config, so nothing changes until you
   edit. Then read the file.
2. **Propose the edits.** For each bundle declaration below, translate its
   required tools into concrete Containerfile additions (install layers,
   version pins where the declaration asks). Keep the file's existing
   structure — the engine's agent stage layers on top of this base, so only
   the base is yours to touch. Attribute each addition with a short comment
   naming the source bundle.
3. **Show the user the full diff and ask before writing.** Per-change
   approval, not a blanket yes. If the user declines a bundle's tooling, skip
   it — its content still works, just without in-image tools.
4. **Apply approved edits, then rebuild:** `ctxloom container build`. The
   default auto-build also picks the file up on the next containerized run.

## Never

- Never apply tooling automatically on pull/sync or without showing the diff.
- Never install from a declaration the user hasn't seen.
- Never edit anything other than the base Containerfile for this purpose.

## Bundle declarations
