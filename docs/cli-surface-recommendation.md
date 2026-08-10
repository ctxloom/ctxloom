# CLI surface recommendation: one verb spine, no aliases

**Status:** design recommendation, 2026-08-01. No code changes accompany this
document. Verified against the tree at `6ee1bcfb` (`internal/cli/*.go`,
`website/src/content/docs/reference/cli/` — 130 generated pages,
`tests/acceptance/completeness_test.go`).

---

## 1. The recommendation, in three sentences

Keep **noun-verb** — it is already the direction the CLI-primary reorg
(`2710ff0`, "Decision 6") committed to, and it is the right one for a CLI with
fifteen nouns. Adopt **one canonical verb spine — `list`, `show`, `create`,
`edit`, `delete` — applied uniformly to every resource noun**, with a short,
closed list of domain verbs that earn their keep; delete every deprecated
alias and every duplicate spelling outright, per this project's
no-backward-compat policy. The single biggest structural change is
**collapsing `manage`'s three subgroups (`hooks`, `statusline`, `gitignore`)
into `manage {install, uninstall, status}` with `--only`/`--skip` part
selectors**, which turns the junk drawer into three verbs and deletes six
leaves plus three group nodes.

**Before:** 108 visible leaves + 20 deprecated runnable spellings = **128
runnable command lines**.
**After:** **101 visible leaves, 0 deprecated.** Net −27 runnable spellings,
−7 visible leaves, while *adding* four genuinely missing capabilities
(`llm show`, `skill delete`, `mcp server edit`, `agent edit`).

---

## 2. Why noun-verb, argued once

The alternative — verb-noun (`ctxloom create profile`) — optimizes for a CLI
whose verbs outnumber its nouns. ctxloom is the opposite: ~15 nouns, ~5
recurring verbs, plus per-noun domain operations (`sign`, `distill`, `hold`,
`materialize`) that have no home in a verb-first tree. Noun-first gives every
noun a `--help` page that is a complete inventory of what you can do to that
thing — which is how people actually explore a CLI (`ctxloom bundle` → see the
verbs). It is also what the best large surfaces do (`az`, `gh`, `kubectl`'s
resource model, `docker <object> <verb>` post-1.13), and what taskloom's
`plan list`/`plan show` already do. This was settled in the prior reorg;
re-litigating it now would churn 100+ leaves for zero DX gain. Noun-verb
stands.

The real decision this document makes is the **verb layer**, where today's
surface is genuinely incoherent — three spellings of destroy, three of create,
two of edit (one noun carrying both).

---

## 3. The canonical verb spine

Every resource noun supports the applicable subset of exactly these five
verbs, spelled exactly this way:

| Verb | Meaning | Replaces |
|---|---|---|
| `list` | enumerate the noun's instances | (already consistent — 11 nouns) |
| `show <ref>` | read one instance in detail | `view` (bundle), `get` (config), `browse` (remote) |
| `create <ref>` | make a new instance | `add` (remote, mcp server, trust signer), `set` (agent) |
| `edit <ref>` | change an instance — bare opens `$EDITOR`, structured flags apply non-interactively | `modify` (profile) |
| `delete <ref>` | destroy an instance | `remove` (agent, remote, trust signer, mcp server), `forget` (session), `blacklist` |

Three rules make the spine load-bearing:

1. **One spelling per concept, everywhere.** A user who knows the spine never
   guesses. `add` and `remove` die even where they read slightly more
   naturally (`trust signer add`): the moment two create verbs coexist, every
   noun forces a memory lookup. `az` — the largest noun-verb CLI in
   production — uses uniform `create`/`delete`/`list`/`show` across
   everything including pure registrations, and it is the most guessable
   large CLI there is. That precedent beats `git remote add` muscle memory.

2. **`edit` is the one mutation verb, with two modes.** Bare `edit <ref>`
   opens the configured editor (today's `profile edit`, `config edit`,
   `fragment edit`). With structured flags (`--add-bundle`, `--description`,
   `--llm`…) it applies the change non-interactively and never opens an
   editor (today's `profile modify`). Precedent: `git commit` (editor unless
   `-m`). This merges `profile modify` into `profile edit` — the two commands
   are genuinely different behaviours today (verified: `profile.go:328` is a
   flag-driven structured update through `operations.UpdateProfile`;
   `profile.go:414` opens the YAML in `$EDITOR`), but they are one *concept*
   badly split across two names, and the flag-vs-bare distinction is
   self-disambiguating.

