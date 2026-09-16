# agent — managed-file writers and reconcilers

Every byte ctxloom puts into a user's engine config directory goes through one of the reconcilers on this page. They share one discipline: ctxloom owns a *marked* or *manifest-tracked* subset of a file or tree, and each write removes exactly the previous ctxloom-owned set before laying down the new one, so uninstall is always possible and foreign content survives. `AtomicWriteFile` is the single low-level write primitive; `CtxloomCommand`/`ResolveMCPCommand` are the single policy point for what binary path lands in a generated config.

References below are by **symbol** (`Type.Method` or bare function name), not `file:line` — a line number drifts on any edit above it and silently points at the wrong thing; a symbol fails loud when it goes stale (`grep` finds nothing) instead of misleading.

```mermaid
flowchart TD
  subgraph prim["primitives — settings_io.go, rmw_lock.go"]
    AWF["AtomicWriteFile(fs, path, data, desc)"]
    WFL["WithFileLock(fs, target, fn)"]
    GFS["GetFS(fs) — nil → OsFs"]
    WRN["Warn(fmt, ...) → clidiag"]
    CC["CtxloomCommand() = selfexec.Path()"]
    RMC["ResolveMCPCommand(override)"]
    HASH["ComputeHookHash / ComputeMCPServerHash / ComputeCommandDigest"]
    RC["RefuseCorrupt(fs, path, data, ...)"]
  end
  subgraph marker["marker-section files — managedcontext.go"]
    WMC["WriteManagedContext"]
    SMS["StripManagedSection"]
    DMC["DeliverManagedContext"]
  end
  subgraph tree["manifest-tracked trees — packagefiles.go"]
    WMPF["WriteManagedPackageFiles[T]"]
    PED["pruneEmptyDirs"]
    PF["PackageFile{Path, Data, Mode}"]
  end
  subgraph cmds["command / skill rendering"]
    CE["CommandExport"]
    WMCF["WriteManagedCommandFiles"]
    SCRP["SafeCommandRelPath"]
    TMP["TransformMustacheToPositional"]
    EYS["EscapeYAMLString"]
    YDQ["yamlDoubleQuoted"]
    SKE["SkillExport"]
  end
  subgraph mcp["MCP registries"]
    MB["InstallMCPServerJSON / Uninstall / Installed"]
    REG["MCPRegistrar (interface)"]
  end
  LEDGER[(".ctxloom-managed — internal/shared/ledger")]
  CJ["CanonicalJSON (marshal.go)"]
  SYM["symlink.go — WarnOnCtxloomPathSkew"]

  WMC --> WFL
  WFL --> AWF
  DMC --> WMC
  WMCF --> WMPF
  WMPF --> SCRP
  WMPF --> PED
  EYS --> YDQ
  CJ --> AWF
  WMPF --> LEDGER
  MB --> CJ
  REG --> MB
  CC --> RMC
```

## R6: exclusively-owned files inside a foreign engine's directory (ruled 2026-08-14)

Some files live inside a *foreign* engine's config directory (`~/.claude`-shaped) but ctxloom is the **sole** author of the whole file — claude's per-instance `.claude.json`. Left to per-site judgment, "does exclusive ownership excuse the lock and the ledger" gets answered differently at every site — one writer relying on the *caller's* project lock rather than its own, another locked, a third skipping both outright — and the inconsistency is invisible until two of them race.

**The rule, no per-site judgment:** a file ctxloom exclusively owns inside a foreign engine's directory is locked and ledgered like a shared file.

- `claude.claudeInstanceConfig.WriteInstanceConfig` now takes its own `agent.WithFileLock` around the whole load-modify-write cycle, keyed to the generated file itself — not the caller's `isolation.lockInstanceHome`, which locks a *different* path in a *different* lock namespace (`paths.ProjectPathFor` on the instance-home directory vs. `paths.HomePathFor` on the generated file) and silently no-ops for the harpless worktree fallback.
- `claude.appendFlagDelivery.DeliverContext` now writes its framed `<hash>.sysprompt.md` cache file through `AtomicWriteFile` instead of a raw `afero.WriteFile`.

