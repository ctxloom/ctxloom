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
| A minimal (`--skip-setup`) claude oneshot — the distill/compaction path — that produced no output | exit 0, nil error, **zero bytes** written | non-zero, "claude produced no output (exit N)"; a non-zero CLI exit keeps its own code |
| A `session_end` hook configured while running codex | written nowhere, said nothing | still written nowhere (codex has no such event) — now **warns** naming engine and kind; **exit code unchanged** |
| `ctxloom run` on a run that ended **cancelled**, **timed out**, **over budget**, or with **no status at all** | exit 0 — success | exit 1, naming the terminal status |

**Why:** a run that silently delivers no context looks identical to a working one. The
"no fragments configured" case is still a legitimate success.

### Bundles, profiles and signing

| Surface | Before | Now |
|---|---|---|
| Loading an empty / truncated / comment-only / `---`-only / `null` `bundle.yaml` | accepted as a valid empty bundle | `bundle is empty` error |
| `ctxloom bundle distill` / `copy` / `import` over such a file | exit 0, zero items | non-zero |
| `ctxloom bundle push` of a contentless bundle | published (**and signed**) zero bytes | non-zero |
| `ctxloom sign --all` / `bundle sign --all` finding nothing | exit 0, "no local bundles to sign" | non-zero, naming the directories searched |
| `ctxloom sign --all` in a project holding directory-form (`<name>/bundle.yaml`) bundles | exit 0, those bundles silently **not signed** | all of them signed, nested ones included |
| `ctxloom skill import` of a malformed archive over an existing skill | exit 1 — with the existing skill tree already **deleted** | exit 1, existing tree untouched |
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
| A `.sig` record in the approvals store whose bytes will not parse as a signature | read as "no such record" — and on the **reject** path that silently **un-rejects** the item | the fail-closed gate fires: every item is denied, naming the unparseable record, and a strictness finding is recorded; **exit code unchanged** outside strict mode |
| An `allowed_signers` file that exists but cannot be opened (e.g. EACCES) | silently erased from the trust root, indistinguishable from "the file is not there" | warns, naming the file and that its keys are not trusted this session; the failure rides on the trust root as a failed source; **exit code unchanged** |
| A `distrusted_signers` file that exists but cannot be opened | silently ignored — every signer suppressed there was **trusted again**, with no notice | warns, naming the file and that its suppressions are not in effect this session; **exit code unchanged** |
| Re-fetching a **rejected** bundle under a respelled remote URL (`…/repo.git/`, `HTTPS://…`, `www.github.com/…`, `http://…`, `…?ref=x`, `…#frag`, `user@…`) | the rejection was **escaped** — a store miss reads as "not rejected", and a bundle with a verified publisher signature went straight to **allow** | all of these collapse to one trust key, so the rejection holds |

**Why:** every one of these reported trust that was not there, or granted trust that a
real `ssh-keygen` refuses. "Nothing was recorded" and "I could not read what was
recorded" are different answers, and only the first one is safe to act on.

### Dependencies and the lockfile

`.ctxloom/lock.yaml` is the only on-disk record of your dependency pins, your holds
(`ctxloom deps hold`) and the publisher retractions learned at the last sync. Losing it
does not just un-pin — it silently **un-retracts** content a publisher withdrew.

| Surface | Before | Now |
|---|---|---|
| `ctxloom deps upgrade` when `config.yaml` fails to load | ran on an empty fallback config, resolved an empty closure, **erased every entry in `lock.yaml`**, printed "Everything is up to date.", exit 0 | non-zero, naming the config error; the lockfile is untouched |
| Any write that would replace a populated `lock.yaml` with an empty one | written | refused, non-zero, naming how many entries it protected |
| Any write over an **unparseable** `lock.yaml` | overwritten — every hold and retraction in it lost | refused, non-zero; the file is left intact to fix or delete |
| A lock rebuild (startup auto-lock) over an unparseable `lock.yaml` | rewrote it with holds and retractions **cleared** | warns and leaves it alone; **exit code unchanged** (a post-sync step never fails the sync) |
| The trust gate reading an unparseable `lock.yaml` | silently treated nothing as retracted, exit 0 — content a publisher **withdrew** was served again | **withholds remote content** and, on `ctxloom run` / `mcp` / `acp` / `profile materialize`, aborts pre-launch naming the file and the recovery. `review`, `list`, `search` warn and keep working |
| The trust gate with **no** `lock.yaml` at all | nothing retracted | unchanged — a project with no pins legitimately has nothing retracted |
| A root profile that fails to load, or an unlistable profiles directory, during a lock/upgrade | silently narrowed the closure | warns naming what dropped out; **exit code unchanged** |

