# 0024 — Minimize the MCP surface to operational session tools; front-load configuration via CLI

**Date:** 2026-06-02.

## Status

Accepted.

**Partially superseded** by the trust-simplify work (Slice 3, commit `192d4ef`).
The `bundle review|approve|decline|show-pending` family named below — and the
`CTXLOOM_AUTO_APPROVE_BUNDLES` escape hatch — were removed. Per-item content
review now happens at exposure through the content-hash trust gate and the
single `ctxloom review` porcelain (see [trust-model.md](../trust-model.md)); a
trusted source is set with `ctxloom remote trust`, and dependency pins moved to
`ctxloom bundle hold`/`unhold`. The core decision — keep the MCP surface small
and route management through the CLI — still stands; only those specific command
names have changed.

## Context

The MCP server had grown to ~20 tools and 13 resources that mixed two audiences.
Some tools are what the model genuinely needs *while a session is running* — pull
context, recover prior work, track tasks. The rest were configuration and
management: creating/editing bundles, profiles, fragments, and prompts; syncing
remotes; reviewing, approving, trusting, and pinning remote bundle changes;
applying hooks. These bloated the model's tool list, and the bundle-review flow
went further — a middleware *blocked the session* until the model echoed a review
template and called the right tool, turning a configuration decision into an
in-conversation interruption.

ADR [0019](0019-cli-pure-frontend.md) already established that every frontend is a
thin shell over `internal/operations`, so the same operation is reachable from the
CLI and from MCP with no duplicated logic. That means a management capability does
not need to live in MCP to exist — the CLI already covers it (or trivially can).
What was missing was a principle for *which* capabilities belong in MCP at all.

## Decision

**The MCP server exposes only functionality that is genuinely useful operationally
— things the model does mid-session to retrieve or track context. Configuration and
management do not get bespoke MCP tools; they live on the `ctxloom` CLI. The model
still reaches them — it runs `ctxloom` like any other shell command, so it can
manipulate configuration mid-session when a task genuinely calls for it — they are
just not part of the MCP tool surface.**

The preference is to front the decision: which profiles, which remotes, what to
trust are best settled at `ctxloom init` (before the model launches) or
deliberately via the CLI between sessions, rather than surfaced as mid-session
friction — the same "front the decision" stance as the startup review/trust
prompts. But that is guidance, not a wall. Nothing is out of reach; management is
simply expressed as CLI invocations (which the model can make) instead of as a
parallel set of MCP tools that bloat the model's surface and duplicate the CLI.

Routing the model through the same CLI the user runs does two things: it keeps the
MCP surface small, and it **unifies the two actors on one path**. A configuration
change behaves identically — same validation, same on-disk result, same output —
whether the user typed it or the model invoked it. There is no second way to do it
that could drift from the first.

MCP keeps exactly these 10 tools:

- **Retrieve/load context:** `assemble_context`, `search_content` (installed
  here), `search_library` (installable across remotes — renamed from
  `search_remotes`).
- **Session history:** `compact_session`, `load_session`, `recover_session`,
  `get_previous_session`.
- **Task tracking:** `task_list`, `task_add`, `task_set_status`.

Removed from MCP (CLI is the home):

- Bundle authoring: `create_bundle`, `update_bundle`, `delete_bundle` →
  `ctxloom bundle create|edit|delete`.
- Sync/hooks: `sync_dependencies`, `apply_hooks` → `ctxloom remote sync`,
  `ctxloom hook apply`.
- Review/trust/pin: `acknowledge_bundle_review`, `decline_bundle`,
  `approve_remote_pending`, `show_bundle_verbatim`, `trust_remote`, `pin_bundle`,
  `unpin_bundle` → `ctxloom bundle review|approve|decline|pin|unpin|show-pending`
  and `ctxloom remote trust|untrust`.

The in-chat review gate (middleware + in-memory `bundleReviewState` + the
review-protocol instruction) is deleted. At MCP startup, sync auto-applies trusted
remotes, leaves the rest pending, and applies hooks against the **active
(approved)** lockfile only — `ApplyHooks` never reads pending content, so an
unreviewed bundle's hooks never run. Pending changes are surfaced for CLI review at
`ctxloom run` startup; `CTXLOOM_AUTO_APPROVE_BUNDLES=1` still merges them for
non-interactive runs.

The SessionStart onload preamble and the MCP server `Instructions` both state the
boundary: management is via the `ctxloom` CLI; the MCP tools are only for searching
and loading fragments, prompts (skills), and history, plus tasks.

## Consequences

- The model sees a small, purpose-built tool list, and the session is never blocked
  on a review. Configuration is preferably fronted at init, but the model can still
  run the CLI mid-session when needed.
- No capability is lost — every removed tool has a CLI equivalent over the same
  `internal/operations` core (ADR 0019), so there is still one implementation per
  operation, exercised identically by the user and the model.
- Read-only **resources** are kept (e.g. `ctxloom://remotes`,
  `ctxloom://mcp-servers`). They are not management *actions*, and discovery — an
  init/pre-launch activity — reads `ctxloom://remotes`. Removing them would regress
  discovery for no surface-area gain. This is a deliberate scope line: the
  reduction targets session-time *actions*, not read views.
- Hook safety no longer depends on a session-blocking gate; it depends on the
  invariant that hooks run only against approved (active) content. A future change
  that makes `ApplyHooks` read pending content would reintroduce the risk.

**Revive trigger:** the model can already invoke any CLI command mid-session, so a
new MCP tool is justified only when shell invocation is genuinely inadequate — e.g.
the model needs a structured/validated return value or streaming feedback the
CLI's text output can't provide. "The model needs to do X mid-session" is not
sufficient on its own; X is already reachable via the shell. Absent that, new
management capabilities are CLI-only.
