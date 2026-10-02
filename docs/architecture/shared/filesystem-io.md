# Filesystem and I/O primitives

Three leaf packages own how ctxloom touches the filesystem: `internal/shared/safefs` is the write library over `afero` — the empty-write guard and durability as `afero.Fs` decorators, and the atomic writer that composes them; `internal/shared/errwriter` latches write errors on a formatted output stream; `internal/shared/watch` turns fsnotify into a filtered, optionally-recursive change-signal channel.

Advisory file locking is no longer a package of its own. `internal/shared/filelock` (a hand-rolled `flock(2)`/`LockFileEx` wrapper) was DELETED: the tree had carried two lock implementations — this one, at ~150 call sites, and `github.com/gofrs/flock`, already a direct dependency and already used in production by `internal/core/agent/rendezvous.go` — and the fix was to end the split by standardizing on the library, not to keep growing the hand-rolled one. Every former `filelock.Lock`/`TryLock`/`LockShared` call site now constructs a `*flock.Flock` directly (`flock.New(path, flock.SetPermissions(0o644))`, then `.Lock()`/`.TryLock()`/`.RLock()`/`.Unlock()`), following `rendezvous.go`'s idiom — see that file for the reference shape. What `filelock` also owned — `PathFor`, `ProjectPathFor` and `HomePathFor`, the protected-path→lock-name derivation — was PATH POLICY, not locking, and moved to `internal/core/paths` (`internal/core/paths/lockpath.go`), which already owned the home/project tiering those functions depend on.

The contract they jointly own: **a mutable store is written atomically under an advisory lock named `<protected-path>.lock`, and observers learn it changed from a `watch.Watcher` that carries no data.**

```mermaid
flowchart TD
  subgraph sf["internal/shared/safefs"]
    WFA["WriteFile(fs, path, data, perm, opts...)"]
    WFAF["NewGuardFs / NewDurableFs — afero.Fs decorators"]
    WFAF --> WFA
  end
  subgraph ew["internal/shared/errwriter"]
    EW["Writer — sticky-error io.Writer"]
  end

  subgraph fl["github.com/gofrs/flock (third-party, not an internal package)"]
    FN["flock.New(path, SetPermissions(perm)) *Flock"]
    FL["(*Flock).Lock() error — blocking exclusive"]
    FT["(*Flock).TryLock() (bool, error) — non-blocking exclusive"]
    FR["(*Flock).RLock() error — blocking shared"]
    FU["(*Flock).Unlock() error — always safe, even unlocked"]
    FN --> FL & FT & FR
    FL & FT & FR --> FU
  end
  subgraph pp["internal/core/paths (lockpath.go)"]
    PF["PathFor(protected) string"]
    PPF["ProjectPathFor(protected) (string, error)"]
    HPF["HomePathFor(protected) (string, error)"]
  end
  pp -.->|"derives the path each call site locks"| fl

  subgraph wt["internal/shared/watch"]
    NEW["New(root, recursive, filter)"]
    W["Watcher — fsw/recursive/filter/events/errs/done"]
    EV["Event{Path, Op}"]
    OP["Op — OpCreate/OpWrite/OpRemove/OpRename/OpChmod"]
    AT["addTree(dir) — WalkDir, NO filter applied"]
    PUMP["pump() — event loop goroutine"]
    NEW --> W
    W --> PUMP --> EV --> OP
    NEW --> AT
    PUMP --> AT
  end

  stores["mutable stores:<br/>sessions/index.go · config · tasks log ·<br/>projectid registry · remote lockfile · memory essence"]
  fl --> stores
  WFA --> stores
  stores -.->|"change signal, no payload"| W
  EW --> cli["internal/adapters/cli + cmd/taskloom renderers"]
```

## `internal/shared/safefs` and `internal/shared/errwriter`

Every production write goes through an `afero.Fs`; safefs is what sits on it. The package doc and each symbol's doc are the reference — this section only says how the pieces fit.

- `NewGuardFs` refuses zero bytes over an existing file (`ErrEmptyOverwrite`), on a rename and on a truncating open that is closed unwritten.
- `NewDurableFs` syncs the parent directory after a rename or a create, through the fs.
- `WriteFile` / `NewAtomicFile` write a unique temp file and rename it over the target through those decorators; `AllowEmpty()` and `Durable()` choose which apply.
- `WriteFileInPlace`, `OpenLockFile` and `OpenFIFOWriter` are the OS-only primitives, for a destination a rename cannot replace.
- `errwriter.Writer` is the errors-are-values writer the CLI renderers use.

The write-discipline gate (`archlint.WriteDisciplineAnalyzer`) refuses a raw `os` or `afero` write anywhere else.

## Advisory locking (`github.com/gofrs/flock` + `internal/core/paths`)

No longer an internal package (see the deletion note above). Every lock call site is now:

