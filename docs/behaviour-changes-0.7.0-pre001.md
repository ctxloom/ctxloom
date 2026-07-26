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
| A skill with **no `files:` manifest** (never `ctxloom skill sync`-ed) | every such skill hashed to one **constant** trust preimage, so an approval bound no content and any later replacement was delivered unreviewed | the preimage is derived from the skill's real source tree; changing any file re-triggers review |
| The install-time tamper check (`VerifyExtractedManifest`) on such a skill | **skipped entirely** — the same condition that emptied the preimage also disabled the check | runs on every skill, synced or not |
| A skill whose source tree is missing, unparseable, or escapes the bundle directory | resolved anyway under the constant hash | withheld, naming the reason |

**Version-only bundle skeletons remain publishable.** Only a document declaring nothing
at all is refused.

**Manifest-less skills become pending again — once.** Their previously recorded approvals
covered a constant, not your content, so they cannot be carried over: re-approving is what
binds the approval to real bytes for the first time. `ctxloom review` shows the full
per-file listing when you do. A skill that had already been through `ctxloom skill sync`
is **unaffected** — its preimage is byte-for-byte what it always was, and its approval
stands. Run `ctxloom skill sync <bundle>` to move a skill to an authored manifest for good.

### Engine settings files ctxloom round-trips

| Surface | Before | Now |
|---|---|---|
| `hooks apply` / launch when `.claude/settings.json` has an unparseable `permissions` block | exit 0, warning, the user's `allow` / `ask` / `defaultMode` / `additionalDirectories` rules **deleted from the file**, no backup | non-zero; original untouched and backed up to `settings.json.corrupt-<ts>` |
| …an unparseable `permissions.deny` | exit 0, warning, same loss | non-zero, same backup |
| A legacy `mcpServers` block in `.claude/settings.json` | **deleted** on every write, including on uninstall | preserved verbatim |
| …an unparseable `.mcp.json` | exit 0, warning, replaced with a file holding only ctxloom's servers | non-zero; original untouched and backed up |
| …an unparseable `.agents/hooks.json` (antigravity) | exit 0, warning, replaced with a ctxloom-only file | non-zero; original untouched and backed up |
| …an unparseable `hooks` field inside it | exit 0, warning, the user's hooks dropped from the file | non-zero, same backup |

**Why:** these are files the user owns and ctxloom only edits. Each path read
"tolerate a schema change and keep going", and each one implemented that by writing an
empty structure over the thing it had failed to read. A **missing** file is still the
normal first-run shape and still writes cleanly.

### The trust root and the approvals store

| Surface | Before | Now |
|---|---|---|
| Any trust decision when `$HOME` cannot be resolved | the user approvals store silently resolved against the **process working directory**, so personal rejections went unseen and a `*.approve.unsigned` file at a repo root was an unconditional approval | the store reports itself unconfigured; the existing fail-closed gate denies all items and names the cause |
| Approving an item whose payload is empty | exit 0, "approved" with a key fingerprint, item stays pending forever | non-zero: an approval that pinned nothing is refused (ref-**rejections**, which pin no bytes by design, are unaffected) |
| Appending to a corrupt/truncated `approvals/index.yaml` | exit 0, the whole approval history silently overwritten | non-zero, the existing file left untouched; the write is now temp+rename |
| `ctxloom review` when the approvals index cannot be read | item silently labelled **NEW** | warns and labels it **UPDATE** (the conservative reading); **exit code unchanged** |
| `ctxloom bundle distill` when the approvals index cannot be read | item silently omitted from the invalidation report | warns and reports it as invalidated; **exit code unchanged** |
| `ctxloom signer add` with whitespace or a comma inside a principal | exit 0, success line + fingerprint, entry unusable on every later read | non-zero |
| `ctxloom signer add --comment` containing a line break | exit 0, a **second** fully-trusted signer appended that the prompt never displayed | non-zero |
| An `allowed_signers` line whose declared key type does not match the key blob | trusted (real `ssh-keygen` calls it "invalid key") | dropped and reported |
| `ctxloom signer list` / `show` over a store with an unreadable line | the line was silently omitted from the audit listing | listed as an `(unreadable)` row + a warning; **exit code unchanged** |
| `ctxloom signer remove` finding nothing in a store whose lines it could not fully read | exit 0, "no entry for X" | non-zero (a fully-parseable store without that principal still exits 0) |
| `ctxloom signer list` / `remove` where the store exists but cannot be opened | silently treated as absent | reported |

