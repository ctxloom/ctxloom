# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args. `library-tree-plan-rejected`
# has no message: a plan's rejection is told in its own diagnostic's words
# (L14).

# Step 10a (texts.md §1): accepted verbatim (owner, 2026-11-16, night;
# reviewed before release, `connected-boil`).

# Step 10b (its texts.md §1): accepted verbatim with 10b's red spec (owner,
# 2026-11-16, night); raised once 10b sends the commands.
library-tree-plan-stale = { $path } changed after this change was planned (it was at change { $seen } of this library's record, it is now at change { $now }), so nothing was written
library-tree-plan-stale-fix = look again at what is there now, then run the command again
library-tree-resource-exists = { $path } already exists, so nothing was created over it
library-tree-resource-exists-fix = choose another name, or remove the one there first
library-tree-no-such-resource = there is nothing at { $path } to change
library-tree-builtin-read-only = { $bundle } is ctxloom's built-in bundle, and it is read-only
library-tree-builtin-read-only-fix = to change it, override it with a bundle of your own: `ctxloom bundle export { $bundle } <bundles dir>`
library-tree-one-change-per-call = this would change { $count } files ({ $paths }); ctxloom makes one change per call
library-tree-one-change-per-call-fix = run one command for each, one item at a time
# Step 10b (texts.md §1, the trash; G12): the daemon's own refusals, never a
# person's command. Accepted verbatim (owner, 2026-11-16, night; reviewed
# before release, `connected-boil`).
library-tree-no-such-slot = nothing is in the trash under { $request_id }
library-tree-slot-not-due = the trash under { $request_id } is { $age_days } days old, and is kept for 7
