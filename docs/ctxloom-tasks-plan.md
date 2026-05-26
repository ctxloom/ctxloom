# ctxloom-tasks — Replacement for mcp-tasks

> **Status:** design draft (2026-05-26). Not yet implemented. Replaces dependency on `flesler/mcp-tasks` with a ctxloom-native task layer that auto-captures Claude's TodoWrite snapshots and binds tasks to harp-named sessions.
> **Living document** — keep this in sync with conversation decisions and PR progress.

## Problem

`flesler/mcp-tasks` works, but task lists evaporate between sessions: when a Claude run ends, its in-memory `TodoWrite` set is gone, and there is no durable record on disk. The MCP server's task file is per-session-per-invocation; nothing aggregates across runs. Two consequences:

1. **Tasks get lost.** A long-running plan disappears when the agent compacts or the session ends.
2. **No auto-capture.** Claude's own `TodoWrite` already encodes the active plan — but ctxloom doesn't pick it up live. `internal/memory/plans.go` extracts `TodoWrite` blocks *during* distillation (post-hoc), which is too late to be a usable working store.

We want a ctxloom-native task layer that:

- **Auto-captures** Claude's TodoWrite output via a `PostToolUse` hook (mirror-snapshot semantics).
- **Persists** to a flesler-style markdown file inside `.ctxloom/`.
- **Names everything with harp** — tasks and sessions both — so the user can resume "the swift-amber-falcon plan" with both distilled context and tasks intact.

## Goals

1. Replace the `flesler/mcp-tasks` MCP server with a ctxloom-internal task surface (≤5 tools).
2. Generate human-friendly identifiers (harp IDs) for every task and session.
3. Auto-capture Claude's `TodoWrite` snapshots into the project task file via a shipped hook.
4. Persist tasks per-project in a flesler-style markdown file readable by the user.
5. Assign harp names to sessions **at ctxloom startup** (before LLM boot), persist them, echo them into the LLM's `Instructions` so it knows its own name, and let the user pick prior sessions via an interactive CLI picker (with flag bypass for scripts).
6. Ship the hook + LLM nudges as a `ctxloom-default-tasks` bundle (no manual wiring needed).
7. **Reduce overall MCP tool surface** while adding this work — net result lands at ~18 tools (vs. ~41 today, ~46 naïve) by converting listings to resources and demoting rare writes to CLI. Claude Code is the LCD target.

## Design summary

- **harp lives in `internal/harp/`** as a fully-native Go package (no cgo, no WASM, no wazero). Word lists embedded via `go:embed`; uses `crypto/rand`. Built here for speed of iteration; **extracted to `github.com/benjaminabbitt/harp-go` later** once the API stabilizes. Designed from day one to extract cleanly: zero ctxloom imports, no project-specific behavior, importable by other Go consumers verbatim when the time comes.
- **Tasks live in `.ctxloom/tasks.md`** — single file per project, flesler-style sectioning by status, with the harp ID inline so the LLM echoes it back on subsequent `TodoWrite` calls:
  ```markdown
  ## In Progress
  - [ ] `swift-amber-falcon` implement TodoWrite hook

  ## To Do
  - [ ] `quiet-silver-meadow` write storage layer
  ```
- **Reconciliation = harp-id-in-text + text-hash fallback.** First sight: hash the trimmed text, generate a harp ID, append to the file. The harp ID appears in `task_list` output. On next `TodoWrite`, the LLM has the harp ID in its content, so we match by ID. Items without an ID get text-hash-matched against existing tasks; novel items get new IDs.
- **Auto-capture = `PostToolUse(TodoWrite)` hook.** Bundle ships a hook script (`ctxloom tasks capture --stdin`) that reads the tool_use payload, runs reconciliation, and rewrites `.ctxloom/tasks.md`. Mirror-snapshot: ctxloom's active set ≡ Claude's last `TodoWrite`. Items missing from the new snapshot move to `## Archived` (or `## Done` if their `status: completed` field arrived in the same write).
- **Session harp-naming.** When ctxloom registers a session, it also assigns a harp name and persists a manifest entry. `compact_session` writes both the essence and a frozen task snapshot under that harp name. `load_session swift-amber-falcon` resolves the harp → session, hydrates both.

### Why this shape