**The ratchet:** `tests/arch/lock_discipline_test.go` (`TestArch_LockDiscipline_EngineRMWIsLocked`) and `tests/arch/ledger_discipline_test.go` (`TestArch_LedgerDiscipline_ManagedWritersRecordOwnership`) are write-discipline-shaped gates — a name-based heuristic over every function in the `SettingsWriter` packages plus this package, with a reasoned, symbol-keyed allowlist and an `AllowlistIsLive` staleness twin each. They are heuristics, not proofs (see their own doc comments for exactly what they can and cannot see), and each carries a reasoned baseline for the gaps it knows about.

## Write primitives — `settings_io.go`, `rmw_lock.go`

| Symbol | Purpose |
|---|---|
| `AtomicWriteFile` | Routes through `iox.WriteFileAtomicFs`: a **unique** temp name in the destination directory (`afero.TempFile`, so two concurrent writers never clobber each other's in-flight bytes), fsync, chmod to an exact mode, then rename. **No backup is taken** — see "What changed" below for why. Refuses a zero-byte write over an existing file unless the caller opts in with `AllowEmptyWrite()`. |
| `WithFileLock` | The `SettingsWriter`/R6 family's one lock idiom: `fn` runs as the WHOLE read-modify-write cycle under a lock at `paths.HomePathFor(target)` (a real OS home-rooted lock directory, not a sidecar beside `target`). Skipped when `fs` is not OS-backed (a test double has no other process to exclude). Fail-closed on acquisition failure. |
| `GetFS` | nil → `afero.NewOsFs()`; the single defaulting point every writer in this package and its engine callers uses. |
| `Warn` | `clidiag.Warn("ctxloom", …)`; binds the program name once. |
| `CtxloomCommand` | Returns `selfexec.Path()` — the absolute path of the running ctxloom binary, so a materialized surface can never diverge between a staged and an installed binary. |
| `ResolveMCPCommand` | Override-or-default; the container-override policy chokepoint (`isolation.Container.MCPCommandOverride`). |
| `ComputeHookHash` / `ComputeMCPServerHash` / `ComputeCommandDigest` | sha256-derived short identifiers used as ownership markers (hooks, MCP server entries, the statusline command) — the `SCM` field claude carries on each managed entry, and the digests the ledger records for the same purpose elsewhere. |
| `SettingsOptions` | `{FS afero.Fs}` — filesystem seam only. Per-engine policy (which surfaces are managed) rides the surfaces × cells seam elsewhere, not this struct. |
| `RefuseCorrupt` | The one refusal shape for "part of this user-owned file will not parse": backs the original bytes up to `<path>.corrupt-<unix>` and returns an error so the caller aborts *before* touching the file. Every backend that round-trips a user-editable settings/hooks/MCP file routes partial-parse failures here. |
| `CanonicalJSON` (`marshal.go`) | Marshal → generic decode with `UseNumber` (numeric precision preserved) → sorted, indented, newline-terminated re-encode. The double round-trip *is* the key-sorting mechanism. |

## Marker-section files — `managedcontext.go`

| Symbol | Purpose |
|---|---|
| `WriteManagedContext` | Merges content into the managed-marker section of a human-editable file (CLAUDE.md, AGENTS.md, …), preserving surrounding user content **in position** — a section that sat *below* the end marker used to be hoisted above it on every rewrite; it is now reinserted at the same offset the old section occupied. The whole read-splice-write(-or-remove) cycle runs inside its own `WithFileLock`. |
| `StripManagedSection` | Removes the managed marker section, returning the surrounding user content. |
| `DeliverManagedContext` | Writes the managed section and returns a strip-on-cleanup handle. Every context-file writer routes through it. |

## Manifest-tracked trees — `packagefiles.go`

| Symbol | Purpose |
|---|---|
| `PackageFile` | One rendered file in a package: `{Path, Data, Mode}`. Shared vocabulary across every engine's command/skill writer. |
| `WriteManagedPackageFiles[T]` | Manifest-scoped tree writer: remove the previously-tracked set, render-to-a-temp-sibling-then-swap each file into place, rewrite the `ledger.Surface`-scoped manifest. Carries an empty-render guard (refuses to touch an existing surface when every enabled item rendered zero files). **Not itself wrapped in `WithFileLock`** — a known, deferred gap (it writes into directories shared with the user and with concurrently-firing hooks/applies); its render-to-temp-then-swap shape is also invisible to `lock_discipline_test.go`'s write-signal heuristic, which recognizes `AtomicWriteFile`/`save*` but not this function's own `afero.WriteFile`-into-temp-dir + `fs.Rename` swap. |
| `pruneEmptyDirs` | Best-effort bottom-up empty-directory cleanup; all errors ignored by design. |

## Command and skill rendering

| Symbol | Purpose |
|---|---|
| `CommandExport` | Agent-agnostic slash-command export spec. |
| `SafeCommandRelPath` | Validates a bundle-supplied name as a path confined to a directory — a security boundary, since bundle content is remote. |
| `WriteManagedCommandFiles` | Adapts a command export ("a command is a one-file package") onto `WriteManagedPackageFiles`. |
| `TransformMustacheToPositional` | Rewrites `{{var}}` → `$N` by first-occurrence order. |
| `EscapeYAMLString` | Quotes and escapes for YAML frontmatter (command files). |
| `SkillExport` | Agent-agnostic Agent Skill package export spec — the `SurfaceSkills` sibling of `CommandExport`. |
| `yamlDoubleQuoted` (`commandfiles.go`) | `json.Marshal` of a string as a YAML double-quoted scalar — the package's one escaping algorithm. |

**One escaping algorithm, one quoting policy.** `yamlDoubleQuoted` always quotes and escapes control characters via `json.Marshal`. `EscapeYAMLString` (command frontmatter) decides WHETHER to quote and then delegates the escaping itself to `yamlDoubleQuoted`, rather than keeping hand-written rules that escaped neither `\n` nor `\r`.

## MCP registries

| Symbol | Purpose |
|---|---|
| `InstallMCPServerJSON` | Merges one server into `mcpServers`, preserving foreign top-level keys. A **present-but-wrong-type** `mcpServers` value (a string, an array) is **refused**, not silently replaced with a fresh empty map — the failure mode that used to destroy whatever the user had under that key. |
| `UninstallMCPServerJSON` | Removes one server; absent is a no-op by contract. |
| `MCPRegistrar` | The facet an external tool (`taskloom manage`) uses to register a server without learning per-agent paths: `{Name, Present, ConfigPath, Install, Uninstall, Installed}`. `claude.MCPRegistrar` implements it over the shared JSON `InstallMCPServerJSON`, which refuses a present-but-wrong-type `mcpServers` value rather than silently replacing it. |

## internal/shared/ledger — the sidecar ownership record

**Marker filename:** `.ctxloom-managed` (constant `ledger.Name`) — **one filename for every engine and every surface**, not the per-engine `<Path>.ledger` variants that predated it. Lines are `<name>\t<surface>`; `Surface` is a deliberately open string type (`ledger.SurfaceMCP`, `SurfaceCommands`, `SurfaceSkills`, `SurfaceHooks`, `SurfaceContext`, `SurfacePermissions`, `SurfaceStatusLine`, and any caller-defined value), so two co-located surfaces sharing one directory never delete each other's entries, and a plugin can claim its own surface with no registration step.

`ledger.Ledger.Read` returns `(nil, nil)` for a missing marker (the legitimate "nothing managed yet" case) but propagates any other read error — never flattens it to empty. `ledger.Ledger.Write` rewrites the marker atomically (`iox.WriteFileAtomicFs`), in a stable sorted order (so an unchanged managed set produces byte-identical output), and removes the marker file only when **every** surface is empty.

Consumers: `WriteManagedPackageFiles` (`SurfaceCommands`/`SurfaceSkills`), and `claude.ClaudeCodeHookWriter.writeSettingsFile` / `removeSettingsFile` (`SurfaceHooks`/`SurfacePermissions`/`SurfaceStatusLine`).

## Binary-path skew warning — `symlink.go`

| Symbol | Purpose |
|---|---|
| `GetExecutablePath` | `os.Executable` + `EvalSymlinks`, memoized in a package global. |
| `WarnOnCtxloomPathSkew` | Warns when the `ctxloom` on `PATH` differs from the running binary — a surface materialized before the `CtxloomCommand` self-exec-absolute fix still carries the bare name `ctxloom` until the next apply re-materializes it, and this is what catches that. Called from the MCP server's startup path. |

## Invariants and contracts

- **`AtomicWriteFile` is the single low-level write path** for settings/config surfaces, and it takes **no backup**. This is a deliberate change, not an omission: the old `<path>.ctxloom.bak` copy existed because a writer that could not tell its own content from the user's had to rewrite the file wholesale and keep a copy in case it was wrong. Every writer reaching `AtomicWriteFile` now knows what it owns — through the sidecar ledger or through in-file managed markers — so it edits its own content and leaves the rest untouched, and there is nothing to recover from. See `internal/shared/ledger`'s package doc for the fuller history (five independently-drifted per-engine ownership records, consolidated into one).
- **The temp file name is unique per write** (`afero.TempFile` with a `.`+base+`.*.tmp` pattern), not a fixed suffix — two concurrent writers of the same settings file can never clobber each other's in-flight temp file the way a fixed name could.
- **A rename failure is returned, never papered over**, and there is no cross-device fallback: the temp file lives in the destination directory by construction, so cross-device rename cannot occur, and every internal failure branch best-effort removes the orphaned temp file before returning the error.
- **`AtomicWriteFile` refuses a zero-byte write over an existing file** unless the caller opts in via `AllowEmptyWrite()` — for an encoder that renders an emptied managed set as literally zero bytes. No writer in the tree opts in today; the option stays for the next one that must.
- **`CtxloomCommand` is the binary-path policy for materialized surfaces**, and `ResolveMCPCommand` is the resolver every MCP-surface writer uses, with the container-override seam substituting an in-container path when the surface will be read from inside an isolated cell.
- **`WriteManagedContext` preserves user content in position**, not merely byte-for-byte: content that sat below the end marker used to be hoisted above the re-appended managed section on every rewrite; it is now reinserted at the same offset the old section occupied.
- **`WriteManagedContext` with empty content deletes the file** — the intended uninstall semantics and the terminus of the empty-context chain.
- **`WriteManagedPackageFiles` removes the previously-tracked set BEFORE rendering.** Every per-item failure warns and continues, and the function returns `nil` when nothing was written — so a total render failure wipes the prior delivery and reports success. The manifest is the only record of what ctxloom owns in that tree, and (see R6 above) this function is not itself under `WithFileLock` — a known, deferred gap, not a fixed one.
- **`SafeCommandRelPath` must gate every bundle-supplied name** before it becomes a path. Bundle content is remote content.
- **The sidecar ledger (`internal/shared/ledger`, marker `.ctxloom-managed`) is the record of managed names** for every surface that uses it — not a per-engine `<Path>.ledger` file. Written sorted and atomically, removed only when every co-located surface is empty.
- **A ledger read error is propagated, not flattened.** `ledger.Ledger.Read` returns a real error rather than degrading to "nothing managed" — a writer that mistakes an unreadable ledger for an empty one concludes it manages nothing and orphans every entry it wrote last time. A missing marker is the one legitimate empty case, and it alone returns `(nil, nil)`.
- **A user-owned settings or registry file that fails to parse is refused, not replaced — at every level of the document.** `claude.ClaudeCodeHookWriter.loadSettings` routes a failed top-level decode through `corruptSettings` to `RefuseCorrupt`, and so does every nested block it splits out (`hooks`, and `parseStatusLine`/`parsePermissions` for `statusLine`/`permissions`/`permissions.deny`). "I could not read it" is not "it was empty": each of those paths once warned and continued, and the delete-then-re-emit-from-the-typed-field shape behind the warning meant the user's own hooks, statusline and allow/ask rules were dropped from the file on a success path. A warning is not a guard — the routing exists so no future field can be added with a warn-and-continue branch. `InstallMCPServerJSON` takes the same stance on a present-but-wrong-type `mcpServers` value.
- **A preserved field is re-emitted as its ORIGINAL bytes, never decoded-and-reencoded.** `claude.ClaudeCodeHookWriter.saveSettings` and `claude.permissionsOutput` decode each preserved key only as a *gate* (`preserveFailure` refuses the write when a value cannot be carried through) and emit the raw bytes; handing the decoded value to `CanonicalJSON` instead would round every number past `float64`'s exact range — `1234567890123456789` comes back `1234567890123456800`, a rewrite of the user's own file that no warning or exit code reports.
- **`MCPRegistrar` has two facets with different natures**: `{Name, Present, ConfigPath}` vary per agent; `{Install, Uninstall, Installed}` are delegated verbatim to this package's shared JSON functions by the JSON-shaped implementors; a registrar over a different document model would have to reimplement the same contract.
- **`WarnOnCtxloomPathSkew` exists only for surfaces materialized before the `CtxloomCommand` self-exec fix** — those still carry the bare name `ctxloom` until the next apply re-materializes them.