```go
lockPath, err := paths.HomePathFor(target)  // or PathFor / ProjectPathFor
...
if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil { ... }
fl := flock.New(lockPath, flock.SetPermissions(0o644))
stop := lockwait.Watch(lockPath)
err = fl.Lock()  // or fl.TryLock() / fl.RLock()
stop()
if err != nil { ... }
defer func() { _ = fl.Unlock() }()
```

`internal/core/agent/rendezvous.go` is the reference idiom (it predates and motivated the migration). `internal/shared/lockwait` (`Watch(label) (stop func())`) is a small, lock-agnostic package that carries forward the deleted `filelock` package's "still waiting" stderr notice on a slow blocking acquisition — it holds no lock itself, purely a watchdog goroutine, used at every `Lock`/`RLock` call site.

`os.MkdirAll` before `flock.New` is now each call site's own responsibility: unlike the deleted `filelock.Lock`'s internal `ensureDir`, `flock.New` does not create the lock's parent directory — a real behavioral difference call sites had to account for, not just a rename.

`PathFor(protected) string`, `ProjectPathFor(protected) (string, error)` and `HomePathFor(protected) (string, error)` (`internal/core/paths/lockpath.go`) are the protected-path→lock-name derivation the deleted package used to own — PATH POLICY, not locking, which is why they live in `internal/core/paths` rather than beside the lock calls. `PathFor` sits beside the protected file (home-rooted stores); `ProjectPathFor` maps into a project `.ctxloom/state/locks/`; `HomePathFor` maps into `~/.ctxloom/locks/` for a FOREIGN file (an engine's own settings.json/config.toml) more than one ctxloom-family binary may read-modify-write. See their doc comments for the full reasoning, including the deliberately-accepted flattening collisions.

Call sites, by protected store: `internal/core/sessions/index.go` (index, `lock()`); `internal/core/config/config_manager.go` (`Update`); `internal/shared/tasks/log.go` (`lock()` exclusive, event-log mutation; `lockShared()` for the three read paths); `internal/shared/tasks/projectid/registry.go` (`mutate`); `internal/shared/admission/store.go` (`lockedRMW`); `internal/core/agent/rmw_lock.go` (`WithFileLock`, the `SettingsWriter`/R6 family's shared lock idiom); `internal/adapters/isolation/ambient.go` (`lockInstanceHome`, warn-and-proceed rather than fail-closed); `internal/adapters/operations/vendorreader.go` (`TryLock` ownership probe); `internal/adapters/transcript/recorder.go` (`RLock` ownership, held for the recorder's lifetime).

## `internal/shared/watch`

An fsnotify wrapper: watch a root, optionally recursively including directories created later, filter events by path, normalize the op bitmask, deliver on a channel. 157 LOC, two consumers.

| Symbol | file:line | Purpose |
|---|---|---|
| `Op` (string) | `internal/shared/watch/watch.go:21` | Normalized filesystem verb |
| `OpCreate`, `OpWrite`, `OpRemove`, `OpRename`, `OpChmod` | `internal/shared/watch/watch.go:23-29` | The five verb constants |
| `Event{Path string; Op Op}` | `internal/shared/watch/watch.go:32` | One change to a watched path |
| `Watcher` | `internal/shared/watch/watch.go:38` | Fields `fsw *fsnotify.Watcher`, `recursive bool`, `filter func(string) bool`, `events chan Event`, `errs chan error` (buffered 1, `:64`), `done chan struct{}` |
| `New(root string, recursive bool, filter func(string) bool) (*Watcher, error)` | `internal/shared/watch/watch.go:51` | `os.MkdirAll(root, 0o755)` (`:52`) → create fsnotify watcher → add root or whole tree → start `pump` goroutine (`:76`). Closes the fsnotify handle on error before returning (`:69`, `:73`) |
| `(*Watcher).Events() <-chan Event` | `internal/shared/watch/watch.go:81` | Receive-only view of `events` |
| `(*Watcher).Errors() <-chan error` | `internal/shared/watch/watch.go:84` | Receive-only view of `errs` |
| `(*Watcher).Close() error` | `internal/shared/watch/watch.go:87` | `close(w.done)` then `w.fsw.Close()`. Ordering matters: signal `pump` before tearing down the handle it reads |
| `(*Watcher).addTree(dir string) error` | `internal/shared/watch/watch.go:94` | `filepath.WalkDir`, `fsw.Add` for every directory. Walk errors return `nil` (`:96-98`) so an unreadable subtree is skipped |
| `(*Watcher).pump()` | `internal/shared/watch/watch.go:108` | Event loop: on `done` return; on a Create that stats as a directory, `addTree` (`:118-122`); apply the filter (`:123`); forward (`:127`); non-blocking error offer (`:135-138`) |
| `normalize(op fsnotify.Op) Op` | `internal/shared/watch/watch.go:144` | Bitmask → single verb; `default:` returns `OpChmod` (`:154-155`) |

| Consumer | Site | Mode | Filter |
|---|---|---|---|
| `taskloom watch` | `cmd/taskloom/watch.go:55` | non-recursive, on `filepath.Dir(logPath)` | `p == logPath` |
| `ctxloom plan watch` | `internal/adapters/cli/plan_watch.go:57` | recursive, on `~/.ctxloom/sessions` | `strings.HasSuffix(p, ".plan.md")` |

Both debounce at 100ms and emit a content-free `{"event":"changed","kind":…}` line on which the frontend re-queries.

## Invariants and contracts

**Atomic write (`safefs.WriteFile`)**

- Ordering is the contract: temp file → `Sync` → `Close` → `Chmod` → `Rename`. A reader never observes a torn file.
- The temp name is unique and dot-prefixed, in the target's own directory, so concurrent writers never clobber each other's temp.
- Zero bytes over an existing file are refused with `ErrEmptyOverwrite` unless `AllowEmpty()` is passed; zero bytes to a new path proceed.
- `perm` is applied by `Chmod`, which ignores umask — unlike `os.WriteFile`/`afero.WriteFile`.
- Without `Durable()` the rename is atomically visible but its directory entry is not synced.

**Sticky-error writer (`errwriter.Writer`)**

- First error wins and sticks; every later write is a no-op. `Err()` is checked once, at the end.
- `Err() == nil` means either "all writes succeeded" or "nothing was ever written" — the two are indistinguishable.
- Not goroutine-safe. One instance per goroutine.

**Locking (`gofrs/flock` + `internal/core/paths`)**

- `flock(2)` on Unix (via `golang.org/x/sys/unix`), a comparable mandatory-lock API on Windows — `*flock.Flock` handles the platform split internally; ctxloom code is platform-agnostic.
- **Non-reentrant.** A goroutine holding the lock must not call anything that re-acquires the same `*flock.Flock`.
- `Lock`/`RLock` are **blocking with no timeout**; `TryLock` is the non-blocking variant, returning `(false, nil)` on contention rather than an error. `flock.Flock` also offers `TryLockContext`/`TryRLockContext` (poll-with-context) that no call site in this codebase currently uses.
- `flock.Flock.Unlock()` is always safe to call — on an unlocked `*flock.Flock`, and safe to call twice — so the old "on error, unlock is nil" hazard (a custom closure that could be nil) is gone: every call site holds a concrete, always-non-nil `*flock.Flock` value, not a closure the package's own bookkeeping could get wrong.
- `flock.New` does NOT create the lock file's parent directory (unlike the deleted `filelock.Lock`'s internal `ensureDir`) — every call site does its own `os.MkdirAll` first.
- **The `<protected-path> + ".lock"` / flattened-name naming convention is `internal/core/paths`' invariant now** (`PathFor`/`ProjectPathFor`/`HomePathFor`, `internal/core/paths/lockpath.go`), not re-derived at each call site — see those functions' own docs for the collision stance and the home/project boundary.
- Lock files are never removed; they accumulate one per protected store. Harmless — the OS releases the lock on fd close, including process death.