- **Single file per project** chosen over per-task files: matches flesler's mental model the user already likes; trivial for the user to `cat .ctxloom/tasks.md`; no directory cruft; rewrites are cheap.
- **Harp ID in text** chosen over ID-as-sidecar: the LLM only sees what's in `task_list` output, so the ID *must* be in the visible text to round-trip back through TodoWrite. Hash fallback covers the case where the LLM strips it.
- **Mirror-snapshot** over append-with-manual-reconcile: matches how Claude already thinks about TodoWrite (it's a snapshot, not a journal). Archiving — not deleting — preserves history.
- **Fully native harp-go** over the existing WASM binding: ctxloom is already CGO-heavy (ONNX, treesitter); piling on a WASM runtime for ID generation is disproportionate.
- **Bundle-shipped hook** over hard-coded ctxloom config: users opt in by installing the bundle, hooks are reviewed via the bundle-review gate (already implemented).

## Existing infrastructure to leverage

- `internal/memory/plans.go::ExtractPlans` — already extracts `TodoWrite` blocks from session transcripts. The auto-capture hook is the *live* equivalent; the existing plan extractor remains for post-hoc compaction.
- `internal/lm/backends/interfaces.go::SessionHistory` — `RegisterSession`, `GetSession`, `GetCurrentSession`, `ListSessions`. Harp-naming layer hooks in here.
- `cmd/mcp_tools_memory.go` — `list_sessions`, `load_session`, `browse_session_history`, `compact_session`. Surface the harp name in these results.
- `internal/operations/hooks.go::ApplyHooksRequest` — bundle hook application path. The new `PostToolUse(TodoWrite)` hook routes through here.
- The bundle review gate (committed in `f1262a4`) — the `ctxloom-default-tasks` bundle will be subject to the same review flow on first install.

---

## Phase 0 — `internal/harp/` (extract later)

Inline package inside ctxloom. Zero ctxloom imports, no project-specific behavior — keep it extraction-ready so a future move to `github.com/benjaminabbitt/harp-go` is a `git filter-repo` + `go.mod` rename, nothing else.

### 0.1 Word lists

Port from `harp-core`:
- `internal/harp/adjectives.txt` (1,269 entries) embedded as `//go:embed adjectives.txt`
- `internal/harp/nouns.txt` (4,396 entries) embedded the same way
- A `_test.go` that asserts the counts so a future word-list edit can't silently drift

### 0.2 API

```go
package harp

func GenerateName() string

type Options struct {
    Components       int    // 2-16, default 3 (= N-1 adjectives + 1 noun)
    MaxElementLength int    // 0 = no limit
    Separator        string // default "-"
}

func GenerateNameWithOptions(opts Options) string
```

Uses `crypto/rand` — uniform pick across each list. No global state. For test determinism, expose a `var rngRead = rand.Read` package-level seam so tests can stub.

### 0.3 CLI

`cmd/harp_cmd.go` registers `ctxloom harp` mirroring the Rust CLI flags (`-c`, `-s`, `--max-len`). Lets the user smoke-test the generator without the rest of ctxloom. When we extract, this becomes the standalone `harp` binary.

### 0.4 Tests

Property tests in `internal/harp/harp_test.go`:
- Output split by separator has exactly `Components` segments.
- Every segment is in the embedded word list.
- The last segment is from `nouns.txt`; earlier are from `adjectives.txt`.
- With `MaxElementLength=N`, no segment exceeds `N`.
- 10k generations produce >9990 unique results (collision-rate sanity).

### 0.5 Extraction trigger

Extract to its own repo when **any** of: (a) a non-ctxloom consumer wants to vendor it, (b) the API surface stops churning, (c) we want versioned releases independent of ctxloom. Until then it lives here.

---

## Phase 1 — `internal/tasks/` package + storage + MCP tools

### 1.1 Storage layer — `internal/tasks/store.go`

```go
type Task struct {
    HarpID   string    // "swift-amber-falcon"
    Text     string    // trimmed, no harp ID inside
    Status   string    // "In Progress" | "To Do" | "Done" | "Archived" | user-custom
    Created  time.Time
    Updated  time.Time
    TextHash string    // sha256(text) hex[:12] — for hash-fallback matching
}

type Store struct {
    path string // .ctxloom/tasks.md
}

func Open(projectDir string) (*Store, error)
func (s *Store) List(statuses []string) ([]Task, error)
func (s *Store) Add(text, status string) (Task, error)
func (s *Store) SetStatus(harpID, status string) (Task, error)
func (s *Store) Search(term string) ([]Task, error)
func (s *Store) Snapshot() ([]Task, error)         // raw, all statuses
func (s *Store) Reconcile(items []SnapshotItem) (Diff, error)  // mirror-snapshot apply
```

File format (round-trip preserving):
```markdown
# Tasks

<!-- ctxloom-tasks:v1 -->

## In Progress
- [ ] `swift-amber-falcon` implement TodoWrite hook

## To Do
- [ ] `quiet-silver-meadow` write storage layer
- [ ] `bold-crimson-thunder` port harp to Go

## Done
- [x] `misty-golden-river` spike session naming

## Archived
- [x] `pale-fern-mountain` initial spike (replaced by bold-crimson-thunder)
```

Parser: line-oriented, regex `^(- \[[ x]\] \x60([\w-]+)\x60\s+(.+))$`. Anything that doesn't match is preserved verbatim under the section it appeared in, so user-added notes survive a round-trip.

### 1.2 Reconciliation — `internal/tasks/reconcile.go`

```go
type SnapshotItem struct {
    Text   string  // verbatim from TodoWrite (may include harp ID)
    Status string  // mapped from TodoWrite's "status" field
}
type Diff struct {
    Added, Updated, Archived []Task
}

func (s *Store) Reconcile(items []SnapshotItem) (Diff, error)
```

Algorithm:
1. Parse each `SnapshotItem.Text`: if it matches `\x60([a-z-]+-[a-z-]+)\x60`, that's the proposed harp ID; strip it from the text.
2. For each item:
   - If proposed harp ID exists in store → update status + text if different.
   - Else hash the stripped text; if hash matches an existing task → update (re-attach harp ID; this happens when LLM dropped the backticks).
   - Else generate a new harp ID via `harp.GenerateName()` (collision-check against current store; regenerate on hit), insert as new task.
3. Any store task whose harp ID didn't appear in the incoming snapshot → move to `## Archived` (or `## Done` if its `status` was `completed` in any item that listed it).
4. Write file atomically (tempfile + rename).

### 1.3 MCP tools — `cmd/mcp_tools_tasks.go`

Five tools, mirroring flesler's tight surface:

| Tool | Params | Behavior |
|---|---|---|
| `task_list` | `statuses?: []string`, `term?: string` | Returns tasks with harp IDs in the text (so the LLM echoes them back). |
| `task_add` | `text: string`, `status?: string` (default `"To Do"`) | Adds a single task. Returns the new harp ID. |
| `task_set_status` | `harp_id: string`, `status: string` | Move between sections. |
| `task_search` | `term: string`, `statuses?: []string` | Substring match across text. |
| `task_summary` | — | Counts per status + active in-progress IDs. |

No `task_delete` by default (per flesler's "ultra-safe" principle): items leave the active list by being moved to `Archived`. A `task_archive` alias of `task_set_status(_, "Archived")` may help LLM ergonomics.

### 1.4 CLI surface — `cmd/tasks_cmd.go`

```
ctxloom tasks list [--status=...] [--term=...]
ctxloom tasks add <text> [--status=...]
ctxloom tasks status <harp-id> <status>
ctxloom tasks capture --stdin    # internal — for the PostToolUse hook
ctxloom tasks summary
```

`ctxloom tasks capture --stdin` parses the Claude Code hook payload (JSON on stdin), extracts `tool_input.todos[]`, calls `Store.Reconcile`. This is the hook entry point.

---

## Phase 2 — Auto-capture hook + `ctxloom-default-tasks` bundle

### 2.1 Inline hook in the bundle YAML

The bundle declares its hooks inline — no shipped shell script files, no separate `hooks.yaml`. The `Bundle` struct gained a `Hooks BundleHooks` field that mirrors `config.UnifiedHooks` (cycle-safe: `internal/bundles` can't import `internal/config`, so the types are duplicated and converted at the boundary).

```yaml
# ctxloom-default-tasks bundle
hooks:
  post_tool:
    - matcher: TodoWrite
      command: ctxloom tasks capture --stdin
      type: command
```

`Config.ResolveBundleHooks()` walks the active default profiles' bundles, extracts each bundle's `Hooks`, tags every entry with `SCM: "bundle:<ref>"` so apply-hooks knows it owns them, and `ApplyHooks` merges the result into `cfg.Hooks.Unified` before writing the backend's settings.json. The existing `PostToolUse` / matcher plumbing in `internal/lm/backends/hooks.go` consumes the merged config unchanged.

**Authoring path:** the canonical home for `tasks.yaml` is `github.com/ctxloom/ctxloom-default`. Until that lands, the bundle can be dropped into `.ctxloom/cache/bundles/ctxloom-default/tasks.yaml` locally for dogfooding — it'll be overwritten on the next sync from upstream, which is fine for the experimental phase.

### 2.2 Skill prompt nudge

Bundle ships a small prompt file `prompts/tasks-guidance.md` that gets merged into the system context via `assemble_context`:

> When you call `TodoWrite`, preserve any backtick-quoted harp ID at the start of each todo's content (e.g. `\x60swift-amber-falcon\x60 finish thing`). The harp ID lets ctxloom's task store track the item across calls. New items don't need an ID — ctxloom will assign one and `task_list` will show it on your next read.

### 2.3 Bundle manifest

`bundles/tasks.yaml`:
```yaml
name: tasks
description: |
  Persistent task tracking. Auto-captures Claude's TodoWrite snapshots,
  stores them in .ctxloom/tasks.md keyed by harp IDs.
prompts:
  - prompts/tasks-guidance.md
hooks:
  - hooks/post-todowrite.sh
```

Ships in `ctxloom-default` remote, so users get it via the default profile.

### 2.4 Capture-side safety

Because the bundle ships a hook (executes code), it lands in the **bundle review gate** on first install — already implemented in `f1262a4`. User reviews the hook before it runs. No new code path needed; this is the dogfood test for the review flow.

### 2.5 Plan-file harp-stamping hook

The same bundle ships a second hook that stamps the active session's harp name into plan files as they're edited, so plans become cross-referenced with sessions automatically.

**Trigger:** `PostFileEdit` (maps to `PostToolUse(Edit|Write)` per `internal/lm/backends/hooks.go:540`), with the script filtering by the existing `planFileRegex` from `internal/memory/plans.go:43` (matches `*-plan.md`, `CURRENT_PLAN.md`, `docs/*-plan.md`).

**Action:** `ctxloom tasks stamp-plan --path <file>` — idempotent rewrite that ensures the file's YAML frontmatter contains a `sessions:` list including `$CTXLOOM_SESSION_HARP`. If no frontmatter exists, one is prepended; existing frontmatter is preserved and only the `sessions:` key is touched. No-op when the harp name is already present.

```yaml
---
sessions:
  - swift-amber-falcon
  - quiet-silver-meadow
---
# Plan title
...
```

**Cross-reference:** `ctxloom session show <harp>` scans plan files in the project for that harp name and lists them under "## Plans worked on", giving the user a session → plans index without a separate database.

**Edge cases:**
- Plan file with non-YAML frontmatter (e.g., starts with `+++` TOML) — leave untouched, log stderr warning, no-op.
- Plan file edited outside an active ctxloom session (`CTXLOOM_SESSION_HARP` unset) — hook short-circuits, no rewrite.
- Plan file lives outside the project — hook still stamps. Worth confirming this is desired; could be tightened to `project_dir` if it produces noise.

**Deferred:** a `ctxloom://plans` resource enumerating plan files with their `sessions:` lists — convenient for resuming "the plan that swift-amber-falcon was working on" without reading individual files. Not v1.

---

## Phase 3 — Session harp-naming + pre-launch resume picker

The LLM never picks its own session name — ctxloom does, before launch. The harp name is generated by `ctxloom run`, persisted to the session index, passed to the LLM via env + MCP `Instructions`, and used by an interactive CLI picker that runs **before** the LLM boots.

### 3.1 Session index

New file: `~/.ctxloom/sessions/index.yaml`

```yaml
sessions:
  - harp_name: swift-amber-falcon
    session_id: 0193e1f2-abcd-7000-...   # populated after backend registers; empty until then
    backend: claude-code
    project_dir: /home/babbitt/workspace/ctxloom/main
    started_at: 2026-05-26T08:14:22Z
    ended_at: 2026-05-26T11:42:01Z       # set by EndSession or inferred from transcript mtime
    transcript_path: ~/.claude/projects/.../1234.jsonl
    summary: "Designed bundle-review-on-startup; landed PR f1262a4."  # one-line essence
    compacted: true
```

### 3.2 Session name lifecycle

Two-phase, because the backend (Claude Code) mints its own session UUID only after it boots:

1. **`ctxloom run` startup, pre-launch**: generate harp name via `harp.GenerateName()`, collision-check against `index.yaml`, append a *pending* entry (`session_id` empty, `started_at` set to `now()`, `project_dir` set).
2. **First MCP `initialize` request from the spawned LLM**: backend session UUID is now known. ctxloom MCP server reads `CTXLOOM_SESSION_HARP` env var, looks up the pending entry, fills in `session_id` and `transcript_path`.

Backfill: existing pre-harp sessions get a harp name lazily on first `list_sessions` after upgrade.

### 3.3 Pre-launch CLI picker — `ctxloom run`

**Not a TUI.** A plain numbered list with a single-keystroke prompt — matches the bundle-review-template aesthetic, no alt-screen, no arrow-key handling, no terminal probing. Implementation: `cmd/session_picker.go` writes to stderr, reads stdin line-by-line, that's it.

Default `ctxloom run` invocation (no flags):

```
$ ctxloom run
ctxloom sessions in /home/babbitt/workspace/ctxloom/main:

 [1] [s✓] [t✓] swift-amber-falcon   2026-05-26 08:14
               session: Designed bundle-review-on-startup; landed PR f1262a4.
               tasks:   3 in progress, 12 done. Focus: harp extraction.

 [2] [s✓] [t·] quiet-silver-meadow  2026-05-25 21:43
               session: (no summary — press d2 to distill)
               tasks:   (no snapshot)

 [3] [s✓] [t✓] bold-crimson-thunder 2026-05-24 09:02
               session: Hardened bundle tools — path traversal, distill state.
               tasks:   (no summary — press d3t to distill)

Choose [1-3] resume · n new · s<N>/t<N> toggle · d<N>[s|t] distill · m more · q quit
> 
```

Each row carries two checkboxes: `[s]` (restore session essence) and `[t]` (restore tasks). Both default to checked when the artifact exists; `[t·]` (dot) means "no task snapshot for this session — not toggleable". On `<N>` resume, the picker passes only the checked-in artifacts to `load_session`.

Keystroke grammar:
- `<N>` — resume session N with current checkbox state
- `s<N>` — toggle the session checkbox on row N
- `t<N>` — toggle the tasks checkbox on row N
- `d<N>` — distill session N's essence (default if no suffix)
- `d<N>s` — distill session essence
- `d<N>t` — distill task snapshot
- `n` — start new session (no carry-over)
- `m` — show more (reveal next batch of older sessions beyond the default horizon)
- `q` (or EOF, or `--no-tty` non-interactive) — quit / fall through to new session

After any toggle or distill, the list re-renders with the new state. No mode switching, no full-screen redraws — just reprint the list and re-prompt.

Resume semantics by checkbox combination:
- `[s✓] [t✓]` — full resume: load essence into LLM context, hydrate frozen tasks into `.ctxloom/tasks.md`.
- `[s✓] [t·or off]` — load essence only; live tasks untouched.
- `[s·or off] [t✓]` — start fresh session context but hydrate the frozen task list. (Lineage recorded — see 3.4.)
- Both off — equivalent to `n` new; the row is functionally ignored. Picker confirms before proceeding so it isn't an accidental keystroke.

Flag bypass mirrors the checkbox state:
- `--session <harp>` — both checkboxes implicitly checked.
- `--session <harp> --no-tasks` — `[s✓] [t·]` equivalent.
- `--tasks-from <harp>` — `[s·] [t✓]` equivalent; new session context, frozen tasks.

Display source: reads `index.yaml` filtered to `project_dir == cwd`, sorted by `started_at` desc. Each row pulls the YAML frontmatter from `essence.md` (session summary) and `tasks.md` (task-snapshot summary) under `~/.ctxloom/sessions/<harp>/`. Missing summary → "(no summary — press d…)" placeholder.

**Default horizon: whichever is shorter — last 10 sessions OR last 7 days.** Computed as `started_at >= now - 7d AND row_index <= 10`. Recency-first, capped, so the picker stays compact for both light and heavy projects. Sessions older than the horizon are hidden but reachable via the `m` keystroke (see below) or the `ctxloom session list --all` CLI.

**`m` keystroke** — "more" — appends the next batch of older sessions to the display (next 10 or +7 days, whichever is reached first). Stackable: press `m` repeatedly to walk back further. Picker re-renders inline; no mode switch. Index/checkbox state for already-visible rows is preserved.

**Flags that bypass the picker:**

| Flag | Behavior |
|---|---|
| `--session <harp-name>` | Resume that session. Error if name not found in index. |
| `--new-session` | Skip picker, start fresh with a new harp name. |
| `--no-tty` | Implies `--new-session` if no `--session` given. Also auto-detected when stdin is not a TTY (CI, piped input). |

If the user picks "new session" or has no prior sessions in this project, a new harp name is generated and printed to stderr: `ctxloom: starting session swift-amber-falcon`.

### 3.4 Echo to LLM

When `ctxloom run` launches the backend, it sets:

```
CTXLOOM_SESSION_HARP=swift-amber-falcon       # the new session's name
CTXLOOM_RESUMED_FROM=quiet-silver-meadow      # source session if resuming, else unset
CTXLOOM_RESUMED_PARTS=session,tasks           # csv of restored artifacts: "session", "tasks", or both
```

`CTXLOOM_RESUMED_PARTS` encodes the checkbox state from the picker:
- `session,tasks` — both restored
- `session` — essence only
- `tasks` — tasks only (fresh session context, lineage to source)
- unset — net-new session, no resume

The MCP server reads these on `initialize` and appends to `ServerOptions.Instructions`:

> Your session is named `swift-amber-falcon`. Refer to it by this name when discussing it with the user. (Resumed from a previous session started 2026-05-26 08:14 with summary: "…". Use `load_session` to pull in the distilled essence and frozen tasks.)

For new (non-resumed) sessions, just the first sentence.

### 3.5 Distillation + YAML-header summary

A session's `essence.md` is regenerated at **session end** (via the `compact_session` MCP tool or an `EndSession` lifecycle hook). It carries a YAML frontmatter that the picker reads:

```markdown
---
harp_name: swift-amber-falcon
session_id: 0193e1f2-abcd-7000-...
started_at: 2026-05-26T08:14:22Z
ended_at: 2026-05-26T11:42:01Z
summary: Designed bundle-review-on-startup; landed PR f1262a4.
project_dir: /home/babbitt/workspace/ctxloom/main
---

# Essence
[full distilled body...]
```

**The `summary` line is part of the long-essence distillation, not a separate run.** The compactor prompt is updated to require: "Output begins with a YAML frontmatter block containing `summary:` — a single line, ≤80 chars, no quotes, capturing the session's purpose. The body follows after the closing `---`." Caller writes the whole blob to `essence.md`; the picker reads only the frontmatter. One LLM call, one file, two consumers.

Same shape for `tasks.md` frozen snapshots: when `compact_session` writes the task-snapshot file, it also pre-pends a YAML frontmatter with a `summary:` line (e.g., `"3 in progress, 12 done. Focus: harp extraction."`). For the task summary we don't need an LLM — derive it deterministically from the task counts + the highest-priority in-progress item's text.

**On-demand distillation from the picker**: when the user keystrokes `d<N>s` or `d<N>t`, the picker shells out to `ctxloom session distill <harp> [--target=essence|tasks]`. The distill command runs the compaction prompt against the saved transcript or task file, rewrites the YAML frontmatter, and the picker re-renders. No background concurrency, no "distilling..." spinner — keystroke is synchronous, blocking, and obvious.

**Lazy distillation at startup is dropped.** It added complexity (background goroutines, refresh logic, horizon enforcement) without clear value over user-driven `d<N>`. If the user wants every old session distilled, `ctxloom session distill --all` handles it as a batch op.

### 3.5.2 Compactor prompt revision

The existing `sessionDistillPrompt` (in `internal/memory/compactor.go:482`) already asks the LLM for a `### Summary` section — we're tightening it into machine-readable frontmatter and reordering the body so resume-relevant content surfaces first.

**Prompt edits:**
- Replace the `### Summary` instruction with a requirement to emit a leading YAML frontmatter block: `---\nsummary: <one line, ≤80 chars, no quotes, no trailing period>\n---`. Style guidance: "like a git commit subject."
- Drop the `### Summary` body heading entirely. The summary lives only in frontmatter; the body starts with the most actionable content.
- Reorder the body sections so resume use cases see the highest-value content first: **Open Items → State → Decisions → Completed → Key Context.**

**Compactor code changes** (three edits to `internal/memory/compactor.go`):

1. **Extend `distilledMeta`** (line 353) with `HarpName`, `StartedAt`, `EndedAt`, `ProjectDir`, `Summary` — all `omitempty`. `Summary` is populated from the LLM-emitted frontmatter; the others from session metadata set at compaction time.

2. **New `parseLLMFrontmatter(out string) (summary, body string)`** helper. Peels a leading `---\n…\n---` block off the output and YAML-unmarshals it into `{ Summary string }`. Returns `("", originalBody)` on any failure (no leading `---`, malformed YAML, missing closing `---`). Idempotent and side-effect-free.

3. **Wire it in `saveDistilled`** (line 366): call `parseLLMFrontmatter`, set `meta.Summary`, write the parsed body.

**One automatic retry** on parse failure. If `parseLLMFrontmatter` returns an empty summary on the first attempt, the compactor re-prompts the LLM with a tighter instruction:

> Your previous output did not begin with a valid YAML frontmatter block. Re-emit the distillation. Begin with EXACTLY `---\nsummary: <text>\n---` and nothing else before that block. The body follows after the closing `---`.

Only one retry — no infinite loop. If the second attempt also fails: save the body with `summary:` empty, log a one-line stderr warning, picker shows the `(no summary — press d…)` placeholder, and the user can trigger `d<N>` manually to try again.

**Edge cases:**
- Summary >80 chars: truncate at 80 with no ellipsis, log warning. Prompt asks but we don't trust the LLM blindly.
- Summary contains newlines: take the first line only; rest discarded with a warning.
- YAML escaping (colons, quotes) handled by `yaml.Marshal` on the way out; nothing special on the way in beyond `yaml.Unmarshal`.

### 3.5.1 Status bar / meta hud integration

`ctxloom meta hud` (the existing status-line command — see `cmd/meta.go` if present, otherwise `cmd/hud.go`) reads `CTXLOOM_SESSION_HARP` from its parent process environment and includes the harp name in its output. Example:

```
ctxloom · swift-amber-falcon · 12 tasks · main
```

Same env var also feeds into the LLM's window title where the backend supports it (Claude Code's `set_title` MCP capability, terminal escape via `\033]2;…\007` as fallback). The harp name is therefore visible in three places at all times: the LLM's context (via `Instructions`), the status bar, and the terminal title.

### 3.6 Compaction artifacts

`compact_session` writes under `~/.ctxloom/sessions/<harp-name>/`:
- `essence.md` — distilled summary with YAML header (3.5)
- `tasks.md` — frozen snapshot of `.ctxloom/tasks.md` at compaction time
- `plans.md` — output of `RenderPlans(ExtractPlans(session))` (already exists, just relocate)
- `transcript.jsonl` — symlink or copy of the backend transcript (so the source survives backend GC)

### 3.7 Resume mechanics

`load_session` accepts `harp_name` (preferred) or `session_id`. On load:
- Display `essence.md` body + `plans.md` to the LLM.
- Surface `tasks.md` as "frozen tasks from <harp-name>" — do **not** overwrite the live `.ctxloom/tasks.md`. New `task_restore <harp-name>` tool explicitly merges (creating archive entries for tasks that conflict).
- `list_sessions` returns `harp_name`, `summary`, `started_at` alongside `session_id`.
- `browse_session_history` mirrors the picker output for in-LLM browsing.

### 3.8 CLI session-management surface

```
ctxloom session list [--project=cwd|--all]
ctxloom session show <harp-name>           # prints essence.md
ctxloom session distill <harp-name>        # force re-distill
ctxloom session rename <old> <new>         # in case of collision or vanity rename
ctxloom session forget <harp-name>         # drop from index (keeps transcript)
```

These also cover the case where the user wants to browse without launching the LLM.

---

## Phase 4 — MCP footprint reduction (Claude-first)

Cut ctxloom's MCP tool surface from ~41 (today) to ~17 (post-tasks), without losing functionality. Target: Claude Code as primary client; weaker-resource clients (Cursor, Codex) get gentle degradation, not full feature loss. Phased rollout per lever — observe, iterate.

### 4.1 Lever A: read-only listings → MCP resources (~12 tools removed)

Convert tools that return collections or single records to MCP resources:

| Today (tool) | Tomorrow (resource URI) |
|---|---|
| `list_bundles` | `ctxloom://bundles` |
| `list_fragments` | `ctxloom://fragments` |
| `list_profiles` | `ctxloom://profiles` |
| `list_remotes` | `ctxloom://remotes` |
| `list_mcp_servers` | `ctxloom://mcp-servers` |
| `list_prompts` | `ctxloom://prompts` |
| `list_sessions` | `ctxloom://sessions?project=cwd` |
| `get_fragment` | `ctxloom://fragments/{name}` |
| `get_prompt` | `ctxloom://prompts/{name}` |
| `get_profile` | `ctxloom://profiles/{name}` |
| `browse_remote` | `ctxloom://remotes/{name}/contents` |
| `browse_session_history` | `ctxloom://sessions/recent` |

Implementation: `cmd/mcp_resources.go` registers a single `ListResources` handler that enumerates from the existing config/registry, plus a `ReadResource` handler that dispatches by URI prefix. Resource MIME types: `application/yaml` for structured, `text/markdown` for content bodies. Reuses the existing config/registry code paths — today's `list_*`/`get_*` tools are thin wrappers, so the conversion is mostly mechanical.

### 4.2 Lever B: rare write ops → CLI-only (~14 tools removed)

The following stop being MCP tools and become CLI-only:

| Removed tool | CLI equivalent |
|---|---|
| `add_remote`, `remove_remote`, `update_remote` | `ctxloom remote add/remove/update` |
| `discover_remotes`, `search_remotes` | `ctxloom remote discover/search` |
| `pull_remote` | `ctxloom remote pull` |
| `push_bundle` | `ctxloom bundle push` |
| `add_mcp_server`, `remove_mcp_server`, `set_mcp_auto_register` | `ctxloom mcp add/remove/auto` |
| `create_profile`, `update_profile`, `delete_profile` | `ctxloom profile create/update/delete` |
| `create_fragment`, `delete_fragment` | `ctxloom fragment create/delete` |

The LLM reaches these via `Bash("ctxloom <cmd> ...")`. The `ctxloom-default` bundle ships a skill prompt that surfaces the available subcommands so the LLM picks Bash over MCP-tool discovery.

`create_bundle`, `update_bundle`, `delete_bundle` **stay as MCP tools** — they're frequent enough during authoring sessions to earn their tool tax, and they integrate with the review gate via the same middleware pipeline.

### 4.3 Lever C deferred (action-enum collapse)

Skipping for now. Discriminated-union schemas would save ~6 more tools but cost LLM-selection clarity. Revisit after dogfooding A + B.

### 4.4 Task surface tightening (5 → 3 tools)

From the tasks plan:
- `task_list({term?, status?})` — absorbs `task_search` (search is just list with filter)
- `task_add({text, status?})` — unchanged
- `task_set_status({harp_id, status})` — unchanged
- `task_summary` — becomes `ctxloom://tasks/summary` resource (or `summary: true` flag on `task_list`)

End state: 3 task tools instead of 5.

### 4.5 Footprint math

| Stage | Tools |
|---|---|
| Today | 41 |
| + Phase 1–3 task tools (5 new) | 46 |
| − Phase 4.1 listings → resources | 34 |
| − Phase 4.2 writes → CLI | 20 |
| − Phase 4.4 task tightening (5→3) | 18 |
| **Net post-Phase-4** | **~18** (vs. 46 naïve) |

### 4.6 Risks & mitigations

- **Resource discoverability.** Some clients don't auto-discover resources well. Mitigation: `ListResources` always returns a synthetic entry `ctxloom://help` whose body documents every available URI, so even an LLM that doesn't list resources can be prompted to read the help.
- **CLI demotion fluency.** LLMs might fumble Bash invocation for ops they're used to as tools. Mitigation: `ctxloom-default` bundle ships a skill prompt with a flat list of subcommands + examples. Validation: run a manual session, ask the LLM to "add a new remote called X" — confirm it reaches for `Bash("ctxloom remote add X ...")`.
- **Non-Claude clients.** Cursor and Codex resource support is weaker. Mitigation: Phase 4 is staged. Ship Lever A first, observe other-client behavior, fall back to keeping some listings as tools (config-gated, opt-in) if needed.
- **Bundle review gate coverage.** Some removed MCP tools (`pull_remote`, `discover_remotes`) interact with the review gate. After demotion to CLI, the gate still triggers correctly because sync runs through `operations.SyncOnStartup` regardless of entry point — verified during PR E2.

### 4.7 PR sequencing

Phase 4 splits into three PRs:

- **PR E1 — Lever A (resources).** Self-contained. Adds resource handlers, removes the 12 listing tools, adds `ctxloom://help`. Ships first because it's the lowest-risk and benefits all clients that support resources at all.
- **PR E2 — Lever B (CLI demotion).** Removes the 14 write tools, ships `ctxloom-default` skill update, adds dogfood test ("LLM completes a config task using only Bash + CLI").
- **PR E3 — Task tightening (4.4).** Bundled into PR B if PR B hasn't shipped yet; standalone PR otherwise.

E1 and E2 are independent of each other and of E3. E3 depends on whichever PR introduces the 5-tool task surface.

---

## Testing

- **Unit `internal/tasks/store_test.go`** — round-trip parse/write preserves arbitrary user notes; section ordering stable; harp IDs survive a load-modify-save cycle.
- **Unit `internal/tasks/reconcile_test.go`** — harp-id match wins over text-hash; first-sight items get new IDs; archive on disappear; status-change-to-completed routes to `## Done`; collision regeneration; LLM-stripped-backticks recovered via text-hash.
- **Unit `cmd/mcp_tools_tasks_test.go`** — JSON Schema validation for the five tools; `task_list` includes harp IDs in the rendered text.
- **Unit `internal/tasks/capture_test.go`** — hook stdin payload (matching Claude Code's actual JSON shape) parses and reconciles correctly.
- **Integration** — bundle install → hook fires → `task_list` reflects the TodoWrite. End-to-end via the existing wire-protocol test rig from `cmd/mcp_review_integration_test.go`.
- **Property** (harp) — see Phase 0.4.
- **Unit `cmd/session_picker_test.go`** — index parsing; filter by `project_dir`; sort order; flag-bypass paths (`--session`, `--new-session`, `--no-tty`); non-TTY auto-detect; behavior with empty index.
- **Unit `internal/sessions/lifecycle_test.go`** — two-phase name assignment (pending → bound on MCP `initialize`); collision regeneration; lazy distillation horizon enforcement.
- **Integration** — `ctxloom run --session <harp>` resumes with the right `CTXLOOM_SESSION_*` env vars set; MCP `Instructions` contains the harp name; `load_session` returns frozen tasks separately from live `.ctxloom/tasks.md`.
- **Manual** — install `ctxloom-default-tasks`, observe review gate, approve, watch a real Claude session populate `.ctxloom/tasks.md`. `/clear`, run `load_session <harp-name>`, confirm frozen tasks appear. Run a second `ctxloom run`, see the picker, resume, confirm the LLM addresses the session by its harp name.

---

## PRs (in order)

1. **PR A — `internal/harp/` + `ctxloom harp` CLI.** Phase 0. Self-contained package, no consumers yet.
2. **PR B — `internal/tasks/` + MCP tools + `ctxloom tasks` CLI.** Phases 1.1–1.4. Imports `internal/harp`. No hook, no bundle, no session work yet. Pure storage + tool surface. Tests at unit level.
3. **PR C — Auto-capture bundle.** Phase 2. Ships `ctxloom-default-tasks` bundle with hook + prompt; relies on the review gate from `f1262a4`. Adds the integration test.
4. **PR D — Session harp-naming + picker.** Phase 3. Adds `index.yaml` + two-phase lifecycle, `cmd/session_picker.go` (bubbletea TUI), `ctxloom session` CLI surface, `--session`/`--new-session`/`--no-tty` flags on `ctxloom run`, `CTXLOOM_SESSION_HARP` env plumbing, MCP `Instructions` injection of the harp name, YAML-frontmatter summaries on `essence.md`, lazy startup distillation. Threads harp names through `RegisterSession`, `list_sessions`, `load_session`, `compact_session`, `browse_session_history`. Backfill: existing sessions get a harp name lazily on first `list_sessions` after upgrade.

PR A is independent. PR B depends on A. PRs C and D are independent of each other but both depend on B.

---

## Deferred / follow-ups

- **`task_link_session <harp-id> <harp-session-name>`** — explicit task↔session association for cross-referencing.
- **Cross-project task search** — `~/.ctxloom/tasks-index.yaml` aggregating project task files for `ctxloom tasks list --all-projects`.
- **Reminders section** (flesler has this — a status the LLM constantly sees). Skip in v1; revisit if useful.
- **`AUTO_WIP` enforcement** — at most N items in `In Progress`. flesler enforces this; we leave it as user discipline for now.
- **Status customization via config** — flesler exposes `STATUS_WIP`, `STATUSES`, etc. as env vars. Skip in v1; hardcode `In Progress` / `To Do` / `Done` / `Archived` and revisit.
- **HTTP transport for the task store** — flesler offers this. Out of scope; ctxloom is stdio-MCP.

---

## Open items resolved

- **harp impl:** fully native Go, built inline at `internal/harp/`; extract to `benjaminabbitt/harp-go` later when API stabilizes. Not the existing WASM binding.
- **Storage:** single-file flesler-style markdown at `.ctxloom/tasks.md`.
- **Reconciliation:** harp-id-in-text primary, sha256(text)[:12] fallback.
- **Auto-capture channel:** `PostToolUse(TodoWrite)` hook shipped by `ctxloom-default-tasks` bundle.
- **Session naming:** harp name minted by `ctxloom run` at startup (pre-LLM), bound to backend session UUID on first MCP `initialize`; tasks + essence frozen under that name on compaction; `load_session` accepts harp name.
- **Session-name discovery by LLM:** via `CTXLOOM_SESSION_HARP` env var consumed by the MCP server and surfaced through `ServerOptions.Instructions`. Not a tool call.
- **Plan-file stamping:** plan markdown edits trigger a hook that records the active harp name into the file's YAML frontmatter. Sessions and plans cross-reference automatically; `ctxloom session show <harp>` lists associated plans.
- **Resume UI:** line-based numbered picker in `ctxloom run` (pre-launch), per-row `[s]`/`[t]` checkboxes toggled via `s<N>`/`t<N>` keystrokes; bypassable via `--session`/`--new-session`/`--no-tasks`/`--tasks-from`/`--no-tty`.
- **One-line session summary:** YAML frontmatter on `essence.md`, produced as part of the long-essence distillation in a single LLM call (compactor prompt requires the frontmatter). Picker reads only the frontmatter. Task-snapshot summaries are derived deterministically from counts + top in-progress item — no LLM needed for those.
- **On-demand distillation:** `d<N>` keystroke in the picker shells out to `ctxloom session distill`, blocking + synchronous. No background distillation; no startup horizon. Batch op via `ctxloom session distill --all` if the user wants it.
- **Picker UI shape:** line-based numbered list with single-keystroke prompt, *not* a bubbletea TUI. Renders to stderr, reads stdin line-by-line.
- **Status-bar / window-title:** `ctxloom meta hud` and terminal title both surface the harp name via `CTXLOOM_SESSION_HARP`. Three visibility points: LLM context, status bar, terminal title.

## Open items remaining

- Exact JSON Schema shapes for the five `task_*` MCP tools — write during PR B against the conventions from `cmd/mcp_tools_bundles.go`.
- Whether `task_archive` should be a separate tool or just a status (mild ergonomic question; revisit when wiring LLM prompts).
- How `load_session` surfaces "frozen tasks" — inline content vs. a hint to call a new `task_restore` tool. Probably both: short summary inline, explicit restore tool for the merge.
- Whether existing pre-harp sessions get retroactive harp names (probably yes, lazily, on first `list_sessions` after upgrade).
- Bundle install ordering: if `ctxloom-default-tasks` is in the default profile, does first-run install hit the bundle-review gate before any session has been seen? (Should be fine — review gate fires regardless of session state.)
- Window-title fallback when the backend doesn't expose a `set_title` MCP capability — emit the OSC2 escape (`\033]2;<harp>\007`) before backend launch from `ctxloom run`. Confirm terminals that don't render OSC2 don't barf on it. (xterm/iTerm2/alacritty/WezTerm all fine; check Windows Terminal.)
