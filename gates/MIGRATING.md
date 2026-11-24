# Changing the gates

These gates were the shared gates repository (`~/workspace/gates`), brought
into ctxloom at its `8c131cf` with their history, as taskloom and annals
brought in their own copies. They are ctxloom's own now: change them
here, and nothing here reaches the other repositories, nor anything there
this one. Carry a fix across by hand when more than one needs it.

## Moving a branch off the `.gates` link

A branch made before the gates came in has its justfile import
`.gates/gates.just` and its lefthook.yml extend `.gates/lefthook.yml`.
Rebasing it on main takes main's justfile, lefthook.yml and .gitignore;
then delete the checkout's `.gates` link (`rm .gates`), which nothing reads
any more and which .gitignore no longer hides. A branch's own change to the
`gates-link` or `gates-check` recipes or the `gates_dir` variable is to be
dropped in the rebase: they are gone, and `hooks-install` no longer depends
on `gates-link`.

## A new hook needs `lefthook install`

lefthook installs a hook only for the hooks its config names when it is run,
and does not notice new ones in an `extends` file. After a change to
`gates/lefthook.yml` that names a hook not installed yet, each clone runs,
once:

```sh
lefthook install          # or: just hooks-install
```

## The machine-wide test lock (2026-11-16)

Every whole-workspace test run takes one flock per user,
`${XDG_CACHE_HOME:-$HOME/.cache}/ctxloom/gates/workspace-tests.lock`
(`gates/tests-lock.sh`), so two agents' runs on one machine never overlap.
A repository copying it takes `gates/tests-lock.sh`, the `_tests` and
`_test-workspace` changes in `gates/gates.just` (`test` and
`test-staged`'s workspace leg now go through `_test-workspace`), its
`scripts/ratchet-base.sh` re-run under the lock if it has that script,
the copy of `tests-lock.sh` into each fixture self-test that copies
`memory-cap.sh` (with `TESTS_LOCK_FILE` set to a file of the fixture's), and
`scripts/tests-lock.selftest.sh` into its `just selftest`. Keep the lock's
path as it is: the memory the lock guards is the machine's, so taskloom's
and annals' runs must take the same file as ctxloom's.

## No sccache; the age sweep is not a gate (2026-11-16)

The sccache export block of `gates/gates.just` (with `CARGO_INCREMENTAL=0`,
`gates/sccache.sh` and the `sccache_*` parameters) is gone again (owner
ruling of 2026-11-16: released sccache cannot share a workspace crate's
compile between worktree paths). A repository that copied it drops the
block, the script, the parameters (its fixture justfiles' too), its
self-test and CI's `CTXLOOM_SCCACHE` opt-out. ctxloom's age sweep (`just trim-targets`,
`scripts/trim-targets.sh`, over the pinned cargo-sweep) is the
repository's own, not a gate: nothing in `gates/` calls it. A repository
that wants it copies the script and its self-test, the
`cargo_sweep_version` parameter and the recipe.

## The commit-msg hook (owner ruling 170)

`gates/lefthook.yml` names a `commit-msg` hook: `just commit-msg-check
<file>` refuses a message carrying a `Co-Authored-By:` trailer, in any
case, on any line. It is a hook lefthook has not installed in a clone made
before it, so each clone runs `just hooks-install` once more (worktrees
share the hooks, so once per clone covers them all). A repository copying
it takes the `commit-msg-check`, `_no-trailers` and `_extract-trailers`
recipes, the hook's block, and `scripts/commit-msg.selftest.sh` into its
`just selftest`.

## Red tests on branches

The gates allow a commit with failing tests on any branch but the default
one (warned `RED  tests: ... cannot merge into <default> until they pass`),
refuse it on the default branch, and keep red tests out of it with three
hooks: pre-merge-commit (a merge commit is tested as a commit is), pre-push
(a push to the remote's default branch runs the whole workspace's tests on
the pushed commit) and post-merge (a fast-forward of the default branch to
local work, which no hook can refuse, is tested just after and the way back
named). With a remote, record its default branch (`git remote set-head
origin --auto`): without origin/HEAD the default branch is the first of
init.defaultBranch, main and master that is a branch.

## The mutation verdict is a recipe, and takes no feature rule

`scripts/mutants-judge.sh` is gone (owner ruling 99): the verdict is the
private recipe `_mutants-verdict <out> <rc> [thresholds]` in `gates.just`,
inline jq over cargo-mutants' `outcomes.json` and its exit code, with no log
read (the old check that an unviable mutant's build log held a compiler
error is dropped). `mutants-staged` and `_mutants-judge` now refuse a
non-empty `feature_rules` (`_mutants-no-features`), and pass no feature to
cargo mutants; `features_prepare` runs before the tests alone. A repository
copying this change deletes its `scripts/mutants-judge.sh`, replaces its
self-test with `scripts/mutants-verdict.selftest.sh` in `just selftest`, and
moves any feature rule into the crate itself: a dev-dependency on itself
with the feature on (`<crate> = { path = ".", features = ["<feature>"] }`).

## One test runner: nextest, mutation included (owner ruling 144)

- `.cargo/mutants.toml` sets `test_tool = "nextest"` and
  `additional_cargo_test_args = ["--no-tests=pass"]` (nextest exits 4 for a
  crate with no tests, which cargo-mutants counts as every mutant caught).
- A committed `.config/nextest.toml`, listed in `workspace_wide`: retries 0,
  `slow-timeout` with `terminate-after`, `leak-timeout` with `result =
  "fail"`, a `max-threads = 1` test group for the tests that bind ports.
- A test that builds a program for itself (a `OnceLock` around `cargo
  build`) builds it once per test PROCESS under nextest: build it in a
  nextest setup script (`experimental = ["setup-scripts"]`) that writes its
  path to `$NEXTEST_ENV`, and take that path in the test.
- `_tests` no longer passes nextest's exit code through (its 102 is "cargo
  metadata failed", which `test-commit` took as red tests): it exits 0, 100
  (a test failed), 101 (not built), 120 (a doc test failed, was 102) or 121
  (nextest failed otherwise). `test-commit` takes 100 and 120 as red. A
  repository copying this change replaces its `_tests`, `test-commit` and
  `_extract-tests`, and adds `scripts/tests-exit.selftest.sh` to `just
  selftest`.

## The `red_judge` parameter

`test-commit` asks the justfile's `red_judge` recipe, given the tests' exit
code, before it lets red tests be committed off the default branch, and its
failure refuses the commit. Every justfile importing these gates defines it
(`red_judge := ""` for none, which lets every red commit through as before).
ctxloom's is `""` (owner ruling of 2026-11-13): its `_red-judge`
(`scripts/red-commit-judge.sh`, no scenario of a feature file the commit
does not touch may turn red) is gone, and the landing gate's ratchet
(`just land-ratchet <base>`) judges red scenarios instead.

## `test-staged` tests the changed crates alone (owner ruling of 2026-11-13)

`test-staged` runs the changed crates' OWN tests, no dependent's, so a
library's change no longer runs the binary's acceptance suite at commit.
`test-staged dependents` adds the workspace dependents, and `test-commit`
asks it on the default branch (and wherever the branch cannot be told), so
red still cannot be committed there. `_test-affected` takes the scope. A
staged `workspace_wide` file tests the whole workspace as before. No hook
is added or removed, so no `hooks-install` is needed. A repository copying
the change takes the `test-staged`, `test-commit` and `_test-affected`
hunks, `scripts/test-staged.selftest.sh` and `scripts/tests-exit.selftest.sh`.

## The plain-binary gate (2026-11-18)

pre-commit gains `plain-binary` after clippy (owner ruling 2026-11-18,
evening): `just plain-binary` builds the workspace as `just build` does and
runs the repository's plain-build checks over the binary it made, under
`_run` (one `ok plain-binary` line; a failure's first line is the failing
check's summary, its log `target/logs/plain-binary.log`). The recipe and its
checks are ctxloom's own (its justfile, `scripts/plain-engines-check.sh` and
`scripts/plain-destroy-check.sh`), not the gates': a repository copying
`gates/lefthook.yml` drops the `plain-binary` command or gives its justfile
a recipe of that name. It is a command of the existing pre-commit hook, so
no `lefthook install` is needed.

## The pre-commit clippy lints the staged tree (owner ruling 2026-11-18, night)

pre-commit's `clippy` runs `just lint-staged`, which runs the repository's
`lint` inside the throwaway staged worktree `_staged-tree` makes (HEAD plus
`git diff --cached`), with test-staged's target dir (`target/staged`), so
an unstaged edit can neither pass nor fail it. Its output is `lint`'s own
terse line (logs in this checkout's `target/logs`, `GATE_LOG_DIR`). It is
the same command of the existing pre-commit hook, so no `lefthook install`
is needed. A repository copying the change takes the `lint-staged` recipe
and the `clippy` command's `run:`; its `lint` recipe must work in a
checkout of its own tree (it is run there).


## One staged target for every checkout (ctxloom, 2026-11-23)

`_scratch-tree` (so `_staged-tree` and `_commit-tree`) no longer builds into
the checkout's `target/staged` under the checkout's `target/.staged-tree.lock`.
Every checkout and worktree of the user builds into ONE target,
`<cache>/staged-target`, under ONE lock, `<cache>/staged-tree.lock`, where
`<cache>` is `$GATES_STAGED_CACHE` (a seam for self-tests), else
`${XDG_CACHE_HOME:-<account home>/.cache}/ctxloom/gates` (account home from
`getent passwd`, not `$HOME`, which a session may relocate). A gate that finds
the lock held prints `wait staged-tree: ...` (lock path, holder's pid, source,
checkout) once and waits; the holder writes that note into the lock file
(opened `>>`, cleared on exit). Change: the `_scratch-tree` hunk of
`gates/gates.just` (header comment, cache resolution, lock, `CARGO_TARGET_DIR`)
and the doc comment of `lint-staged`. Self-tests: `scripts/scratch-tree-lock.selftest.sh`
(path, no per-checkout lock, a second checkout waits and says so) and
`scripts/test-staged.selftest.sh` (both export `GATES_STAGED_CACHE`; any
self-test that runs a staged recipe must). Replace `<cache>/ctxloom/gates`
with the repository's own directory name when porting, if it should not share
ctxloom's.
