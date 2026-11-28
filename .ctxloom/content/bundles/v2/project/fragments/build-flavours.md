---
tags:
  - domain
  - build
  - purge
---
# Two builds: destructive and non-destructive

ctxloom ships TWO builds from one source tree, and so does every member of the family that can destroy data (angzarr's Purge API, taskloom's `prune`). Owner rulings, 2026-11-26 and 2026-11-27.

- **The non-destructive build** (the default, `ctxloom`) cannot delete or destroy.
  - The code that would is REMOVED AT COMPILE TIME, behind the cargo feature `purge`. It is absent, not merely disabled: no verb, no handler, no store call.
  - Its CLI does not know the destroy verbs (`session remove`, `session sweep`), so they are unknown commands.
  - Its daemon answers the destroy RPCs (`DeleteSession`, `SweepSessions`) with the generated UNIMPLEMENTED, meaning "unsupported here".
  - `scripts/plain-destroy-check.sh` holds the plain binary to this.
- **The destructive build** (`ctxloom-purge`, `--features purge`) has them.
  - `scripts/purge-present-check.sh` holds the purge binary to having them.
- **Both are built, tested and released.**

When you add anything that deletes or destroys (a verb, an RPC, a store purge, a file removal that loses recorded history), it goes behind `purge` in every crate it touches. The default build must still compile and pass without it. Never add a runtime switch that turns destruction on.

## Documentation is unified

There is ONE set of documentation for both builds. That covers help text, `docs/`, the README, CLAUDE.md, feature files and tool descriptions. Do not write a second set per build.

Every delete or destroy action documented there carries a disclaimer: it is unsupported on the non-destructive build and needs the destructive one. For example: "Not available in the default `ctxloom` build; this verb exists only in `ctxloom-purge`."

## Testing

- The full suite and the mutation run are built WITH `purge`, so purge code is fully tested and mutated.
- The non-destructive build is tested only on the purge diff. That means what proves the exclusion: the plain-build checks, the compile_fail tests, and the scenarios a plain build answers differently (`CTXLOOM_TEST_FLAVOUR=plain`).