3. **Domain verbs are allowed only when CRUD would misstate the operation,
   and each spelling is global.** The closed list:

   | Domain verb | Nouns | Why CRUD misstates it |
   |---|---|---|
   | `default [name]` | agent, llm, remote | show-or-set the default; a config pointer, not an instance |
   | `distill <ref>` | bundle, command, fragment, session | derived-content generation; already spelled consistently across 4 nouns |
   | `export` / `import` | bundle, profile, skill | archive out / archive in; already a consistent pair across 3 nouns |
   | `sign` | bundle | a cryptographic act on existing bytes |
   | `hold` / `unhold` | bundle | pin semantics against `deps upgrade` |
   | `push` / `move` | bundle | publish to a remote / signature-preserving relocation — distinct from each other and from export |
   | `sync` | skill | manifest recomputation; skills are directory trees, not blobs |
   | `materialize` | profile | write the resolved closure to disk |
   | `rename`, `watch`, `backfill` | session | keyed-by-name store; streaming; vendor-transcript recovery (not an `import` — it scans, it doesn't unpack an export) |
   | `search` | (top level), session | query by content; `session query` renames to `session search` so the concept has one spelling |
   | `accept` / `reject` | trust | trust *decisions*, not CRUD on records |
   | `pull` / `update` / `upgrade` / `discover` | remote | the apt triad (verified deliberate: `remote_upgrade.go` documents update=refresh-index, upgrade=advance-pins, pull=install-what's-pinned) plus find-new-remotes |
   | `serve`, `register` / `unregister` | mcp; acp | run a protocol server; toggle auto-registration |
   | `client` | acp | drive one outbound headless turn |
   | `build`, `check`, `scaffold`, `tooling` | container | image lifecycle |
   | `install` / `uninstall` / `status` | manage | harness lifecycle (family-shared with taskloom/ltk) |
   | `prompt` | init | print the setup interview prompt |

   Anything not in this table is a violation. New commands must pick from the
   spine first, this table second, and extend the table only with a written
   justification.

---

## 4. The proposed tree, in full

Top-level verbs (8 leaves) — each earns top-level status by being a
whole-workflow action, not an operation on one noun:

```
ctxloom init                 # bootstrap a project (interactive)
ctxloom init prompt          # print the setup-interview prompt
ctxloom run                  # run an agent session
ctxloom weave                # fan a task across agents, synthesize
ctxloom review               # accept/reject pending items
ctxloom search               # search content across local + remote
ctxloom doctor               # diagnose the installation
ctxloom version
```

(`index` does not exist and is not proposed. Hidden machine plumbing —
`hook {inject-context, session-bind, stamp-plan, hud}`, `llm {turn, serve,
host}`, `util config-write`, `plan watch`, `completion`, `container
provenance` — is untouched by this proposal and not counted.)

Nouns (93 leaves):

```
profile      list · show · create · edit · delete · export · import · materialize        (8)
bundle       list · show · create · edit · delete · distill · export · import
             · push · move · sign · hold · unhold                                        (13)
fragment     list · show · create · edit · delete · distill                              (6)
command      list · show · create · edit · delete · distill                              (6)
skill        list · show · create · delete · sync · export · import                      (7)
session      list · show · rename · delete · search · watch · distill · backfill        (8)
trust        accept · reject
signer list · show · create · delete                                               (6)
agent        list · show · create · edit · delete · default                              (6)
remote       list · show · create · delete · default · pull · update · upgrade
             · discover                                                                  (9)
mcp          serve · register · unregister
mcp server   list · show · create · edit · delete                                        (8)
llm          list · show · default                                                       (3)
config       show [section] · edit · init                                                (3)
manage       install · uninstall · status        (--only/--skip part selectors)          (3)
container    build · check · scaffold · tooling                                          (4)
acp          serve · client · list                                                       (3)
```

**Total: 101 visible leaves** (8 + 93), against 108 today.

### The addressing grammar carries the load the tree doesn't

The existing ref grammar — `<bundle>#<kind>/<name>` — is the universal
instance address, and this proposal leans on it harder:

- Item nouns own item content. `fragment show core#fragments/tdd` is the one
  way to read a fragment; `bundle view` (which duplicated exactly that,
  verified in `bundle_view.go`) is deleted, and `bundle show` gains `--raw`
  for the full-YAML case `view` also served.
- `mcp server *` spans **both** MCP-server stores, disambiguated by ref
  shape: a bare name addresses the config-level store
  (`config.yaml mcp.servers`), a `<bundle>#mcp/<name>` ref addresses a
  bundle item. `mcp server list` grows a source column. This absorbs
  `bundle mcp edit` (deleted along with its group node) and closes the
  read gap for bundle-scoped servers (`mcp server show core#mcp/tree-sitter`).

### Where the reduction comes from

| Change | Runnable spellings |
|---|---|
| Delete all 20 deprecated command aliases (§6) | −20 |
| Delete bare runnable `trust <ref>` (duplicate of `bundle trust`) | −1 |
| Merge `profile modify` → `profile edit` (flags) | −1 |
| Merge `bundle view` → `fragment/command show` + `bundle show --raw` | −1 |
| Merge `config get` → `config show [section]` | −1 |
| Collapse `manage hooks {install,uninstall,status}`, `manage statusline {install,uninstall}`, `manage gitignore install` → flags on `manage {install,uninstall,status}` | −6 |
| Add `llm show`, `skill delete`, `agent edit` (from the `set` split) | +3 |
| Moves, count-neutral: `bundle mcp edit`→`mcp server edit`; `remote browse`→`remote show`; `acp entries`→`acp list`; `acp server`→`acp serve`; `session query`→`search`; `session forget`→`delete`; all `add`/`remove`/`set`→`create`/`edit`/`delete` | 0 |
| **Net** | **−27** (128 → 101) |

---

## 5. Every place today's surface violates the spine

Verified against `internal/cli` at `6ee1bcfb`. Each row is a rename in this
proposal (deprecated aliases are in §6 instead).

| Today | Canonical | Note |
|---|---|---|
| `agent set` | `agent create` / `agent edit` | today's `set` is an upsert; split into create (fails if exists) + edit (fails if missing). See §9 for the cost. |
| `agent remove` | `agent delete` | |
| `remote add` | `remote create` | |
| `remote remove` | `remote delete` | |
| `remote browse` | `remote show <name>` | also fills remote's missing `show`; shows URL, default status, and the remote's bundles |
| `mcp server add` | `mcp server create` | |
| `mcp server remove` | `mcp server delete` | |
| `trust signer add` | `signer trust` | |
| `trust signer remove` | `signer untrust` | |
| `trust <ref>` (bare runnable) | `bundle trust <ref>` | bare `trust` becomes a pure group node |
| `session forget` | `session delete` | help text keeps the "transcript and essence stay on disk" sentence |
| `session query` | `session search` | one spelling for content search, matching top-level `search` |
| `profile modify` | `profile edit` + flags | verified distinct behaviours today; merged per spine rule 2 |
| `bundle view` | `fragment/command show`, `mcp server show`, `bundle show --raw` | pure duplicate of item-noun reads |
| `bundle mcp edit` | `mcp server edit <bundle>#mcp/<name>` | deletes the `bundle mcp` group node |
| `config get <section>` | `config show [section]` | |
| `acp entries` | `acp list` | |
| `acp server` | `acp serve` | verb-consistent with `mcp serve` |
| `manage hooks install` | `manage install --only hooks` | |
| `manage hooks uninstall` | `manage uninstall --only hooks` | |
| `manage hooks status` | `manage status` (per-part rows) | |
| `manage statusline install/uninstall` | `manage install/uninstall --only statusline` | |
| `manage gitignore install` | `manage install --only gitignore` | |

### Asymmetric gaps filled (new capability, +4)

- `llm show <name>` — completes the read pair; today `llm` has only
  `default` + `list` (confirmed).
- `skill delete` — confirmed missing today; without it the only path is
  hand-deleting the directory and leaving a stale `files:` manifest in
  `bundle.yaml`, which is precisely the tamper-check drift `skill sync`
  exists to prevent. `delete` must remove the tree *and* the manifest entry.
- `mcp server edit` — bundle-scoped servers had `bundle mcp edit`;
  config-level servers had **no** edit at all (add/remove only). One verb now
  covers both stores.
- `agent edit` — falls out of the `set` split.

Deliberately **not** added: `skill edit` (skills are trees; the honest
workflow is edit-files-then-`sync`, and a fake `edit` that opens SKILL.md
would imply blob semantics the noun doesn't have — see §9), `fragment/command
export/import` (no demonstrated need; bundles are the transfer unit), and a
`session export` to pair with `backfill` (backfill is recovery, not the
inverse of an export).

---

## 6. Deleted outright, and what it costs

**All 20 deprecated command aliases** (confirmed: 20 command-level
`Deprecated:` marks + 1 flag, `run --run-prompt`, in `internal/cli`):

`manage mcp install`, `manage mcp uninstall`, `manage mcp servers`,
`manage config show`, `manage config get`, `manage config edit`,
`manage config init`, `mcp list`, `mcp add`, `mcp remove`, `mcp show`,
`command push`, `sign`, `signer add`, `signer list`, `signer show`,
`signer remove`, `blacklist`, `acp agents`, `agent setup`, top-level
`tooling` — plus the `--run-prompt` flag.

**Capability cost: zero.** Every one is a verified byte-for-byte duplicate of
a canonical leaf (same `RunE`). The cost is entirely in muscle memory and in
the acceptance corpus, which today drives several behaviours *only* through
the deprecated spelling (§8).

**Merges with a real (small) cost:**

- `manage` subgroup collapse: per-part operations survive as `--only`/
  `--skip`, but the dedicated `manage hooks status` per-backend report folds
  into `manage status` — that output gets denser. Acceptable; `status` is
  where a human looks anyway.
- `agent set` upsert: the split to `create`/`edit` means blind
  create-or-update (used by the init setup interview's LLM-driven flow) now
  needs two attempts or a `--replace` flag on `create`. Cost is one sentence
  in the regenerated `init prompt` text.
- Bare `trust <ref>`: two extra words (`bundle trust`) for the most common
  trust action. Deliberate: an imperative bare noun that silently means
  "accept" is exactly the kind of surface a user can't guess *safely*.

---

## 7. Family scope: taskloom and ltk

**In scope at the shared-subtree level, out of scope for their task verbs.**
Concretely:

- The shared subtrees must be identical across all three binaries:
  `manage {install, uninstall, status}` (taskloom and ltk already match the
  collapsed shape — neither ever grew subgroups), `version`, `completion`,
  `mcp`/`mcp serve`.
- Where a taskloom noun appears, it already follows the spine
  (`plan list`, `plan show`). Good; keep enforcing it.
- taskloom's flat verb-first task surface (`add`, `edit`, `list`, `show`,
  `status`, `tag`) is **correct and stays**. Noun-verb layering pays when
  nouns are plural; taskloom has one implicit noun, and forcing
  `taskloom task add` would be symmetry theater. Its `add` (vs the family's
  `create`) is the one inconsistency I accept: a single-noun CLI's primary
  verb is part of its identity, and taskloom's append-only log has no
  `delete` for `add` to pair against anyway.
- ltk (`check`, `evaluate`, `loadout`) is too small to have a layering
  problem.

---

## 8. Sequencing: what this breaks, journey by journey

The completeness gate (`tests/acceptance/completeness_test.go`) holds an
**exact-set** allowlist that fails in both directions, so every rename or
deletion below forces a same-commit corpus + allowlist edit. That is the
mechanism that makes this reorg safe to do in one commit; it is also why it
must be *one* commit, not a drizzle.

### Leaves renamed, moved, or deleted (the full delta)

- **Deleted (21 runnable spellings):** the 20 deprecated aliases (§6) + bare
  runnable `trust <ref>` + merged leaves `profile modify`, `bundle view`,
  `config get`, `manage hooks *` (3), `manage statusline *` (2),
  `manage gitignore install` — 30 spellings gone in total.
- **Renamed/moved (13):** `agent set`(split), `agent remove`, `remote add`,
  `remote remove`, `remote browse`, `mcp server add`, `mcp server remove`,
  `trust signer add`, `trust signer remove`, `session forget`,
  `session query`, `bundle mcp edit`, `acp entries`, `acp server`.
- **Added (4):** `llm show`, `skill delete`, `mcp server edit`, `agent edit`.

### Cost to J001600 (in flight, being written now)

J001600's spine survives intact: `bundle sign`, `bundle move`, `bundle trust`,
`bundle untrust`, `signer list`, `signer show` are all unchanged.
Three hits:

1. `trust signer add` → `signer trust` and `trust signer remove` →
   `signer untrust`: mechanical re-spell of the affected steps.
2. The planned scenario driving deprecated `sign` and asserting its
   deprecation notice on stderr **dies entirely** — under this proposal there
   is no deprecation notice to assert, because there is no deprecated twin.
   Same for any `signer list` twin scenario.
3. Allowlist rows for `sign` and `signer list` are deleted, not covered.

**Recommendation: do not stall J001600.** Let it land against current spellings —
its value is the ssh-agent fixture and the payload assertions, which are
spelling-independent. The reorg commit then re-spells the corpus mechanically
(the gate enforces completeness of that re-spell) and deletes the two
deprecated-twin scenarios.

### Cost to J000100–J001200 (planned, per `docs/journey-coverage-gaps.md`)

Write these against the **new** surface from the start — none exists yet, so
renames cost nothing if the reorg's spelling table is handed to their author:

- **J000100:** `mcp server add/remove` → `create`/`delete`; `list`, `show`,
  `register`, `unregister` unchanged. Gains a scenario slot for the new
  `mcp server edit`.
- **J001400:** `manage config init --engine X` is **deleted**, so
  `engineMatrixLeaves` in `completeness_test.go` must repoint its matrix rows
  to `config init --engine X`. The gaps doc's deliberate both-spellings
  scenario ("use the deprecated spelling for the matrix, add one canonical")
  collapses to a single spelling — a simplification, not a loss.
  `manage install --engine X` is unchanged.
- **J001900:** untouched — the four hidden `hook` leaves are outside this
  proposal.
- **J001300:** `acp entries` → `acp list`, `acp server` → `acp serve`;
  `acp client` unchanged. The gaps doc's note that `agent.feature:52` covers
  via deprecated `acp agents` becomes moot (alias deleted).
- **J001200:** `session query` → `session search`; `session watch` unchanged.
- **Folds:** the `config get`/`config show` fold into `config.feature`
  becomes `config show` + `config show <section>`; `container tooling` and
  `init prompt` folds are unchanged.

### The hidden cost: today's coverage rides the aliases

The gaps doc's own table (§1 there) shows ~15 canonical leaves are covered
*only* through deprecated spellings — `trust_cli.feature` drives `blacklist`
and bare `trust`, J001500 drives `signer add/remove/show`, `agent.feature` drives
`tooling` and `acp agents`, `config.feature`/`manage.feature` drive
`manage config *`. **Deleting the aliases turns all of those scenarios RED at
reorg time.** They must be re-spelled to canonical in the same commit — which
is exactly the "re-spell, but with real payload assertions" work the gaps doc
prescribes anyway. The reorg is the forcing function for it.

Also churned mechanically in the reorg commit: the 130 generated reference
pages (regenerate), `strictRunLeaves`/`engineMatrixLeaves`/`knownUncoveredCLI`
in the completeness gate, and any hand-written website prose naming old
spellings (needs a sweep; the website-truthfulness audit machinery is the
right tool).

### Order of work

1. J001600 lands on current spellings (in flight, don't touch).
2. One reorg commit: rename/delete/add in `internal/cli`, re-spell the whole
   corpus, update the gate's three lists, regenerate docs.
3. J000100–J001200 authored against the new surface using the §5 table.

---

## 9. Where I was genuinely torn

1. **`session forget` → `delete`.** `forget` is *semantically better* — it
   says exactly "drop the index entry, keep the files" — and it is the one
   rename where uniformity trades away precision. I chose uniformity because
   a fourth destroy verb re-opens the guessing problem for every noun, and
   the help line carries the nuance. To settle it: decide whether `session
   delete` should ever grow a `--purge` (remove transcript/essence too); if
   yes, `delete` is clearly right — `forget --purge` is an oxymoron.
2. **`agent set` upsert split.** Upsert is genuinely convenient for the
   LLM-driven setup interview. If, when implementing, the two-step
   create-or-edit dance measurably trips the interview flow, add
   `agent create --replace` rather than resurrecting `set`.
3. **`remote discover`.** Arguably `remote search`. I kept `discover`
   because it finds remotes you *don't have* while `search` queries content
   you *do* — overloading one verb with both would be false symmetry. Would
   flip on evidence that users try `remote search` first.
4. **Fragment/command as first-class nouns at all.** Collapsing them into
   bundle-ref operations (`bundle edit core#fragments/tdd` only) would cut
   ~12 leaves. I kept the nouns: cross-bundle `list` is real capability,
   `skill` and `profile` can't collapse (they carry `sync`, `materialize`,
   store duality), and treating item kinds asymmetrically would be worse than
   the extra leaves. Usage data on `fragment list`/`command list` would
   settle it.
5. **`acp entries` → `list`.** `entries` was itself a deliberate recent
   rename (from `agents`). Renaming again is churn, but a bespoke noun-leaf
   meaning "list" is a spine violation in the most visible way. I renamed.
6. **`manage`'s name.** It reads verby and vague; `harness` would be
   truer. Kept `manage` for family consistency (taskloom and ltk both ship
   `manage install/uninstall`) — renaming across three binaries buys
   aesthetics only.

---

## 10. Rules for whoever adds the next command

1. Pick a noun; only whole-workflow actions go top-level.
2. Use the spine (`list`/`show`/`create`/`edit`/`delete`) if the operation is
   CRUD-shaped. No new spellings of spine concepts, ever.
3. A domain verb needs a written one-line justification for why every spine
   verb misstates the operation, and must reuse an existing domain spelling
   if the concept already has one (`distill`, `export`, `default`, …).
4. Address instances with the ref grammar (`bundle#kind/name`), never with a
   new positional-argument scheme.
5. No aliases, no deprecation shims. A rename is a rename; re-running init is
   the upgrade path.
