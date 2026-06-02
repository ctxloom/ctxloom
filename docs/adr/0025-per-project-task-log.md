# 0025 — Append-only per-project task log with origin-session provenance and project-identity keying; defer the movement layer

**Date:** 2026-06-02.

## Status

Accepted (the store: append-only per-project log, harp identity scoped to the log, project-identity keying, the private/distributable wall) and Deferred (the movement layer: `repoint`, dump/load, copy-fork seeding).

## Context

Tasks have three storage paths today and a migration that shuttles between them:

- legacy project-local `<projectDir>/.ctxloom/tasks.md`,
- per-session `~/.ctxloom/sessions/<harp>/tasks.md`,
- and `OpenSession` (`internal/tasks/session.go`), which on first access *moves* one forward into the other — legacy → harp on a fresh session, prior-harp → new-harp on resume-with-tasks.

Continuity is therefore achieved by *moving the file*. That has two failures. It strips a task from the session that gave it meaning: once the text is carried forward, the plan, transcript, and reasoning that defined "implement the storage layer" are no longer reachable from the task. And it cannot reconcile fan-out: continue one session twice and whichever resume runs first takes the file; the two lineages diverge with no merge.

Projects are identified only by a raw filesystem path. `sessions.Entry.ProjectDir` stores the path; `ListForProject` recovers prior sessions by string-equality on it. A move or rename scatter-breaks that match across every entry at once, and "the previous session in this project" silently stops resolving.

Three prior ADRs frame the task surface, and this decision honors all three:

- **0006** deferred an explicit `task_link_session` tool, relying on implicit plan-stamping, with a revive trigger that is *exactly this work*: "revisiting an old session's task from a fresh session."
- **0007** kept tasks strictly per-project and rejected a cross-project aggregation index.
- **0013** ships the task workflow as an always-on embedded bundle.

The goal: one task store that preserves the task→context bond, survives project move/rename, reconciles fan-out, and never mixes private working state with distributable artifacts.

## Decision

### Core (accepted)

1. **One append-only log per project, home-rooted, keyed by a stable project-id** — `~/.ctxloom/tasks/<project-id>.jsonl`, where the id is a harp, not a path. This replaces all three current paths. Keying by identity rather than path is what lets a project move or rename without breaking the store.

