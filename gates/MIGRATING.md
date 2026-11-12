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