**Watching (`watch`)**

- `New` **creates the root if it is missing** (`os.MkdirAll`, `watch.go:52`), documented at `:49-50` as deliberate so the watch can attach before the first write. Consequence: a typo'd or wrongly-resolved root produces a healthy watcher on a directory the process just invented, streaming zero events forever at exit 0.
- `filter == nil` means all events pass (guarded at `:123`).
- **`addTree` never consults `filter`** — the filter applies to *events* only. Recursive mode therefore costs one inotify watch per directory in the tree, including build and worktree churn, and `pump` adds a watch for each newly created directory too.
- `Close` is **not idempotent** — a second call panics on `close` of a closed channel. There is no `sync.Once`. Both consumers call it exactly once, via `defer`.
- `Close`'s ordering is required: `close(w.done)` before `w.fsw.Close()`.
- `errs` is buffered at 1 and the send is non-blocking with an empty `default:` — errors after the first buffered one are discarded, and if a consumer never selects on `Errors()`, all of them are. When `fsw.Errors` closes, `pump` returns without closing `errs`, so a consumer selecting on `Errors()` waits forever.
- Recursive adoption is inherently racy: files written into a new directory between its `mkdir` and the `fsw.Add` produce no event, and there is no rescan.
- `normalize`'s `default` arm returns `OpChmod`, so an unrecognised or zero op is reported as a confident "chmod".
- Real vs documented: the package doc justifies `Op`'s five-verb vocabulary as being "for the wire", but neither consumer reads `Event.Op` — both discard the whole event (`internal/adapters/cli/plan_watch.go:88`, `cmd/taskloom/watch.go:79`) and emit a fixed `{"event":"changed"}` line.
- Depth contract mismatch worth knowing when reading `plan watch`: the watcher is recursive at any depth while `internal/shared/plans.List` (`plans.go:60-80`) enumerates exactly `<root>/<harp>/*.plan.md` and skips second-level subdirectories, so nested plan files fire events that the list they trigger cannot show.