2. **Events reference their origin session harp.** A task keeps its own identity harp (today's `Task.HarpID`); the `add` event additionally records the session harp active at creation (from `CTXLOOM_SESSION_HARP`), and `status` events record the session that changed them. This makes 0006's implicit link direct: the task points home to the session directory that holds its plans, transcript, and essence. Context travels by reference, never by copy, so it can never be stripped.

3. **Task identity is a harp, scoped to the project log.** Uniqueness is owned by the log, not the global allocator: mint by folding the log's issued-harp set and redrawing on collision (extend `uniqueHarpID` to check the whole log, file-locked on it). Task uniqueness must not depend on `AssignHarp`'s session-index check — a future change there must not be able to break task identity. The post-100-draw silent fallback and the concurrent-mint race are repaired the same way: the fold detects a duplicate `add` (same harp, different origin or text) and appends a `rekey` event. A harp, once issued, is never freed; compaction preserves the full issued set so references stay stable.

4. **Project identity: a gitignored in-tree marker plus a home registry.** A small `<projectDir>/.ctxloom/project-id` marker carries the id; a home registry maps `project-id ↔ current path`. The marker is gitignored because it is private working state and must not ride a distributable tree (see the wall, below). Resolution is path-first: look the path up in the registry; on a miss, read the marker and resolve by id; on an id match whose registered path differs, self-heal by re-pointing.

5. **Move vs copy — re-point only on a proven move.** When the marker resolves an id whose registered path differs from the launch path, probe the old path before acting:
   - old path gone, or its marker missing/changed → **move** → re-point silently;
   - anything short of that proof — old path still live with the *same* id (a copy), **or** the probe inconclusive (unreachable path, unmounted volume, permission or I/O error) → **fork**: mint a new id into the current tree's marker, new registry entry, fresh log, and warn. Re-pointing happens only when the original is provably gone; short of that, fork rather than risk two trees writing one private log. A fork is non-destructive — the old id's log is untouched — so a spurious fork (a genuine move whose old path was momentarily unreachable) costs only a fresh empty log the human reconnects via the movement layer. No oscillation tracking and no guessing: prove the move, or fork.

6. **The private/distributable wall.** Distributable artifacts (bundles) and private working state (tasks, session context, the project-id marker, dump/load output) never share a file, a tree, or a subsystem. Bundles already carry no task or context content — their structs, loaders, and operations never read the task store, plans, essence, or transcripts. This ADR states that separation as an invariant rather than an accident. Bundles and dump/load share a surface mechanic (package a directory) but serve opposite goals — outward distribution versus private relocation — so they stay separate subsystems; merging them would bleed distribution semantics (trust, review) onto private movement.

### Mechanics

- **Event schema** (one JSON object per line): `add {task, text, session, ts}`, `status {task, status, session, ts}`, `edit {task, text, ts}`, `rekey {from, to, ts}`. Current state is the fold of the log, last-write-wins per task harp; provenance is the immutable `add` event's `session`.
- **Resolution** is the path-first / marker-fallback / self-heal flow of decision 4, with the move-vs-copy probe of decision 5 on the differing-path branch, and new-project minting (id + marker + entry) when no marker and no path match.
- **Fault tolerance** (per `CLAUDE.md`): a malformed event warns and is skipped, never blocking `task_list`; an unresolvable project identity warns and degrades, never blocking startup; a harp that cannot be cleanly minted is rekeyed, never silently collided.

## Relationship to prior ADRs

- **0006 (skip `task_link_session`)** — this trips its revive trigger verbatim. The implicit plan-stamp link becomes a direct origin-session reference on the task event. The *link* is superseded; the *tool* is still not added — per 0024 the reference is recorded at add time, not via a bespoke MCP tool.
- **0007 (skip cross-project task search)** — preserved. Scope stays strictly per-project; `task_list` sees only the current project's log. The project *registry* resolves identity (id↔path); it is **not** the cross-project task *aggregation index* 0007 rejected, and no `--all-projects` surface is added.
- **0013 (keep tasks bundle embedded)** — unaffected. The task workflow still ships as the always-on embedded bundle; only what its hooks *write to* changes (the log instead of `tasks.md`). User task *data* still never enters a distributable bundle — that is the wall, and 0013 is about feature *delivery*, not data.
- **0024 (minimize MCP surface)** — `task_list`, `task_add`, `task_set_status` stay as the operational MCP tools; their backend changes, their surface does not. The movement layer (`repoint`, dump/load, registry management) is CLI-only, exactly as 0024 prescribes.

## Consequences

- Three storage paths collapse to one; move-on-resume carry-forward is removed. Tasks decouple from the session chain — the parent / `get_previous_session` machinery stays, but only for narrative and essence recovery, which is the one place recursion belongs.
- Project-id normalizes the raw `ProjectDir` out of the places it is scattered. A move or rename touches one registry record instead of every session entry.
- Fan-out reconciles: concurrent continuations append to one log, the fold merges, and completion is authoritative once.
- Migration: replay the existing `tasks.md` files (legacy project-local and per-session) into the log as `add` events; the origin session reference is the harp directory the file was found in, and unset for the legacy project-local file.

**Revive trigger (movement layer — `repoint`, dump/load):** the core store works on one machine without them. Revive `repoint` when a project moves or renames on the same machine and the marker probe needs a manual override; revive dump/load when context must move where the registry cannot see it (another machine) or to seed a copy-fork. Both are CLI, both honor the wall.

**Revive trigger (fork log seeding):** until dump/load lands, a fork — whether a detected copy or an inconclusive probe — starts an *empty* log with a warning. Revive to optionally seed the fork from the contested id's log via dump/load.

**Revive trigger (session entries referencing project-id):** the registry resolves identity today without rewriting every `Entry.ProjectDir`. Revive when path drift across a project move actually breaks session matching in practice, rather than pre-emptively.
