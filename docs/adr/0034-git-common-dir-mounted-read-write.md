# 0034 — The git common dir is mounted read-write into containers

## Status

Accepted

## Context

A linked git worktree's `.git` is not a directory but a pointer file naming
`<common>/worktrees/<name>` — a path outside the worktree. A container given
only the worktree therefore has no resolvable git: every git call fails with
`fatal: not a git repository`, exit 128.

This affects two independent call sites:

- `isolation.gitCommonDirMount`, for container-worktree agent runs;
- the `_run` recipe, which drives the devcontainer for builds and gates.

The second had no such mount at all, so container gates and builds silently
misbehaved from a worktree. Notably the failure was not loud: the version
stamper consumed git's failure as an empty string and emitted a well-formed
stamp with no commit hash, so builds SUCCEEDED producing binaries that could not
say what they were built from.

Three options were weighed:

1. **Whole common dir, read-write, at its identical absolute path.** Simplest
   mount that works, because the per-worktree admin dir a linked checkout needs
   is a subdirectory of the common dir. Identical-path is forced: the `.git`
   pointer is absolute.
2. **A surgical partial mount** — the worktree's own admin dir plus read-only
   objects and refs. Narrower exposure, but git needs write access to refs,
   logs, and the packed-refs/objects layout in ways that make a partial mount
   fragile and easy to get subtly wrong.
3. **Read-only.** Sufficient for pure reads such as `rev-parse` and `describe`,
   and verified to work for those, but it breaks linked-worktree container runs
   generally for the write reasons above.

Measured on the deciding machine at decision time: 61 worktrees shared one
common dir, `.git/hooks` held live `pre-commit`, `prepare-commit-msg` and
`pre-push` hooks, and all remotes were SSH.

## Decision

Mount the whole git common dir **read-write, at its identical absolute path**,
at both call sites. Accept the resulting exposure rather than narrowing it.

`isolation.gitCommonDirMount` owns the rationale. Other sites cite it; they do
not restate it. A rule copied into several places is the expensive kind of
documentation debt — one copy gets retired and the others keep asserting it.

## Consequences

The exposure is real and is accepted knowingly, not overlooked:

- A container can reach every other worktree's admin dir and the main
  checkout's refs, objects, and index. The blast radius is the repository, not
  the container's own tree.
- `.git/hooks` is writable, and hooks execute **on the host** at the next git
  operation in any worktree. This is the sharpest edge of the accepted risk.
- The reflog is writable, which is the recovery path the worktree lifecycle
  depends on.

Two fears that do **not** apply, recorded so they are not re-litigated:

- Uncommitted work is unaffected. It lives in the worktree directory, not the
  common dir.
- No credential is exposed by the mount where remotes are SSH; there is no
  embedded token to read or rewrite.

This is consistent rather than permissive: a main-checkout container run already
mounts `.git` read-write, because `.git` sits inside the directory that becomes
the workspace mount. Read-only for worktrees alone would have made the worktree
path stricter than the ordinary path — an inconsistency, not a principle.

`TestGitCommonDirMount_WholeCommonDirReadWrite` pins the posture in both
directions: read-only breaks linked-worktree container runs, and a non-identical
mount path breaks the `gitdir:` pointer. Any future narrowing must therefore be
deliberate; it cannot happen accidentally in a sweep.

**Revisit trigger:** per-agent git isolation becoming a requirement, or agents
ceasing to be trusted by construction. Either flips this back to Proposed for
re-discussion, at which point option 2 above is the starting point.

One implementation trap worth carrying, because it fails silently: Docker's
`-v src:dst:ro` form mis-parses a destination ending in `.git`, landing the bind
at a truncated path while reporting success, so the mount appears to exist and
`.git` is still absent. Use `--mount type=bind,src=…,dst=…`, which takes named
keys and has no colon ambiguity.
