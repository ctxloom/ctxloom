# Behaviour changes in 0.7.0-pre001

**Read this if you script ctxloom, taskloom or ltk, or gate CI on their exit codes.**

Most changes below are the same shape: a command that **failed and reported success**
now fails loudly. Nothing here is a new capability — each is a case where the tool was
lying about an outcome. If a command starts failing after this upgrade, it was almost
certainly failing before, silently.

ctxloom breaks rather than shims. Breaking **silently** is what it does not do — hence
this page.

Sources: the architecture review's findings register (`FINDINGS.md`), the complete census
(`docs/architecture/findings-index.md`), and the remediation sweep on
`chore/findings-sweep-1`.

---

## The one rule behind almost all of it

An operation that cannot determine what to do now says so, instead of doing nothing and
reporting success.

**Legitimately empty is still success.** An empty task list, a project with no remotes, a
filter matching nothing, nothing to uninstall — all still exit 0. The distinction drawn
throughout is between *"nothing to do"* (fine) and *"could not work out what to do"*
(now an error). Where the code could not previously tell those apart, teaching it to was
the fix.

---

## Exit-code changes

### Context assembly and delivery

| Surface | Before | Now |
|---|---|---|
| Any command assembling context where fragments exist but all resolve empty (`ctxloom run`, `ctxloom init`, `hooks apply`, codex delivery) | exit 0, zero context delivered | fails with `ErrNoContext` |
| `ctxloom hook inject-context` with a missing context file | silent | warns; **exit code unchanged** |
| `session/set_mode` over a zero-byte assembly | ok, lead context blanked | RPC error |
| A kiro or antigravity run whose context hash cannot be resolved | exit 0, steering file **removed** / AGENTS.md section **stripped**, launched with zero context | non-zero; the previous delivery is left intact |
| A `session_end` hook configured while running codex | written nowhere, said nothing | still written nowhere (codex has no such event) — now **warns** naming engine and kind; **exit code unchanged** |

**Why:** a run that silently delivers no context looks identical to a working one. The
"no fragments configured" case is still a legitimate success.

### Bundles, profiles and signing

| Surface | Before | Now |
|---|---|---|
| Loading an empty / truncated / comment-only / `---`-only / `null` `bundle.yaml` | accepted as a valid empty bundle | `bundle is empty` error |
| `ctxloom bundle distill` / `copy` / `import` over such a file | exit 0, zero items | non-zero |
| `ctxloom bundle push` of a contentless bundle | published (**and signed**) zero bytes | non-zero |
| `ctxloom sign --all` / `bundle sign --all` finding nothing | exit 0, "no local bundles to sign" | non-zero, naming the directories searched |
| `ctxloom <item> list --bundle X` for an unknown `X` | exit 0, "Fragments (0):" | non-zero (a real-but-empty bundle still exits 0 with a message) |
| `ctxloom <item> edit` saved from an empty editor buffer | exit 0, "Updated …", item destroyed | non-zero, item unchanged |
| `profile create` / `save` with no content at all | exit 0 | non-zero (labels-only profiles still save) |
| `profile edit` saved from an empty editor buffer | exit 0, "updated", profile truncated to 0 bytes | non-zero, profile unchanged |
| `profile import` of an empty / whitespace / comment-only / `null` file | exit 0, "imported" | non-zero |
| `profile import` where the destination cannot be statted | overwrote it without `--force` | non-zero |
| `profile export` of such a profile | exit 0, "exported", 0 bytes shipped | non-zero |
| A pinned remote bundle whose content is empty | vanished silently from assembly | strictness finding |

**Version-only bundle skeletons remain publishable.** Only a document declaring nothing
at all is refused.

### Configuration

| Surface | Before | Now |
|---|---|---|
| `CTXLOOM_CONFIG_<INT_KEY>=0` or `=1` | coerced to a **bool**, which then failed the yaml decode of the entire merged config, degraded to a warning — silently discarding your whole layered config | applied as an integer |
| A `config.yaml` that exists but defines no keys | silent | warning naming the path; exit unchanged |
| An unparseable codex `config.toml` | degraded to an empty table which callers wrote back, **replacing every user key** | non-zero |
| `exclude_mcp` against a builtin or companion MCP server | silently ineffective | works |

`CTXLOOM_CONFIG_AGENT_TURN_CAP=1` was the worst of these: it arrived as `true` and took
the whole config layer down with it.

### Companions — ltk and taskloom

| Surface | Before | Now |
|---|---|---|
| `ltk manage install` when the engine yields empty settings, or the shipped rule set contains no rules | exit 0 — installing a **permit-everything guard** | exit 1 (`--no-default-rules` unaffected) |
| An ltk **deny** rule with neither `message` nor `suggest` | loaded; fired with no `permissionDecisionReason` — the agent saw a bare `deny` and retried | load error (`suggest` alone is enough; `allow` and `mode: disable` rules are exempt) |
| `ltk check` where `.gitmodules` exists but cannot be read, or a submodule path is not a valid glob | exit 0, `@submodules` expanded to nothing, verdict `allow` | exit 1 |
| `ltk evaluate` (the hook) in the same situation | allowed everything the rule was written to guard | **fails closed** — denies until fixed, same as its config-load branch |
| `taskloom manage install` / `uninstall` with an empty config payload | exit 0, user config truncated | exit 1 |
| `taskloom manage uninstall` with no backend detected | printed nothing, exit 0 | **still exit 0**, now says `nothing to remove` |
| `taskloom plan list` | listed plans from **every project on the machine** | scoped to the current project; `--global` restores the old breadth |
| `taskloom plan list` (text output) | 3 columns | **5 columns** (adds project and path) |
| `taskloom plan list` over an unreadable session dir or plan file | exit 0, short list | non-zero |