**A genuinely empty project is still success.** An empty write is allowed whenever the
lockfile is absent, blank, or already empty, and `deps check --cleanup` may still prune
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
| Any write over an **unparseable** `config.yaml` (`ctxloom agent add`, `mcp add`, anything through `Manager.Update`) | exit 0 — the file was **replaced** with only the sections the in-memory config could emit, destroying every key it did not carry, after warning "unknown fields may be lost" | refused, non-zero, naming the file; the file is left exactly as written so the broken line can still be fixed |
| The v5→v6 upgrade of a config that has **both** `profiles.defaults` and a hand-authored `agents.default` | the profile list was deleted from disk with no notice — the next run launched with a different profile set | recorded as a lossy migration naming the dropped profiles and where to re-add them; **fatal in strict mode**, warning otherwise |

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
| `ctxloom session rename <old> ../..` (or any name that is not one path component) | exit 0, index key and `MkdirAll`/`Symlink` on a traversed path | non-zero, index unchanged |
| `ctxloom session list` over a session whose vendor transcript was pruned but whose ctxloom-captured `transcript.jsonl` survives | exit 0, session **irreversibly deleted** from the index | kept and listed; any genuine drop now warns, naming the harp |

### Agents and coordination

| Surface | Before | Now |
|---|---|---|
| `agent_run` when the launch has already failed | `"spawned <harp>"` | an explicit failure disposition. **The success wording is byte-identical** — anything matching `"spawned"` is unaffected |
| `agent_send` with a structured payload that cannot be encoded | ok + message id, payload silently stripped | `InvalidArgument` |
| `ctxloom run -p <typo>` or an unloadable bundle ref | exit 0, zero MCP servers / hooks / commands / skills delivered | strictness finding — warns always, aborts in strict mode, `--degraded` downgrades |
| Transcript export / clipboard copy of an all-notice feed | wrote a **0-byte file** reporting `saved`; clipboard copy emitted OSC 52 **clear** while reporting `copied` | both refused |
| A transcript export whose `kind` is unknown | wrote a file whose extension lied about its contents | refused |
| `ctxloom run --one-shot`, `map`, `weave`, a delegated turn or `acp client` where the engine exits 0 with **no output** | exit 0, empty report / empty `Part.Output` / empty assistant turn | non-zero, carrying the engine's stderr (in a fan this is one member's error `Part`, not the whole call) |
| A `map`/`weave` member (or delegated child) whose **named profiles** assemble to nothing | ran context-free, produced plausible output | non-zero — a run naming no profile is still legitimately context-free |
| `ctxloom manage hooks uninstall --backend <typo>` | `Status: "removed"` listing the typo, nothing removed | non-zero, naming the supported backends |

### Agent coordination (additional)

| Surface | Before | Now |
|---|---|---|
| A child relaunch, turn boundary, or wake whose mail-consume fails to journal | read as "no mail": the child was driven with **no prompt** / parked idle holding undelivered mail, and the message became permanently invisible (its reservation was never released) | the reservation is released so the message stays queued, and the child is failed with the journal error rather than driven promptless |

### Build and generator gates (contributors)

| Surface | Before | Now |
|---|---|---|
| `cmd/validate` (build prerequisite) | exit 0 **having validated zero files** in CI on every run | exit 1 when zero documents validate |
| `just gen-schemas` | exit 0 with an empty target list | exit 1 |
| `just gen-mcp-schemas` | exit 0 with an empty binding table | exit 1 |
| `extract-defaults` / `just defaults` | exit 0 with a rule-free document | exit 1 below a floor of 8 rules |
| `ctxloom weave` | exit 0 when parts resolved to zero, the task was empty with members, stdin failed, or there was no output | non-zero in all four |

### Agent delegation and remote retraction

