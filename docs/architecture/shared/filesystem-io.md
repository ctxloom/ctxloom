# Filesystem and I/O primitives

Three leaf packages own how ctxloom touches the filesystem: `internal/shared/iox` replaces a file's contents without any reader ever seeing a torn state and latches write errors on a formatted output stream; `internal/shared/filelock` serializes access to every mutable on-disk store with `flock(2)`-family advisory locks that work cross-process *and* intra-process; `internal/shared/watch` turns fsnotify into a filtered, optionally-recursive change-signal channel. `iox` and `watch` have zero internal imports; `filelock` has exactly one, `internal/paths`, for `paths.AppDirName`/`paths.LocksPath` — the `.ctxloom` directory name has exactly one home, and filelock's project-scoped lock path needs to name it rather than duplicate the literal. That one import is deliberate and stays acyclic: `internal/paths` depends on nothing in this trio, so `internal/config` and `internal/shared/tasks` can both depend on `filelock` without creating a cycle.

The contract they jointly own: **a mutable store is written atomically under an advisory lock named `<protected-path>.lock`, and observers learn it changed from a `watch.Watcher` that carries no data.**

```mermaid
flowchart TD
  subgraph iox["internal/shared/iox"]
    WFA["WriteFileAtomic(path, data, perm)"]
    WFAF["WriteFileAtomicFs(fs, path, data, perm)"]
    EW["ErrWriter — sticky-error io.Writer"]
  end

  subgraph fl["internal/shared/filelock"]
    L["Lock(path) (unlock func(), err)"]
    LS["LockShared(path) (unlock func(), err)"]
    LFU["lockFile — filelock_unix.go (!windows)<br/>syscall.Flock LOCK_EX / LOCK_SH"]
    LFW["lockFile — filelock_windows.go<br/>LockFileEx via kernel32 LazyDLL"]
    ED["ensureDir(path)"]
    L --> LFU & LFW
    LS --> LFU & LFW
    LFU --> ED
    LFW --> ED
  end

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
  L --> stores
  WFA --> stores
  WFAF --> stores
  stores -.->|"change signal, no payload"| W
  EW --> cli["internal/cli + cmd/taskloom renderers<br/>(55 construction sites, 155 Printf calls)"]

  callers["callers concatenate the .lock suffix themselves<br/>22 sites across 4 packages"] -.-> L
```

## `internal/shared/iox`

Two unrelated primitives in one package: crash-safe atomic replace, and a sticky-error output writer. 183 LOC, 11 internal importers.

| Symbol | file:line | Purpose |
|---|---|---|
| `WriteFileAtomic(path string, data []byte, perm os.FileMode) error` | `internal/shared/iox/atomicwrite.go:16` | Unique dot-prefixed temp in `filepath.Dir(path)` → write → `Sync` → `Close` (checked) → `Chmod(perm)` → `Rename` over `path`; removes the temp on every failure path |
| `WriteFileAtomicFs(fs afero.Fs, path string, data []byte, perm os.FileMode) error` | `internal/shared/iox/atomicwrite_fs.go:15` | Same algorithm over an `afero.Fs`; the seam `internal/config` needs to inject `MemMapFs` in tests. Independently written — no shared code with the `os` variant |
| `ErrWriter` | `internal/shared/iox/errwriter.go:22` | Wraps an `io.Writer`; latches the first write error, short-circuits every later write. Fields `w io.Writer`, `err error`, both unexported |
| `NewErrWriter(w io.Writer) *ErrWriter` | `internal/shared/iox/errwriter.go:28` | Required constructor (both fields unexported). Does not reject a nil `w` — `NewErrWriter(nil)` panics on first write, not at construction |
| `(*ErrWriter).Printf(format string, args ...any)` | `internal/shared/iox/errwriter.go:34` | Guarded `fmt.Fprintf`, latching. 155 call sites |
| `(*ErrWriter).Println(args ...any)` | `internal/shared/iox/errwriter.go:43` | Guarded `fmt.Fprintln`, latching. 64 call sites |
| `(*ErrWriter).Print(args ...any)` | `internal/shared/iox/errwriter.go:52` | Guarded `fmt.Fprint`, latching |
| `(*ErrWriter).WriteRaw(p []byte)` | `internal/shared/iox/errwriter.go:62` | Guarded raw write returning nothing — the errcheck-silencing spelling. Duplicates `Write`'s body rather than delegating |
| `(*ErrWriter).Write(p []byte) (int, error)` | `internal/shared/iox/errwriter.go:73` | `io.Writer` implementation; returns `(0, e.err)` once latched. Reached through interface dispatch (`clidiag.Fwarn`, `compactEntry`) — gopls under-reports its references |
| `(*ErrWriter).Err() error` | `internal/shared/iox/errwriter.go:83` | The terminal step of the pattern. 68 call sites |