`taskloom plan list` gaining `path` means its output now composes with `plan show`.
Plans whose session cannot be attributed to a project are **always shown**, marked `-`,
in scoped and global listings alike — they are never hidden.

### Sessions, memory and transcripts

| Surface | Before | Now |
|---|---|---|
| `ctxloom compact` / `compact_session` when the LLM exits 0 with no output | success, writing an **empty essence over a good one** | non-zero |
| An empty session being distilled | overwrote a real essence and its index summary with a placeholder | existing essence kept |
| `SetSummary` with an empty summary | erased the summary, its detail, and the staleness fingerprint | refused |
| `ctxloom session backfill` over a transcript parsing to zero entries | exit 0 | non-zero (admin-only files still exit 0) |
| Oneshot capture with content but no harp | exit 0, nothing written | non-zero |
| `ListSessions` / `GetSession` on the `acp` backend | exit 0, empty list — indistinguishable from "none" | `backend acp has no session history` |
| `/recover` on opencode when `opencode export`'s JSON shape has drifted | exit 0, empty scrollback | non-zero, naming the drift (a real session with zero messages still exits 0) |

### Agents and coordination

| Surface | Before | Now |
|---|---|---|
| `agent_run` when the launch has already failed | `"spawned <harp>"` | an explicit failure disposition. **The success wording is byte-identical** — anything matching `"spawned"` is unaffected |
| `agent_send` with a structured payload that cannot be encoded | ok + message id, payload silently stripped | `InvalidArgument` |
| `ctxloom run -p <typo>` or an unloadable bundle ref | exit 0, zero MCP servers / hooks / commands / skills delivered | strictness finding — warns always, aborts in strict mode, `--degraded` downgrades |
| Transcript export / clipboard copy of an all-notice feed | wrote a **0-byte file** reporting `saved`; clipboard copy emitted OSC 52 **clear** while reporting `copied` | both refused |
| A transcript export whose `kind` is unknown | wrote a file whose extension lied about its contents | refused |
| `ctxloom run --print`, `map`, `weave`, a delegated turn or `acp client` where the engine exits 0 with **no output** | exit 0, empty report / empty `Part.Output` / empty assistant turn | non-zero, carrying the engine's stderr (in a fan this is one member's error `Part`, not the whole call) |
| A `map`/`weave` member (or delegated child) whose **named profiles** assemble to nothing | ran context-free, produced plausible output | non-zero — a run naming no profile is still legitimately context-free |
| `ctxloom manage hooks uninstall --backend <typo>` | `Status: "removed"` listing the typo, nothing removed | non-zero, naming the supported backends |

### Build and generator gates (contributors)

| Surface | Before | Now |
|---|---|---|
| `cmd/validate` (build prerequisite) | exit 0 **having validated zero files** in CI on every run | exit 1 when zero documents validate |
| `just gen-schemas` | exit 0 with an empty target list | exit 1 |
| `just gen-mcp-schemas` | exit 0 with an empty binding table | exit 1 |
| `extract-defaults` / `just defaults` | exit 0 with a rule-free document | exit 1 below a floor of 8 rules |
| `ctxloom weave` | exit 0 when parts resolved to zero, the task was empty with members, stdin failed, or there was no output | non-zero in all four |

---

## Upgrading

1. **Run your CI once and read the exit codes.** Anything newly failing was already
   failing; you just could not see it. The most likely hits are an empty or truncated
   `bundle.yaml`, a `0`/`1` integer config override, and `plan list` scoping.
2. **`taskloom plan list` is the one intentional scope change** rather than a
   correctness fix. If you relied on it listing every project, add `--global`.
3. **Parsing `plan list` text output?** It gained two columns. Use `--format json`.
4. **Matching on `agent_run`'s disposition?** Success is unchanged; only the
   previously-mislabelled failure case differs.

## What is deliberately NOT changed

- Legitimately empty results still succeed.
- Version-only bundle skeletons still publish.
- Labels-only profiles still save.
- `taskloom manage uninstall` with nothing to remove still exits 0.
- Mixed-content chat messages record exactly as before.
- A run that names **no** profile still runs context-free.
- A kiro/antigravity run with **no** context configured still removes the managed
  steering file / AGENTS.md section — that is how teardown works.
- An ltk rule file with **no** `@submodules` rule is unaffected by the `.gitmodules`
  changes; a repo that genuinely has no submodules is not an error.
- An opencode session that genuinely recorded nothing still exports and exits 0.