| Surface | Before | Now |
|---|---|---|
| A top-level container **one-shot** run (`ctxloom run --one-shot` on `runtime: container`) with an empty composed prompt | exit 0, run launched, **zero payload delivered** | refused before the runner starts, naming the empty first turn (U023-F17) |
| A delegated child whose `StartRun` would carry no first turn — no composed prompt, no resume session id, no queued mail | attached and sat idle in `executing`, having been told nothing | the run fails terminal with the reason (U023-F17) |
| A child that starts and exits repeatedly **without consuming its mail** | relaunched forever at zero backoff | bounded by the same budget a failing launch gets, with backoff, then a loud give-up to the parent's mailbox (U023-F02) |
| Retry-budget exhaustion for a cause other than `launch_failed` | silent; the child's mail stayed queued and nobody was told | the parent's mailbox and stderr both learn (U023-F02) |
| `ctxloom deps pull` where the remote's `manifest.yaml` exists but does not parse | treated as **not retracted**, pull proceeded | non-zero: the retraction status is UNKNOWN and the pull stops, `--force` included (U150-F04) |
| A profile using `deny_tools` | `ctxloom run` aborted in strict mode; degraded mode printed "it is IGNORED", which was false | accepted, as the loader always honoured it (U049-F01) |
| A container `--one-shot` one-shot that streamed **zero answer bytes** | a warning, an empty file, **exit 0** | non-zero: the engine ran and answered nothing, so there is nothing to print or record (U041-F01) |
| A **host** (go-plugin) `--one-shot` one-shot that produced zero answer bytes | no check at all: an empty file, **exit 0**, nothing said | non-zero, through the same seam as the container arm (U041-F02) |
| `broken-producer \| ctxloom run --one-shot` where stdin cannot be read | the read error was discarded and the run launched with an empty prompt, **exit 0** | non-zero, naming the read failure (U041-F03) |
| `ctxloom run --one-shot` with no prompt from any source (flag, `--command`, args, pipe) | launched a headless engine that asked nothing, **exit 0** | non-zero: "nothing to run" (U041-F03) |

Two silent-loss fixes carry **no** exit-code change, only correct behaviour:

- `agent_report` from a **resumed** child is no longer discarded. The dedupe
  watermark was keyed by harp while the sequence number restarts per run, so under
  one-shot driving essentially every report after turn 1 was dropped before it
  reached the log (U023-F24).
- The `ui:` config section (`prefix_key`, `surround`) is now actually written by
  `Save()`/`Marshal()`. It had been accepted and silently discarded (U049-F03).
- A container `--one-shot` one-shot's answer is no longer read across a data race. The
  render goroutine kept appending to the answer buffer while the main goroutine read
  it at the turn boundary, and both wrote stdout at once — so an answer could be
  truncated, or the trailing newline could land mid-answer, on a run that reported
  success (U041-F04).

---

### MCP tool arguments the model was told about and the handler discarded

Eight `agent_run` / `roster` / `agent_stop` / `agent_send` arguments were
projected into the tool schemas models read, published as real in the generated
[MCP tools reference](../website/src/content/docs/reference/mcp-tools.md), filled
in by models, and thrown away. Passing one produced a success receipt for a
request that was partly not performed.

| Tool | Argument | Before | Now |
|---|---|---|---|
| `agent_run` | `budget`, `constraints`, `notify_on` | accepted, discarded | **removed from the schema** — no budget carve-out, constraint enforcement, or completion-notification machinery exists |
| `agent_run` | result `child_task_id` | always an empty string | **removed** — ctxloom keys delegation on (harp, run_id) |
| `roster` | `task_id`, `include_descendants` | accepted, discarded | **removed** — there is no task-id axis, and the roster is *already* the whole delegation tree, so `include_descendants: true` could only return the identical list |
| `agent_stop` | `grace` | accepted, discarded | **removed** — the stop is a hard kill; there is no unwind window to grant |
| `agent_stop` | `reason` | accepted, discarded | **honoured** — it becomes the run's durable terminal detail and an audit field; a second `agent_stop` on the same run reports it back |
| `agent_stop` | result `exited_within_grace` | hardcoded `true`, including on the immediate-kill path | **removed** — a response field must not assert an unmeasured fact |
| `agent_send` | `artifact_ids` | accepted, discarded | **removed** — use `agent_report` (which stamps a content-addressed manifest) plus `agent_fetch_artifact`, and name the id in `text`/`structured` |
| `agent_recv` | result `messages[].artifacts` | advertised, never populated | **removed** |
| `roster` | result `phase` | documented as a `StatusChanged.Phase` name or `"TERMINAL"` — an enum nothing constructs | **corrected** to the five real values: `queued`, `executing`, `parked`, `idle`, `ended` |

**Why:** silent acceptance teaches the caller nothing and the model has no way to
discover the failure. A rejection at least teaches; an argument that cannot be
honoured should not be offered at all. A generator gate now fails the build if a
projected input field has no reader in its handler.

Two contract comments were also corrected rather than removed: `HarnessSpec.extra_args`
claimed runner-side allowlist validation that did not exist (the field is gone), and
`Hello.resume_from_seq` / `HelloAck.committed_seq` claimed an authoritative resume
cursor and a credit window — the coordinator echoes back whatever the agent claimed,
and the window was never implemented. Both are now labelled as such in the proto.