Principal `WriteFileAtomic` consumers: `internal/sessions/index.go:214,763`, `internal/memory/stamp.go:50,99`, `internal/memory/compactor.go:1001,1021,1032`, `internal/shared/tasks/projectid/registry.go:99`, `internal/shared/tasks/projectid/marker.go:51`, `cmd/taskloom/manage.go:200`, `cmd/ltk/manage.go:228`. `WriteFileAtomicFs` consumers: `internal/config/config_save.go:60,131`, `internal/ltk/state/state.go:116`, `internal/remote/lockfile.go:124`, `internal/opencode/settings.go:682`, `internal/shared/agent/mcpfile.go:270`.

## `internal/shared/filelock`

Blocking exclusive/shared advisory locks, portable across Unix (`flock(2)`) and Windows (`LockFileEx`). Declares no types; the return value is a bare closure.

| Symbol | file:line | Purpose |
|---|---|---|
| `Lock(path string) (func(), error)` | `internal/shared/filelock/filelock.go:25` | Exclusive blocking lock; `return lockFile(path, false)`. 19 production call sites |
| `LockShared(path string) (func(), error)` | `internal/shared/filelock/filelock.go:33` | Shared blocking lock; `return lockFile(path, true)`. 3 production call sites, all in `internal/shared/tasks/log.go:554,578,598` |
| `ensureDir(path string) error` | `internal/shared/filelock/filelock.go:38` | Guards `""`/`"."`, then `os.MkdirAll(dir, 0o755)`. Returns `MkdirAll`'s error unwrapped |
| `lockFile(path string, shared bool)` (unix) | `internal/shared/filelock/filelock_unix.go:11` | `ensureDir` → `os.OpenFile(O_CREATE\|O_RDWR, 0644)` → `syscall.Flock` → release closure. Closes the fd on flock failure |
| unlock closure (unix) | `internal/shared/filelock/filelock_unix.go:31` | `Flock(LOCK_UN)` then `f.Close()`, both discarded via `_ =`; returns no error |
| `lockFile(path string, shared bool)` (windows) | `internal/shared/filelock/filelock_windows.go:23` | Same shape via `lockFileEx`; release closure at `:44`. Two unchecked `f.Close()` calls (`:40`, `:46`) |
| `lockFileEx(handle, flags)` | `internal/shared/filelock/filelock_windows.go:52` | `procLockFileEx.Call(handle, flags, 0, 0xFFFFFFFF, 0xFFFFFFFF, &overlapped)`; error consulted only when `r1 == 0` |
| `unlockFileEx(handle)` | `internal/shared/filelock/filelock_windows.go:73` | Mirror for `UnlockFileEx`; its only caller (`filelock_windows.go:45`) drops the return |
| `modkernel32`, `procLockFileEx`, `procUnlockFileEx`, `lockfileExclusiveLock = 0x00000002` | `internal/shared/filelock/filelock_windows.go:11-20` | Hand-rolled `syscall.LazyDLL`/`LazyProc` binding to `kernel32.dll` |

Call sites, by protected store: `internal/sessions/index.go:194,232,272,601,632,665,695,736` (index); `internal/config/config_manager.go:128` and `internal/config/config_save.go:99` (config); `internal/shared/tasks/log.go:333` (exclusive, event-log mutation) and `:554,578,598` (shared, reads); `internal/shared/tasks/projectid/registry.go:162,206,238`.

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
| `ctxloom plan watch` | `internal/cli/plan_watch.go:57` | recursive, on `~/.ctxloom/sessions` | `strings.HasSuffix(p, ".plan.md")` |

Both debounce at 100ms and emit a content-free `{"event":"changed","kind":…}` line on which the frontend re-queries.

## Invariants and contracts

**Atomic write (`iox`)**

- Ordering is the contract: temp file → `Sync` → `Close` (error checked, catching deferred write errors) → `Chmod` → `Rename`. A reader never observes a torn file.
- The temp name is *unique* (`os.CreateTemp` / `afero.TempFile` with a `*` pattern) and dot-prefixed, in the target's own directory. Uniqueness is what prevents concurrent writers clobbering each other's temp; a fixed temp name reintroduces that hazard.
- Both functions **succeed writing zero bytes** when `data` is nil or empty, atomically replacing an existing file with a 0-byte one. That is the deliberate contract (mirroring `os.WriteFile`); the fail-loud duty sits entirely with the caller.
- `perm` is applied by `Chmod`, which **ignores umask** — unlike `os.WriteFile`/`afero.WriteFile`, which mask it. A caller migrating from `os.WriteFile` under a restrictive umask gets a wider mode than before.
- Real vs documented: `atomicwrite_fs.go:13` claims "the new content survives a crash"; neither function fsyncs the parent directory after `Rename`, so only the temp's *contents* are durable — the rename is not. `atomicwrite.go:12-14` states the narrower, accurate claim.
- All errors are returned bare, unwrapped, and without the target path — a `CreateTemp` failure names the *temp* file, not the file the caller asked to write.

