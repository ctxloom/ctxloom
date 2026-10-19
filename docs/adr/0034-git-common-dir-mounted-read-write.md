# 0034 — The git common dir is mounted read-write into containers

## Status

Accepted

## Context

A linked git worktree's `.git` is not a directory but a pointer file naming
`<common>/worktrees/<name>` — a path outside the worktree. A container given
only the worktree therefore has no resolvable git: every git call fails with
`fatal: not a git repository`, exit 128.

This affects two independent call sites:

- `isolation.gitDirMounts`, for container agent runs on a linked checkout;
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
at both call sites. Accept the resulting exposure rather than narrowing it,
with one exception at both: the `worktrees/` registry is masked by an empty
read-only directory, and the checkout's own admin dir is mounted back into it.
The registry is the part of the common dir that belongs to OTHER checkouts and
that git acts on by itself: `git worktree prune`, and gc's automatic prune,
delete every registration whose checkout is not visible, and no other checkout
is mounted into either container.

A second exception at both (ruled 2026-10-06): the common dir's `hooks/`
directory and the main checkout's `index` and `HEAD` are bound **read-only**
over themselves, shadowing the read-write mount. Hooks run on the host at the
next git operation in any checkout; the index and HEAD belong to the main
checkout, while a linked checkout keeps its own in its admin dir, which stays
read-write. A missing `hooks/` is created first, so the container cannot create
one; a missing index is not, because an empty one is corrupt. A write git makes
to a shadowed file fails at the rename and git removes its lock. `config` stays
read-write. Option 2 remains rejected for everything else: config, packed-refs
and refs are updated by lock-and-rename inside the common dir, which a
file-by-file mount breaks.

`isolation.gitDirMounts` owns the rationale. Other sites cite it; they do
not restate it. A rule copied into several places is the expensive kind of
documentation debt — one copy gets retired and the others keep asserting it.

## Consequences

The exposure is real and is accepted knowingly, not overlooked:

- A container can reach the main checkout's refs and objects. The blast
  radius is the repository, not the container's own tree. A container
  cannot reach other worktrees' admin dirs, so its git also cannot see which
  branches they have checked out, and does not refuse to check out or delete
  one of those.
- `.git/hooks` is **not** writable from a linked checkout's container, and so
  neither are the hooks that execute on the host. In-container agents cannot
  install hooks; that cost is accepted.
- `.git/config` is writable, and its `core.hooksPath`, `core.fsmonitor`,
  `core.sshCommand`, `core.pager` and `[alias]` keys name commands git runs on
  the host. That remains inside the accepted risk: the read-only `hooks/`
  closes the hooks directory, not every path from the common dir to host code
  execution.
- The reflog is writable, which is the recovery path the worktree lifecycle
  depends on.

Two fears that do **not** apply, recorded so they are not re-litigated:

- Uncommitted work is unaffected. It lives in the worktree directory, not the
  common dir.
- No credential is exposed by the mount where remotes are SSH; there is no
  embedded token to read or rewrite.

A main-checkout container run mounts `.git` read-write, because `.git` sits
inside the directory that becomes the workspace mount. Its index and HEAD are
that checkout's own and must stay writable for it to commit, so the read-only
overlays apply only where the common dir is mounted on its own — a linked
checkout; on a main-checkout run `hooks/` stays writable too.

`TestMountBase_HidesOtherWorktreesRegistry` pins the agent call site's posture
in both directions: the registry is unreachable and read-only, while the
checkout's admin dir and the common dir's config, packed-refs, objects and refs
resolve read-write through the runtime's path mapping.
`TestGitDirMounts_MainHooksIndexAndHeadAreReadOnly` pins the read-only
overlays and the writable admin-dir HEAD and index beside them, and
`TestContainerPolicy_HostBaseOutOfRepoWorktree_GitResolves` proves them in a
real container. Any further
narrowing must therefore be deliberate; it cannot happen accidentally in a
sweep. The `_run` site's mounts come from `scripts/devcontainer-git-mounts.sh`,
pinned the same way by the `TestDevcontainerGitMounts_` tests.

**Revisit trigger:** per-agent git isolation becoming a requirement, or agents
ceasing to be trusted by construction. Either flips this back to Proposed for
re-discussion, at which point option 2 above is the starting point.

One implementation trap worth carrying, because it fails silently: Docker's
`-v src:dst:ro` form mis-parses a destination ending in `.git`, landing the bind
at a truncated path while reporting success, so the mount appears to exist and
`.git` is still absent. Use `--mount type=bind,src=…,dst=…`, which takes named
keys and has no colon ambiguity.