### Artifacts

- Publishing a **zero-byte** (or whitespace-only) file no longer succeeds. It used to
  upload, journal, and return `{"journaled": true, "artifact_ids": [...]}` — a
  content-addressed receipt for nothing (U016-F18). Auto-discovered `*.plan.md`
  candidates are skipped; a file you named in `publish_paths` fails the report loudly.
- A child run's failure text is now recorded for **any** non-success terminal, not only
  an explicitly `FAILED` one. A run that ended with no status set, timed out, or blew
  its budget used to have its dying words dropped (U016-F06).

### The delegation watchdog (`liveness`)

The watchdog that warns "agent X looks stalled" was reaching confident verdicts
from failures of its own instruments, and reaching them about agents that had
already finished. Nothing about this changes how any command exits; it changes
which warnings you see on the console during a delegated run.

- **A transcript that cannot be READ no longer reports as an engine that emitted
  zero events.** An unreadable file (permissions, a directory in its place), a
  mid-file scan abort, and a transcript path the coordinator could not resolve all
  used to arrive at the ladder as `Exists:false` — indistinguishable from the file
  being genuinely absent, which is a real progress signal — and past the launch
  grace each produced `stalled: "no canonical transcript exists ... the engine has
  emitted zero events"`. They now produce `unknown`, carrying the read failure
  (U056-F01/F05/F09).
- **A transcript longer than 20 000 lines is no longer judged on its first 20 000
  lines.** The scan bound read the head, so a long-running healthy agent looked
  quiet forever and a cleanly-finished one looked dead. A bounded seek to the last
  1 MiB now recovers the last-record measurements (U056-F02).
- **A run the coordinator has already ended is no longer warned about.** Every
  cleanly-completed child used to be reported `stalled` roughly ten minutes after
  it finished, because `Target.Ended` was passed to the monitor and read by nothing
  (U056-F03). A run terminated with its turn still open is still reported as a
  death — that distinction is the point.
- **The `slow` verdict is gone**, along with the CPU machinery behind it. It could
  never be reached: no pid ever reached the monitor (the spawner returns a kill
  closure, not a pid), so the host probe reported "no pid known" on every call and
  the CPU rung's shipped verdict was permanently its "no CPU evidence obtainable"
  default. The same question — busy or hung? — is now answered from the agent's
  **worktree mtime**, which the coordinator does know and now passes in (U056-F04).
  Stall reasons name the three clocks (transcript, worktree, coordinator activity)
  instead of citing CPU evidence that was never gathered.
- **A worktree that cannot be WALKED no longer reports as a worktree nobody
  wrote to.** The walk swallowed every error, so a permission-denied worktree
  came back indistinguishable from an empty one, its clock silently dropped out
  of the quiet measurement, and the agent was condemned for a silence the
  monitor had never observed. A failed walk now produces `unknown` carrying the
  failure, and the stall reason states what was actually seen of the worktree —
  never looked at / nothing countable / old files — instead of always asserting
  "no worktree write" (U056-F08).

---

## The mock engine can now say "nothing was delivered"

`internal/mockengine` is the stand-in vendor CLI the test suites launch in place
of `claude`/`codex`, and its discovery report is how this project proves context
actually reached a child. It affects no shipped command — but a test instrument
that cannot fail is worse than no instrument, and several of its limbs could not.

- **A zero-byte prompt now reads as a zero-byte prompt.** `promptSha256` used to
  hash the empty prompt to `e3b0c442…`, so it was never empty and "nothing was
  delivered" rendered exactly like a delivery. Reports now carry
  `promptPresent`, and the hash is EMPTY when no bytes arrived (U079-F03).
- **The report carries an `env` section.** The engine declarations say which
  variables ctxloom SETS on the child and which it STRIPS; nothing read either
  list, so a run that stopped setting `CTXLOOM_CONTEXT_FILE` — or stopped
  stripping a credential — reported identically to one that did. A variable set
  to the EMPTY string is recorded as a violation, not a delivery (U079-F05).
- **A probe that fell back to the real `$HOME` says so.** With `CODEX_HOME`
  unset the walk reads the developer's own `~/.codex` and reports `present:true`
  for a surface ctxloom never delivered. The fallback is now marked and folded
  into the discovery digest (U079-F04).