**Why:** every one of these reported trust that was not there, or granted trust that a
real `ssh-keygen` refuses. "Nothing was recorded" and "I could not read what was
recorded" are different answers, and only the first one is safe to act on.

### Dependencies and the lockfile

`.ctxloom/lock.yaml` is the only on-disk record of your dependency pins, your holds
(`ctxloom bundle hold`) and the publisher retractions learned at the last sync. Losing it
does not just un-pin — it silently **un-retracts** content a publisher withdrew.

| Surface | Before | Now |
|---|---|---|
| `ctxloom remote upgrade` when `config.yaml` fails to load | ran on an empty fallback config, resolved an empty closure, **erased every entry in `lock.yaml`**, printed "Everything is up to date.", exit 0 | non-zero, naming the config error; the lockfile is untouched |
| Any write that would replace a populated `lock.yaml` with an empty one | written | refused, non-zero, naming how many entries it protected |
| Any write over an **unparseable** `lock.yaml` | overwritten — every hold and retraction in it lost | refused, non-zero; the file is left intact to fix or delete |
| A lock rebuild (startup auto-lock) over an unparseable `lock.yaml` | rewrote it with holds and retractions **cleared** | warns and leaves it alone; **exit code unchanged** (a post-sync step never fails the sync) |
| The trust gate reading an unparseable `lock.yaml` | silently treated nothing as retracted, exit 0 — content a publisher **withdrew** was served again | **withholds remote content** and, on `ctxloom run` / `mcp` / `acp` / `profile materialize`, aborts pre-launch naming the file and the recovery. `review`, `list`, `search` warn and keep working |
| The trust gate with **no** `lock.yaml` at all | nothing retracted | unchanged — a project with no pins legitimately has nothing retracted |
| A root profile that fails to load, or an unlistable profiles directory, during a lock/upgrade | silently narrowed the closure | warns naming what dropped out; **exit code unchanged** |

**A genuinely empty project is still success.** An empty write is allowed whenever the
lockfile is absent, blank, or already empty, and `remote update --cleanup` may still prune
its last entry — that erasure is declared, not inferred.

**Recovery from an unparseable `lock.yaml`:** delete it and re-run (`ctxloom remote lock`).
The refusal exists so you get to decide, not so you get stuck. Nothing overwrites the
broken file, so your holds and retractions are still in it and can be read by hand first.
The commands that help you diagnose it — `review`, `list`, `search` — keep working; only
the commands that would *launch an agent on it* refuse.

**Why the read side now refuses too.** "I cannot read the retraction record" is not the
same statement as "nothing is retracted", and treating it as such re-exposed content a
publisher had deliberately withdrawn. Retraction exists for *"this turned out to be
harmful"*, so failing open on it inverts the control. The accepted cost is stated plainly:
a corrupt `lock.yaml` turns `ctxloom run` into an abort rather than a degraded run.
`--degraded` (`CTXLOOM_DEGRADED=1`) still launches — it downgrades the fault from fatal to
a warning — but it does **not** relax the withholding: unreadable trust state is never
treated as trustworthy.

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
5. **Scripting `ctxloom remote upgrade`?** It can now exit non-zero where it used to
   print "Everything is up to date." Every such case was an upgrade that resolved
   nothing and erased your pins. If it fires, fix the config it names — do not retry.

## What is deliberately NOT changed

- Legitimately empty results still succeed.
- Version-only bundle skeletons still publish.
- A skill that has been through `ctxloom skill sync` hashes exactly as before, and its
  existing approval stands — the preimage change reaches manifest-less skills only.
- Authoring a skill without syncing it is still supported. `ctxloom skill create` leaves
  no manifest and that remains a legitimate shape; it is now bound to its content rather
  than refused.
- Labels-only profiles still save.
- `taskloom manage uninstall` with nothing to remove still exits 0.
- Mixed-content chat messages record exactly as before.
- A run that names **no** profile still runs context-free.
- A kiro/antigravity run with **no** context configured still removes the managed
  steering file / AGENTS.md section — that is how teardown works.
- An ltk rule file with **no** `@submodules` rule is unaffected by the `.gitmodules`
  changes; a repo that genuinely has no submodules is not an error.
- An opencode session that genuinely recorded nothing still exports and exits 0.
- A project with genuinely nothing pinned still locks and upgrades to an empty
  `lock.yaml` and exits 0; `remote update --cleanup` may still prune its last entry.