**Sticky-error writer (`ErrWriter`)**

- First error wins and sticks; every later write is a no-op. `Err()` is checked once, at the end.
- `Err() == nil` means either "all writes succeeded" or "nothing was ever written" — the two are indistinguishable.
- Not goroutine-safe (`err` is unsynchronised). One instance per goroutine.
- `Write` honours the `io.Writer` contract: non-nil error whenever `n < len(p)`. `WriteRaw` is the same operation with the return values dropped.

**Locking (`filelock`)**

- `flock(2)`, not `fcntl(2)`. Locks are owned by the *open file description*, so two independent `os.OpenFile` calls in one process genuinely conflict. This is depended on: `internal/config/config_save.go:110-117` documents `saveLocked` existing because a second `Lock` on the same path from the same process self-deadlocks.
- **Non-reentrant.** A goroutine holding the lock must not call anything that re-acquires it.
- `Lock`/`LockShared` are **blocking with no timeout, no context, and no try variant**. Contention therefore never produces an error — real vs documented: the "fall back to unlocked rather than blocking" rationale at several call sites describes an outcome these functions cannot produce. The only errors returnable are environmental and persistent: `ensureDir` failure, `os.OpenFile` failure, and `flock`/`LockFileEx` failure (EACCES, EROFS, ENOSPC, ENOLCK, EOPNOTSUPP).
- On error, `unlock` is `nil`. `unlock, _ := filelock.Lock(p); defer unlock()` panics; no in-tree caller does this, and nothing in the API prevents it.
- The returned closure takes no arguments and **returns no error**: a failed release is unreportable. Acceptable only because the lock file carries no data.
- **The `<protected-path> + ".lock"` naming convention is the package's real invariant and the package does not own it.** All 22 call sites concatenate the suffix by hand, in four packages; a fifth (`cmd/taskloom/watch.go:53-54`) encodes it independently in order to *exclude* `.lock` from a watch. A typo yields a lock nobody else takes — mutual exclusion silently absent, no error.
- Permission asymmetry: the lock *directory* is created `0o755` (`filelock.go:43`), the lock *file* `0644` (`filelock_unix.go:16`). Under a shared or UID-remapped `~/.ctxloom` (a live container configuration), user B cannot open user A's lock file `O_RDWR`.
- Every error is returned bare — the caller cannot tell a locking failure from any other `mkdir`/`open` failure.
- Lock files are never removed; they accumulate one per protected store. Harmless — `flock` releases on fd close, including process death.

**Watching (`watch`)**

- `New` **creates the root if it is missing** (`os.MkdirAll`, `watch.go:52`), documented at `:49-50` as deliberate so the watch can attach before the first write. Consequence: a typo'd or wrongly-resolved root produces a healthy watcher on a directory the process just invented, streaming zero events forever at exit 0.
- `filter == nil` means all events pass (guarded at `:123`).
- **`addTree` never consults `filter`** — the filter applies to *events* only. Recursive mode therefore costs one inotify watch per directory in the tree, including build and worktree churn, and `pump` adds a watch for each newly created directory too.
- `Close` is **not idempotent** — a second call panics on `close` of a closed channel. There is no `sync.Once`. Both consumers call it exactly once, via `defer`.
- `Close`'s ordering is required: `close(w.done)` before `w.fsw.Close()`.
- `errs` is buffered at 1 and the send is non-blocking with an empty `default:` — errors after the first buffered one are discarded, and if a consumer never selects on `Errors()`, all of them are. When `fsw.Errors` closes, `pump` returns without closing `errs`, so a consumer selecting on `Errors()` waits forever.
- Recursive adoption is inherently racy: files written into a new directory between its `mkdir` and the `fsw.Add` produce no event, and there is no rescan.
- `normalize`'s `default` arm returns `OpChmod`, so an unrecognised or zero op is reported as a confident "chmod".
- Real vs documented: the package doc justifies `Op`'s five-verb vocabulary as being "for the wire", but neither consumer reads `Event.Op` — both discard the whole event (`internal/cli/plan_watch.go:88`, `cmd/taskloom/watch.go:79`) and emit a fixed `{"event":"changed"}` line.
- Depth contract mismatch worth knowing when reading `plan watch`: the watcher is recursive at any depth while `internal/shared/plans.List` (`plans.go:60-80`) enumerates exactly `<root>/<harp>/*.plan.md` and skips second-level subdirectories, so nested plan files fire events that the list they trigger cannot show.