- **The declared argv grammar is enforced in full.** `agent.CLIFlag` gains
  `Required`, `Enum` and `ConflictsWith`, all enforced by `ParseArgv`. Lines the
  real binaries reject with exit 2 — `--sandbox nonsense`, codex's bypass flag
  alongside `--sandbox`, a claude oneshot line missing `--print` — used to parse
  cleanly and run green (U079-F01/F02). This tightens the shared reader the
  driver's own anti-drift tests use, so a driver that stops emitting a required
  flag now fails its test instead of the live spawn.
- **A failed observation is no longer reported as an absent surface.**
  `os.Stat` reports "it is not there" and "I could not look" through the same
  error return, and the probe collapsed both into `present:false` — so a surface
  ctxloom DID deliver, on a path the probe could not stat, read as a delivery
  that never happened. Records now carry `unreadable`, and the directory walk
  no longer discards its error: an incomplete listing says so instead of hashing
  cleanly, and a listed-but-unreadable file is marked rather than left with an
  empty hash (U079-F06/F07).
- **The discovery digest has changed value.** It now covers the env section, the
  env-dir fallback marker and the `unreadable` markers above. Any test pinning a
  literal digest must be re-taken — that is the point: the old digest could not
  distinguish a delivered run from an undelivered one on those limbs, and could
  not distinguish either from a run whose observation failed.

---

## Bundles, agents and imports (batch 8)

- **`ctxloom agent set <name>` now updates only the flags you typed.** It was a
  whole-record replace behind an "add or update" help string, so
  `ctxloom agent set dev --runtime container` silently destroyed dev's engine,
  profiles, permission posture, coordinator flag — and its approval-escalation
  ladder, which the request could not even express, so no caller could have
  preserved it. Omitted flags are now left alone. **Explicitly clearing still
  works**: `--engine ""` clears the engine, because "not named" and "set to
  empty" are now different things. If you were relying on `agent set` to reset a
  binding to defaults, name the fields you want cleared, or `agent remove` and
  re-add (U081-F01).
- **`ctxloom bundle distill` no longer reports a zero-byte distillation as a
  success.** A distiller that returned no error and no content had its empty
  result written over a previously-good distillation and reported as
  `distilled` with a fresh model id. Such an item is now reported
  `distill_failed` and the prior distillation is kept (U081-F04).
- **An unreadable signature is an error, not an unsigned bundle.** A stat
  failure on a detached `.sig` (permissions, I/O) used to read as "this bundle
  is unsigned". On `bundle move` that was worse: the carried signature stayed
  nil, the "published but its signature did not" guard was skipped, and the
  signed local source was deleted. Both paths now stop loudly. A genuinely
  unsigned bundle is still normal and still silent (U081-F06).
- **`bundle import` / `profile import` refuse a filename the loader could never
  find.** The destination is the source basename written verbatim, and the
  loaders filter by extension — so `ctxloom bundle import seed.txt` reported
  `imported` for a file nothing would ever open, and with `--force` destroyed a
  real bundle of the same basename to make room for it. Bundles must be
  `*.yaml` (`bundles.Loader` stats only `<name>.yaml` and `<name>/bundle.yaml`,
  so `.yml` is as unloadable as `.txt`); profiles accept `*.yaml` or `*.yml`.
  Rename the file and re-import (U081-F10).
- **`bundle import` reads the destination-exists error.** An unstattable
  destination used to read as "absent" and was overwritten without `--force`,
  which is the one outcome that guard exists to prevent. `profile import`
  already did this; now both do (U081-F11).
- **A remote URL can no longer place a cache path outside the cache.**
  `Reference.LocalPath` derives an on-disk directory from a lockfile's remote
  URL, and none of the derivations stripped traversal — `https://x/../..`
  cleaned to `..`. `ctxloom deps check --cleanup` against such a lockfile
  deleted a file outside `.ctxloom/cache/bundles`. Traversal segments are now
  rewritten to `__` (rewritten, not dropped, so two degenerate remotes cannot
  collide onto one cache directory), and the cleanup path keeps an independent
  containment check before any delete. A remote with such a URL will find its
  cache directory at a new name and re-pull; ordinary remotes are unaffected
  (U081-F08).

## Sessions, signing and skill imports (batch 9)

- **`ctxloom session rename <old> <new>` validates the new name.** A harp name
  is an index key that becomes a filesystem path component, and the only check
  was non-empty — so `ctxloom session rename <old> ../..` reached
  `MkdirAll`/`Symlink` on a traversed path. Names must now be one path
  component: non-empty, not `.` or `..`, no `/`, `\` or `:`, no control
  characters, and no leading or trailing whitespace. The rule is otherwise
  permissive — `Fix Login Bug`, `réunion-café` and `a.b.c` are all still fine —
  so ordinary renames are unaffected. Validation sits in `paths.HarpDir` and
  in the session store, so every harp-derived path inherits it (U087-F04).
- **`ctxloom session list` stops deleting sessions it can still recover.** A
  session whose *vendor* transcript had been pruned but whose ctxloom-captured
  `transcript.jsonl` survived was reaped from the index — irreversibly, on an
  ordinary listing. ctxloom's own capture now counts as recoverable, and every
  drop prints one line naming the harp it dropped instead of vanishing
  silently (U087-F05).
- **`ctxloom sign --all` now signs directory-form bundles.** It enumerated only
  top-level `<name>.yaml` files, so every `<name>/bundle.yaml` bundle — that
  is, exactly the shape that can carry skills — was skipped, and the command
  reported success having signed a subset. Nested bundles are found too. If you
  publish a directory-form bundle, `--all` will now produce a `.sig` for it
  that it previously never wrote (U087-F03).
- **A rejected `ctxloom skill import` no longer destroys the skill it was
  replacing.** The destination is computed from the archive's own top-level
  directory name and was cleared before anything validated the replacement, so
  importing a malformed archive over a good skill left the skill gone from disk
  and still referenced in `bundle.yaml`. The replacement is now validated in a
  staging directory and only swapped in once it is known good; a bad `--sig`
  path is read before extraction rather than after. A successful import behaves
  exactly as before (U087-F25).

## The canonical transcript (batch 10)

These change what ctxloom writes into, and reads back out of, a harp's own
`persist/transcript.jsonl` — the record that replaced the four per-engine
scrapers and is now the only memory source for codex/kiro/claude-code/
antigravity.

- **Two fields that were being dropped are now on disk.** `session` lines
  carry `resumable` (whether the connected adapter advertised it can resume
  that native session — the live half of the one-shot resume gate), and
  `permission` lines carry `tool_call_id` (the engine-native id of the tool
  call the request gated). Both were declared on the in-memory types and
  silently lost by the on-disk converters, so no transcript ctxloom has ever
  written contains them. `docs/transcript.schema.json` already declared
  `tool_call_id`; `resumable` is added to it. Both keys are optional and
  omitted when empty, so existing transcripts and any consumer of them stay
  valid (U144-F01, U144-F02).
- **A transcript that decodes to nothing is now an error, not an empty
  conversation.** A `transcript.jsonl` whose every line failed to parse — or a
  zero-byte one — used to read back as a successful, empty session. That was
  indistinguishable from a real conversation with no entries, and it
  suppressed the fallback to a vendor transcript, which only runs when the
  canonical read fails. So a user whose capture was corrupted saw a
  confidently empty history instead of the transcript that still existed.
  Reading such a file now fails loudly, and `session list` skips that one harp
  with a warning instead of hiding the rest. **A file that decodes fine but
  carries no conversation entries — only `session`/`complete` envelope lines —
  is still a legitimate empty session and still reads back without error**
  (U144-F03).
- **A failed transcript write is now visible.** Transcript capture is
  deliberately invisible to the chat it shadows: every capture seam discards
  write errors so a failing disk can never stall or break a live
  conversation. With nothing behind that, a full disk or an unwritable
  `persist/` dir produced a completely green chat, zero captured bytes and no
  diagnostic at all. The recorder now prints one warning on the first failure
  (not one per event) and a total on close, including the case where no file
  was ever created. **Nothing about the chat changes** — every event is still
  forwarded, no error is propagated to the engine or the user's turn, and a
  successful capture is as silent as it always was (U144-F04).

## `ctxloom run --format json` carries ten more fields (batch 19)

This changes the **NDJSON stream on stdout** — the documented "contract a GUI
frontend consumes", and the channel the ctxloom VSCode frontend actually reads.
It is an **additive** change: every new key is optional and omitted when empty,
so a stream from a backend that reports none of them is byte-identical to what
0.7.0-pre001 emitted before this. Nothing is renamed, retyped or removed.

`chatEventToJSON` is a third hand-written mirror of the same `agent.ChatEvent`
the canonical transcript mirrors, and it had the same defect, worse. It was
found by the U144 remediation, not by the review corpus — **no finding row
names it**.

- **`entry` events now carry the eight fields they were dropping.**
  `sidechain` (this entry belongs to an engine's own in-harness subagent, not
  the main thread — a frontend without it renders a subagent's whole
  conversation as if the user had had it), `toolCallId` (the engine-native
  tool-call id, so a consumer can pair a result with its call instead of
  guessing by tool name), `toolKind` (the ACP category: execute | edit | read
  | search | …), `toolLocations` (the file paths and lines a tool call
  touches — ACP "follow-along"), `toolContent` (a tool result's structured
  content: content blocks, diffs, terminal references, alongside the flattened
  `toolOutput` that is unchanged), `contentBlocks` (structured message content
  alongside the flattened `content`, unchanged), `systemKind`, and `plan` (the
  agent's plan entries with priority and status).
- **`session` events now carry `sessionId` and `resumable`.** `sessionId` is
  the harness-native session id — the resume handle. Without it a frontend
  could not offer "continue this conversation" at all, because the id never
  reached it. `resumable` is the live half of the resume gate, the same field
  batch 10 restored to the transcript.
- **`complete` events are unchanged** — that variant was already complete.

The mirror-parity gate batch 10 built for the transcript now covers this
mirror too, extended rather than rebuilt (its engine moved to
`internal/testsupport/parity` so a second package could declare pairs against
it; a `_test.go` file is not importable). It also gained a third half — **bool
isolation** — closing the blind spot batch 18 declared: the count-based half
passes when a converter writes one source bool into two mirror slots and drops
the other, which was verified by injecting exactly that defect.

---

## The bundle loader stops failing silently (batch 20)

Three loader paths reported "nothing here" when the truth was "I could not find
out". All three now say so.

- **A bundle that will not load no longer exports zero files in silence.**
  `CommandsFromBundleRef`/`SkillsFromBundleRef` feed the code that *writes*
  per-engine command and skill files. A profile naming an unloadable bundle used
  to write nothing and exit 0, which looked exactly like a bundle that ships no
  commands. You now get a `skipping unresolved bundle "<ref>": <why>` warning —
  the same one you already got when a profile referenced an unresolvable bundle
  for its fragments.
- **An unreadable bundles directory is no longer an empty bundle list.**
  `ctxloom` used to fold "this directory cannot be read" into the same path as
  "this directory does not exist", so a permissions problem on your bundles root
  surfaced downstream as *fragment not found* — blaming your ref for a problem
  with your filesystem. It now warns naming the directory and the underlying
  error, and in strict mode records a fatal-class finding. A bundles directory
  that simply **does not exist** is still ordinary and still silent.
- **Skills in companion, remote and seeded bundles are no longer resolved from
  your working directory.** A bundle's `Path` is a real file path only for
  on-disk bundles; companion bundles (which ctxloom reads from a companion
  binary's `loadout` output) have none, and remote/seeded ones carry a synthetic
  `<remote>:…` marker. Taking the directory of those yielded `.`, so such a
  bundle's skills resolved to `./skills/<name>` **relative to wherever you ran
  ctxloom** — arbitrary project-local files loaded, trust-gated, shown to you
  for approval, and materialized under that bundle's identity. Those skills are
  now withheld with a warning naming the skill.

### What this may change for you

If a companion or remote bundle appeared to ship a file-backed skill, that skill
was never really its own — it was whatever sat at that path in your working
directory. It is now withheld, and you will see a warning saying the bundle has
no directory to resolve files against. A skill with a synced (authored) manifest
is unaffected: those are hashed from the manifest and never touched the
filesystem, so companion and remote bundles carry them exactly as before.

## Two invocations are spelled differently

These are the only two renames in this release, and both are hard breaks: the
old spelling is gone rather than aliased. Carrying two names for one thing puts
the ambiguity in help, in scripts, and in every future reader's head, and this
project's upgrade path is re-spelling, not a shim.

| What you typed | What you type now | Why |
| --- | --- | --- |
| `ctxloom mcp` (as the stdio MCP server) | `ctxloom mcp serve` | The bare noun now lists this project's configured MCP servers, like every other noun's bare form. One spelling per concept, symmetric with `acp serve`. |
| `ctxloom run --print` | `ctxloom run --one-shot` | `--print` named the OUTPUT, which every mode produces. What distinguishes the mode is that it takes one turn and exits — the word the code already uses for it. |

**`ctxloom mcp` is the one to act on**, because a stale entry is silent. Your
engines' settings files still name the old invocation, and an engine launching
it starts perfectly: the ctxloom tools simply never appear, because the client
is waiting on a JSON-RPC handshake that a server listing cannot satisfy. Nothing
in the session says so.

Run **`ctxloom doctor`**: `DOCTOR-CHECK-MCP-INVOCATION-g7` reads the ctxloom
entry inside every engine's own registry (`.mcp.json`,
`.agents/mcp_config.json`, `.kiro/settings/mcp.json`, `.codex/config.toml`,
`opencode.json`) and names any file still carrying the old spelling. Re-run
`ctxloom init` to rewrite them.

Typing the bare noun where a machine expects the protocol is caught too: off a
terminal, `ctxloom mcp` refuses and names `mcp serve` rather than printing a
listing into a pipe that cannot frame it.

`ctxloom acp client` also takes `--one-shot`, and **requires** it. That leaf
drives exactly one turn and has no interactive form, so the flag states the mode
rather than leaving a bare invocation to imply a session it never opens.

## Upgrading

1. **Run your CI once and read the exit codes.** Anything newly failing was already
   failing; you just could not see it. The most likely hits are an empty or truncated
   `bundle.yaml`, a `0`/`1` integer config override, and `plan list` scoping.
2. **`taskloom plan list` is the one intentional scope change** rather than a
   correctness fix. If you relied on it listing every project, add `--global`.
3. **Parsing `plan list` text output?** It gained two columns. Use `--format json`.
4. **Matching on `agent_run`'s disposition?** Success is unchanged; only the
   previously-mislabelled failure case differs.
5. **Scripting `ctxloom deps upgrade`?** It can now exit non-zero where it used to
   print "Everything is up to date." Every such case was an upgrade that resolved
   nothing and erased your pins. If it fires, fix the config it names — do not retry.

## What is deliberately NOT changed

- Legitimately empty results still succeed.
- `ctxloom run` with **no** prompt and no `--one-shot` still opens an interactive session. Only
  the one-shot arm, which gets exactly one turn, refuses an empty prompt.
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
  `lock.yaml` and exits 0; `deps check --cleanup` may still prune its last entry.
- A **structured** container run still opens with no lead: it takes its turns via
  follow-up sends, so an empty first prompt there is legitimate. Only the one-shot
  arm, which gets exactly one turn, is refused.
- A remote that publishes **no** `manifest.yaml` is still legitimately "not retracted".
  Only a manifest that exists and will not parse is an error.
- A relaunch budget resets whenever the child actually **consumes** mail, so a
  long-lived child taking one turn per run is never throttled by it.
- `agent_stop` without a `reason` still works and still records the exact wording it
  always did (`stopped by <harp>`); the reason is additive detail, never required.
- An artifact with real content of any size up to the 64 MiB cap publishes exactly as
  before. Only *nothing* is refused.
- `ctxloom run` on a run that genuinely **succeeded** still exits 0, and an
  explicitly `FAILED` run still exits 1 with the same message.
- `ctxloom agent set <newname>` on a name that does not exist yet is unchanged: with
  nothing to preserve, a per-field update and a whole-record write are the same write.
- A `bundle`/`profile` import of a correctly-named, non-empty file is unchanged.
- A bundle that genuinely carries no detached `.sig` still imports, exports and moves
  as unsigned, silently. Only an unreadable one is an error.
- A distillation that produces real content is unchanged, and a `--dry-run` still
  plans without distilling anything.
- Ordinary session renames are unaffected: only names that stop being one path
  component are refused, and the charset stays permissive.
- A session with genuinely no transcript and no essence is still reaped — it is
  unactionable. It just says so now.
- A successful `ctxloom skill import` behaves exactly as before, staging and all.
- A transcript that decodes to real records but carries no conversation entries
  (only `session`/`complete` envelope lines) is still a legitimate empty session,
  and still reads back without error. Only a file from which *nothing* decoded is
  a failure.
- A partially readable transcript still degrades to partial exactly as before:
  the readable lines come back with no error.
- A chat whose transcript capture fails is otherwise **unchanged** — every event
  is still forwarded, nothing is propagated to the engine or the user's turn.
  Only the silence changed.
- A `--format json` stream from a backend that reports none of the ten new fields
  is **byte-identical** to before: every new key is `omitempty`. No existing key
  was renamed, retyped or removed, so a frontend that ignores unknown keys needs
  no change at all.
- A bundles directory that genuinely does not exist is still skipped silently —
  most search directories are speculative, and "absent" is not "unreadable".
- A bundle that genuinely ships no commands or skills still exports nothing, with
  no warning. Only a bundle that *failed to load* is now loud.
- A skill with a synced (authored) manifest in a companion or remote bundle is
  unaffected: its preimage never touched the filesystem, so it needs no bundle
  directory and none is now demanded of it.
- The liveness watchdog still **reports only**. It has never terminated, relaunched
  or reaped anything, and none of the above changes that; a quieter watchdog is a
  watchdog whose warnings are worth reading.
