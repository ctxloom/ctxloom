# Config layer scope, and the `.ctxloom` tiers — design

**Status:** proposed, 2026-08-05. Nothing built. Every claim below is labelled
VERIFIED (read in this tree, or run against a binary built from it at
`e1e12ca0`) or INFERRED.

A configuration value is a fact about SOMETHING — this machine, this user, this
project, this invocation. A `.ctxloom` path is likewise a fact about something,
and what a fresh clone gets depends entirely on which. Neither is written down,
so neither is enforced.

---

## The problem

**Every config key is settable at every layer.** `confload` resolves one chain
(home file < project file < env vars < `--config-set`), and after
`config.decodeMergedLayers` marshals the merged map and unmarshals it into
`config.Config`, nothing downstream can tell which layer a value came from.
`config.loadConfigLayer` is the last place layer identity exists, and it uses it
only to name the file in a diagnostic. (VERIFIED: `internal/config/config.go`,
`loadLayeredConfig` → `decodeMergedLayers`.)

Three properties of the layers that no code knows:

- **The project config file is COMMITTED and multi-author.**
  `gitignore.PrivateStatePatterns` deliberately excludes `.ctxloom/config.yaml`
  — "committed by omission — it's content, config, or trust state the project
  depends on" (VERIFIED, `internal/gitignore/gitignore.go`). Anything written
  there arrives, pre-set, in every clone.
- **The env layer is AMBIENT and inherited by children.** `confload`'s own
  package doc says so: "Env vars are inherited by child processes, so
  `CTXLOOM_CONFIG_*` overrides set for a ctxloom invocation are also visible to
  any taskloom/ltk/engine process it spawns." An agent that can run `bash` can
  write this layer.
- **The home layer FILLS GAPS, it does not merely lose.** `confload.Merge` deep
  merges maps, so `home` contributes every key `project` does not mention —
  including keys nested inside an object the project DID mention.

The consequence is not hypothetical. Three measured cases below.

### Already wrong #1 — a consent flag is grantable by environment variable

`dirty_tree_commit_ack` authorizes ctxloom to `git commit` on the user's behalf
so a delegated child can see uncommitted work. `operations.commitDirtyTree`
documents the invariant in full:

> the per-project human acknowledgement (`cfg.GetDirtyTreeCommitAck()`, read
> ONLY from the PROJECT config — never req/agent-supplied data — by design …
> DO NOT add a per-call override for this: a per-call parameter would let a
> delegating AGENT grant itself permission to commit on the user's behalf

VERIFIED, run against the binary built from this tree, in a scratch `HOME` and
a scratch project whose `config.yaml` never mentions the key:

```
$ CTXLOOM_CONFIG_DIRTY_TREE_COMMIT_ACK=true ctxloom config show --format yaml
dirty_tree_commit_ack: true

$ ctxloom config show --format yaml --config-set dirty_tree_commit_ack=true
dirty_tree_commit_ack: true
```

And with the key set only in `~/.ctxloom/config.yaml`, the project inherits it.
So the sentence "read ONLY from the PROJECT config" is false three ways. The
no-per-call-override rule was enforced at the `agent_run` parameter (VERIFIED,
`coord.runchannel` and `coord.children` both restate it) while the ambient
channel that reaches the same field was left open.

### Already wrong #2 — home can escalate a project's agent by name collision

VERIFIED, same method. Home declares `agents.reviewer` with `permissions:
bypass`, `coordinator: true`, `runtime: container`. The project declares
`agents.reviewer: {profiles: [default]}` — a plain agent. The merged result:

```yaml
agents:
    reviewer:
        coordinator: true
        engine: claude-code
        permissions: bypass
        profiles:
            - default
        runtime: container
```

`coordinator: true` is a trust grant — it admits an agent to the coordinator-only
MCP tools (`agent_run`/`roster`/`agent_stop`/`agent_fetch_artifact`, per the
schema's own description). The project author wrote none of that and cannot see
it in their file.

Worse, the escalation needs no file at all. VERIFIED against a project whose
config declares no agents:

```
$ CTXLOOM_CONFIG_AGENTS_EVIL_COORDINATOR=true \
  CTXLOOM_CONFIG_AGENTS_EVIL_PERMISSIONS=bypass ctxloom config show --format yaml
agents:
    evil:
        coordinator: true
        permissions: bypass
```

Two environment variables mint a coordinator-trusted, permission-bypassing agent.

### Already wrong #3 — "LOCAL ONLY" agents are committed and shipped

The schema says of `agents`: "LOCAL ONLY — never shipped in a bundle or remote."
VERIFIED: this repository's own `.ctxloom/config.yaml` is tracked
(`git ls-files .ctxloom`) and carries eight agents, four of them
`permissions: bypass`, one `coordinator: true`, five `runtime: container`. A
clone gets all of it. `paths.AgentsDir` (`.ctxloom/agents/`) is likewise absent
from `PrivateStatePatterns`, so the directory form is committed too.

"Local only" is true of the bundle/remote path and false of the file the key
actually lives in. Nothing enforces the difference because nothing knows the
project file is committed.

---

## What each layer is a fact about

| layer | the value is a fact about | who can write it | who reads it |
|---|---|---|---|
| home `~/.ctxloom/config.yaml` | this USER on this MACHINE | the person at the keyboard | every project they open |
| project `.ctxloom/config.yaml` | this PROJECT, for everyone | anyone with commit rights | every clone, forever |
| env `CTXLOOM_CONFIG_*` | this PROCESS TREE | anyone who can spawn a process, **including an agent** | this process and every child |
| flag `--config-set` | this ONE INVOCATION | whoever typed the argv | this process only |

The two rows that carry the surprise are the middle ones. "Project" reads as
"local to my checkout" and is in fact "distributed to strangers". "Env" reads as
"a temporary override I typed" and is in fact "the one channel that crosses into
the engine process ctxloom launches".

The complement of that table is the scope a KEY belongs to:

| scope | the value is a fact about | home | project | env | flag |
|---|---|:--:|:--:|:--:|:--:|
| `ScopeShared` | the project's content and team policy | – | ✓ | – | ✓ |
| `ScopeMachine` | this box's filesystem, binaries, runtimes | ✓ | – | ✓ | ✓ |
| `ScopePreference` | this person's taste; harmless either way | ✓ | ✓ | ✓ | ✓ |
| `ScopeInvocation` | this one run | – | – | ✓ | ✓ |
| `ScopeNever` | a human's prior authorization | – | – | – | – |

`ScopeShared` keeps `flag` because `--config-set` is scoped to one argv and
cannot be inherited — an operator overriding a team default for one run is the
flag's purpose. It loses `home` deliberately: a home value for a shared key does
not "lose the race", it FILLS A GAP the project left, which is exactly bug #2.

---

## The key × layer table

Grounded in `resources/schema/input/config-schema.json` — the hand-authored
schema `schema.ConfigValidator` validates each layer against. (Note for anyone
following the brief: `resources/schema/gen/` is NOT the config schema. VERIFIED
after running `just gen-schemas` in this tree — it emits 58 schemas, all of them
`*-result-schema.json` for JSON OUTPUT shapes. The config schema is authored, not
reflected.)

| key | scope | why — what the value is a fact about |
|---|---|---|
| `version` | `ScopePreference` | the file's own schema generation; per-file by construction |
| `default_agent`, `agents.*.profiles`, `agents.*.engine`, `agents.*.driving`, `agents.*.escalation` | `ScopeShared` | which context this project's roles compose |
| `agents.*.runtime`, `runtime` | `ScopeMachine` | whether THIS box has a container runtime; `strictness.ClassIsolation` already exists because this fails per-machine |
| `agents.*.permissions`, `agents.*.coordinator`, `llm.configs.*.permissions` | `ScopeShared` | a privilege grant; a team may decide it, but a *user's home* must never fill it in for a project (#2) |
| `dirty_tree_commit_ack` | `ScopeNever` | prior human authorization to mutate a repo — see "Consent leaves the chain" |
| `dirty_tree_handler`, `workspace` | `ScopeShared` | how this project's delegation behaves; same for everyone |
| `agent_turn_cap` | `ScopeMachine` | a resource ceiling; its own doc calls it "a deliberately finite number well below the process-count load this project has measured pain at" — a fact about the box |
| `llm.configs.*` (label, `type`, `model`, `role`, `args`, `thinking`) | `ScopePreference` | which model a person likes; harmless in either file |
| `llm.configs.*.binary_path` | `ScopeMachine` | an absolute path on this filesystem |
| `llm.configs.*.env` | `ScopeMachine` | credential passthrough; a committed value is a leaked secret |
| `llm.defaults.primary`, `llm.defaults.fast` | `ScopePreference` | which label plays which role |
| `profiles.definitions.*` | `ScopeShared` | authored content |
| `mcp.servers.*`, `mcp.plugins.*` | `ScopeShared` | what the team wires in |
| `mcp.servers.*.command`, `.args`, `.env` | `ScopeMachine` | a binary path and its credentials on this box |
| `mcp.auto_register_ctxloom` | `ScopePreference` | – |
| `hooks` | `ScopeShared` | authored content |
| `isolation_images`, `isolation_engines`, `isolation_devcontainer_base`, `isolation_devcontainer_service` | `ScopeMachine` | image tags and engines present on this machine |
| `isolation_base_containerfile` | `ScopeShared` | its own doc: "Relative paths resolve against the project root" — a repo file |
| `sync.auto_sync` | `ScopePreference` | – |
| `config.use_distilled`, `config.compaction_chunks` | `ScopePreference` | – |
| `config.statusline` | `ScopeMachine` | whether ctxloom may own THIS terminal's statusline |
| `config.sign.key` | `ScopeMachine` | a fingerprint or path to this user's key material |
| `config.sign.default` | `ScopePreference` | – |
| `editor.command`, `editor.args` | `ScopeMachine` | a binary on this box |
| `ui.prefix_key`, `ui.surround` | `ScopePreference` | its own doc already says "Flag/env never lives here — only presentation preferences" |

`mcp.servers.*` splitting across two scopes is not a fudge: the SET of servers
is team policy and the COMMAND that launches one is a machine path. That split
is why `Rule.Path` has to reach leaves inside a wildcard level, and why
longest-match wins.

### Consent leaves the chain

`dirty_tree_commit_ack` is the only key in the schema that records prior human
authorization, and it is the only one this design removes from config entirely.
Every OTHER consent record in the tree already lives outside the chain, and the
reasons are already written down:

- `paths.HomeCompanionConsentPath` — "may ctxloom exec this file on THIS
  machine, which is a property of the machine's filesystem and cannot be
  delegated to a repo. A committable form would let a clone arrive carrying
  pre-approved binaries."
- `paths.HomePublishRemotesPath` — "Committing one would ship it to everyone who
  clones the repo and pre-answer the question for them, which is exactly one of
  the three mistakes the confirmation exists to catch."

Both are `admission.Store` files, not config keys. `dirty_tree_commit_ack` is
the outlier, and #1 is what the outlier costs. It cannot simply move to home —
it is a fact about ONE checkout's branch, for the same reason
`paths.RefusedAdvancesPath` documents at length for refusals. It needs a
project-scoped, never-committed, never-inherited home. Which `.ctxloom` does not
have. That is question 2's answer.

---

## What happens at a disallowed layer

Options: ignore silently (today), warn and ignore, warn and honour, refuse.

**Recommendation: record a `WarnKindLayerScope` warning and DROP the value.**

This is one behaviour, not two, and it produces refusal or warning depending on
posture without a new switch — because `config.WarningKind.StrictnessClass`
already maps every kind to a fatal class, and `strictness.SetDegraded` already
exists. Default startup is strict, so a scope violation aborts, names the key,
names the layer, and names the file to move it to. `--degraded` downgrades it to
a warning and launches. The existing `WarnKindUnknownKey` is the exact
precedent: it also drops the value, and its doc gives the reason this design
adopts verbatim — "Silently dropping it is the worst outcome: the setting looks
applied and is not."

Why **drop** rather than **honour**: honouring is the harm in every one of the
three measured cases. #1 and #2 are privilege grants and #3 ships machine
configuration to strangers; a warning that leaves the value in place fixes
nothing and trains people to skip the line.

**The trade-off, stated plainly.** Somebody's home config sets a
`ScopeShared` key today and it works. After this change their setup stops at
startup with a finding. That is a deliberate cost, and it is bounded by the fact
that it cannot be silent: the failure is loud, names the key and the fix, and
`--degraded` gets them running in the same second. The alternative — grandfather
existing keys — is a silent-no-op with extra steps.

---

## The `.ctxloom` classification

VERIFIED by reading `internal/paths/paths.go`, `internal/gitignore/gitignore.go`,
the root `.gitignore`, and by listing a live `.ctxloom` and `~/.ctxloom`.

`content/` is committed and `cache/` is derived, as stated. The model is TRUE
and INCOMPLETE: it has no name for the third category, so third-category files
were placed by ad-hoc decision — some at the `.ctxloom` root, one inside
`cache/`, one loose and gitignored.

| path | tier | rebuilt by | note |
|---|---|---|---|
| `config.yaml` | committed | – | multi-author; carries machine keys today (#3) |
| `remotes.yaml` | committed | – | intended; `HomePublishRemotesPath` is the mitigation |
| `lock.yaml` | committed | `remote lock` | pins bundles only — VERIFIED, three keys: `version`, `locked_at`, `bundles` |
| `content/**` | committed | – | authored; `paths.LocalBundlesPath` |
| `profiles/**` | committed | – | authored |
| `agents/**` | committed | – | **contradicts** the schema's "LOCAL ONLY" |
| `allowed_signers`, `distrusted_signers` | committed | – | intended, spec §7.3 path A |
| `approvals/**` | committed | – | intended; written only by `review --project` |
| `cache/bundles`, `cache/repos` | derived | `ctxloom remote pull` | the only genuinely re-fetchable tree |
| `cache/context/*.md` | derived | `manage hooks install`, `run` | `agent.SCMContextSubdir` |
| `cache/refused_advances.yaml` | derived | `remote upgrade` | correct by its own doc |
| `cache/trust/objects` | **derived-in-name-only** | nothing | see below |
| `project-id` | **local-only** | nothing | see below |
| `sessions/*.md` | **local-only** | nothing | distilled session records |
| `*.lock` | local-only | any writer | advisory filelock sidecars |
| `pieces/`, `ephemeral/` | **neither** | – | VERIFIED: no writer exists |

### The local-only category, and what a clone silently loses

**`.ctxloom/project-id`.** Gitignored (`PrivateStatePatterns`), and it is the
KEY to the task log at `~/.ctxloom/tasks/<project-id>.jsonl`. ADR 0025 decision
4 is explicit that the marker is gitignored on purpose. The consequence it does
not state: a fresh clone has no marker and no registry path match, so it MINTS A
NEW PROJECT — new id, new empty log. Every task the team logged is still on disk
under the old id and is unreachable from the clone. ADR 0025 warns on the
COPY→fork path (decision 5); the no-marker-at-all path is new-project minting,
which has nothing to warn about because from its point of view nothing was lost.
INFERRED that this is silent — I read the resolution rules, I did not run a
clone.

**`.ctxloom/cache/trust/objects`.** Filed under `cache/` and documented "Pure
cache", but nothing rebuilds it: it holds "content-addressed copies of the bytes
a human approved at review", written by `review`, and `remote pull` does not
produce them. A clone that inherits committed `approvals/*.sig` gets the
signatures without the snapshots, so update review degrades from a diff to a
full-content dump. The doc anticipates the degradation; the filing implies a
rebuild that does not exist. VERIFIED for the filing and the absence of a
rebuild path; INFERRED for the observable degradation.

**`.ctxloom/sessions/*.md`.** Gitignored, no rebuild. Fine as a category — it is
this machine's history — but it is the same tier as the two above and lives in a
different place.

**Not in the project at all:** `~/.ctxloom/sessions/<harp>/` holds plans
(`paths.PlanFileExt`), essences, and the canonical transcript. Nothing under
`.ctxloom/` points at them. A new machine loses every plan.

### Where classification disagrees with `.gitignore`

- **`.ctxloom/agents/` and the `agents:` block are committed** while the schema
  calls them LOCAL ONLY (#3).
- **`.ctxloom/pieces/` and `.ctxloom/ephemeral/` are ignored and have no
  writer.** VERIFIED: `paths.EphemeralDirName` is used only under a session harp
  dir, which is home-rooted (`paths.HarpEphemeralDir`); "pieces" appears in no
  Go file outside `gitignore.PrivateStatePatterns`. Two of the five private-state
  patterns guard nothing.
- **`!.ctxloom/plans/` is vestigial.** No writer produces that directory
  (VERIFIED: plans live in harp session dirs). It is also inert as written — a
  negation cannot re-include files under an ignored directory, and no rule above
  it ignores `plans/` anyway.
- **`.ctxloom/cache/refused_advances.yaml` is correctly filed** — the writer,
  `operations.saveRefusedAdvances`, replaces or deletes the whole file each
  upgrade round, and `operations.LiveRefusedAdvances` re-validates against the
  live lockfile at read time. This is the exemplar the rest of the tree should
  match.

**The known lead is STALE.** `config.Config.GetBundleDirs` resolves
`paths.LocalBundlesPath(appPath)` — the committed `content/bundles` — and the
cache is deliberately absent. It further records a fatal `ClassMigration`
finding when authored bundles are found in the old cache location
(`config.legacyCacheBundlesSignpost`), and the behaviour is pinned by
`config.TestGetBundleDirs_ResolvesCommittedContentTree` and
`TestGetBundleDirs_ExcludesCacheBundles`. VERIFIED; nothing to fix.

### What `setup`/`install` re-grabs

VERIFIED, `cli.runManageInstall`: `manage install` scaffolds `.ctxloom` if
absent (`operations.InitializeProject`), maintains `.gitignore`, and applies
hooks/MCP/statusline. **It fetches nothing.** The re-fetch is `ctxloom remote
pull` (and the `sync.auto_sync` path in `cli.run` / `cli.mcp_server`), from
`lock.yaml` + `remotes.yaml`, and it reconstructs `cache/bundles` +
`cache/repos` and nothing else.

So the honest statement of the round trip is: **a clone plus `remote pull` is
complete for CONTENT and empty for STATE**, and nothing tells the user which
state it does not have.

---

## Proposed API surface

### 1. The scope policy

```go
// Package layerscope states which config LAYER may set which config KEY, and
// why: a value is a fact about a machine, a user, a project, or one
// invocation, and a layer that cannot carry that fact must not set it.
package layerscope // internal/config/layerscope

// Layer is one rung of the resolution chain, in ascending precedence.
type Layer uint8

const (
	LayerHome Layer = iota // ~/.ctxloom/config.yaml
	LayerProject           // <project>/.ctxloom/config.yaml — COMMITTED, multi-author
	LayerEnv               // CTXLOOM_CONFIG_* — ambient, inherited by every child process
	LayerFlag              // --config-set — one invocation, crosses no process boundary
)

func (l Layer) String() string

// File reports the path a user must edit to set a key at this layer, or ""
// for the two override layers. It is what a violation's fix-it names.
func (l Layer) File(appPath, homeAppPath string) string

// Scope names what a key's value is a fact ABOUT. It is the whole policy:
// Allows is derived from it, never listed per key.
type Scope uint8

const (
	ScopeShared     Scope = iota // the project, for everyone who clones it
	ScopeMachine                 // this box's filesystem, binaries, runtimes
	ScopePreference              // this person's taste; harmless at any layer
	ScopeInvocation              // this one run
	ScopeNever                   // prior human authorization; belongs in an admission store
)

func (s Scope) Allows(l Layer) bool

// Why is the one sentence a violation leads with, e.g. ScopeMachine's "this
// value names something on one machine, and a committed project file is read
// by every clone".
func (s Scope) Why() string
```

```go
// Rule binds one dotted config path to the scope its value is a fact about.
// A "*" segment matches exactly one path segment, so a wildcard map level
// (agents.<name>, llm.configs.<label>) is addressable per LEAF.
type Rule struct {
	Path  string
	Scope Scope
	Note  string // the key-specific reason, appended after Scope.Why()
}

// Policy is a rule set resolved by LONGEST MATCH, so a group rule
// ("llm.configs.*", ScopePreference) is narrowed by a leaf rule
// ("llm.configs.*.binary_path", ScopeMachine) without restating the group.
type Policy []Rule

func (p Policy) Lookup(path []string) (Rule, bool)

// Check walks one layer's DECODED values and reports every key that layer may
// not set. It never mutates values — the caller drops.
func (p Policy) Check(l Layer, values map[string]any) []Violation

// DefaultPolicy is ctxloom's table. It is EXHAUSTIVE against the config schema
// by test: a key the schema knows and the policy does not is a failure, so no
// key can be added without its scope being decided.
func DefaultPolicy() Policy

// Violation is one key at a layer that cannot carry it.
type Violation struct {
	Path  []string
	Layer Layer
	Rule  Rule
}

// Message states the key, the layer, why, and where it belongs instead.
func (v Violation) Message(appPath, homeAppPath string) string

// FixIt is the edit that clears it.
func (v Violation) FixIt(appPath, homeAppPath string) string
```

### 2. Wiring it into the file layers

`config.loadConfigLayer` is the one place layer identity survives; it gains the
layer and applies the policy where it already applies schema validation.

```go
// internal/config — signature change, unexported
func loadConfigLayer(cfg *Config, layer layerscope.Layer, configPath string,
	validator *schema.ConfigValidator, fs afero.Fs) (values map[string]any, pending *upgrade.Pending, err error)
```

```go
// internal/config — new warning kind, joining the five in warnings.go.
//
// WarnKindLayerScope: the layer carries a key whose value cannot be a fact
// about that layer — a machine path in a committed project file, a project
// key filled in from a user's home config, a privilege grant from an
// environment variable. The value is DROPPED, exactly like WarnKindUnknownKey:
// a setting that looks applied and is not is the worse outcome.
const WarnKindLayerScope WarningKind = "layer-scope"
```

`StrictnessClass()` returns `strictness.ClassConfig`; `FixIt()` returns the
violation's own fix-it. No new gate, no new flag — strict refuses, `--degraded`
warns and continues.

### 3. Wiring it into the override layers

`confload` must stay free of ctxloom's schema, so this mirrors the existing
`Product.KnownPath` hook exactly.

```go
// internal/shared/confload

// OverrideSource distinguishes the two override channels, which differ in
// REACH, not just syntax: env is inherited by every child process this one
// spawns; a flag crosses no process boundary.
type OverrideSource uint8

const (
	SourceEnv OverrideSource = iota
	SourceFlag
)

func (s OverrideSource) String() string

// Product gains one field:
//
//   - ScopeAllows, if set, reports whether an override from source may set
//     path (case-insensitive segments, already lower-cased), and why not when
//     it may not. Nil means "no scope knowledge available": every override is
//     allowed, which is today's behaviour verbatim. A false verdict makes
//     ApplyOverrides DROP that override and join its reason into the returned
//     error, exactly as it already does for an ambiguous one — partial, never
//     fatal, per this package's "Errors are non-fatal and partial" contract.
ScopeAllows func(source OverrideSource, path []string) (ok bool, why string)
```

The caller-side predicate lives beside `ctxloomProduct`'s existing `KnownPath`:

```go
// internal/config
func scopeAllows(source confload.OverrideSource, path []string) (bool, string)
```

### 4. The third `.ctxloom` tier

```go
// internal/paths

// StateDir is the THIRD tier under .ctxloom, beside ContentDir (committed) and
// CacheDir (derived). It holds LOCAL-ONLY state: a fact about this checkout on
// this machine that must never be committed (a clone would arrive carrying
// somebody else's answer) and that nothing can reconstruct from pins. It is
// gitignored, and its ABSENCE is reportable rather than silently defaulted —
// that reportability is the whole reason it is a named tier and not a
// scattering of loose files.
const StateDir = "state"

func StatePath(appPath string) string

// DirtyTreeCommitAckPath returns the record that a human authorized ctxloom to
// commit on their behalf in THIS checkout. It leaves config.yaml because the
// config chain has three channels an agent can write (a home file, an
// environment variable, an argv) and consent has none.
func DirtyTreeCommitAckPath(appPath string) string
```

```go
// internal/paths

// Tier classifies one .ctxloom path by WHAT A FRESH CLONE GETS.
type Tier uint8

const (
	TierCommitted Tier = iota // the clone has it
	TierDerived               // the clone rebuilds it from pins
	TierLocal                 // the clone has neither, and nothing can rebuild it
)

// Entry is one classified .ctxloom path.
type Entry struct {
	Rel     string // ".ctxloom/cache/bundles"
	Tier    Tier
	Rebuild string // the command that reconstructs it; "" iff TierLocal
	Lost    string // TierLocal only: what a clone does not have, in a user's words
}

// Layout is the classification of every path any writer in this tree produces,
// each appearing exactly once. An arch test walks the paths package against it,
// so a new path constant cannot be added without a tier.
func Layout() []Entry
```

`doctor` gains one check over `Layout()`: report each `TierLocal` entry that is
absent, with its `Lost` text. That is the thing a fresh clone has no way to
learn today.

---

## Open decisions

**1. Drop, or honour, at a disallowed layer?**
*Recommend: drop, as `WarnKindLayerScope`, fatal-class.* Honouring leaves all
three measured bugs live. Trade-off: an existing home config that sets a shared
key stops working — loudly, with the fix named, and `--degraded` runs anyway.

**2. May home fill a gap in a `ScopeShared` key the project left unset?**
*Recommend: no — a home value for a shared key is a violation whether or not the
project set one.* Gap-filling IS bug #2; there is no version of it that is safe
only when the project stayed silent. Trade-off: a solo developer who kept one
`agents:` block in home for all their projects must copy it per project, or the
project must gain a shared-agents mechanism (a bundle) — which is what bundles
are for.

**3. Do agent bindings deep-merge across layers at all?**
*Recommend: no. An agent is defined by exactly one layer — the highest that
names it.* Per-leaf scopes alone do not fix #2, because `permissions` and
`profiles` legitimately have different scopes and the merge fuses them into a
binding neither file describes. Trade-off: "home sets my default runtime for
every project's agents" is lost; the top-level `runtime:` key already serves it,
and that is what it is for.

**4. Split the project file, or keep one file and a table?**
The stricter shape is two project files: `.ctxloom/config.yaml` (committed,
`ScopeShared` only) and `.ctxloom/state/config.yaml` (gitignored, machine and
preference), making the chain five layers and enforcing scope by FILE rather
than by lookup. *Recommend: table first, split later if the table proves
noisy.* The table is a prerequisite either way — you cannot decide which file a
key goes in before deciding its scope — and a fifth layer invalidates every
precedence doc, fragment, and test in the family at once. Trade-off: until the
split, a machine key in the committed file is caught at load rather than
impossible by construction.

**5. Where does `dirty_tree_commit_ack` go?**
*Recommend: `.ctxloom/state/`, as an `admission.Store` file, matching
`HomeCompanionConsentPath`'s mechanism.* Home is wrong (it would grant the
behaviour for every project on the box); the committed file is wrong (a clone
arrives pre-authorized); `cache/` is wrong (derived means deletable, and a
consent that silently reappears is worse than one that is silently absent).
Trade-off: it stops being editable by hand in the file people already open, so
`init`'s interview and a `ctxloom manage` verb must both write it.

**6. Does the env layer keep any privilege reach?**
*Recommend: env may set `ScopeInvocation` and `ScopePreference` only.* Env is
the one channel an agent inherits by construction. Trade-off: a CI pipeline that
sets permissions via environment breaks and must pass `--config-set`. That is
the right shape — argv is auditable in a process listing and is not inherited.

**7. Do `pieces/` and `ephemeral/` stay in `PrivateStatePatterns`?**
*Recommend: delete both, and say so in the pattern list's doc.* They guard
nothing. Trade-off: nil, unless a writer exists outside this repo — which is why
this is a decision and not a fix.

---

## What is not established

- **Whether a clone silently loses its task log.** The resolution rules
  (ADR 0025 §4–5) say a no-marker tree mints a new project; whether that path
  emits a warning was read, not run. Settled by cloning a project with a
  populated log into a fresh path and running `taskloom list`.
- **Whether the missing `cache/trust/objects` is observable.** The filing and
  the absent rebuild path are verified; the degraded review experience is
  inferred from `paths.TrustObjectsPath`'s own doc. Settled by a review-update
  cycle against a tree with the directory removed.
- **How often home configs carry project-scoped keys in the wild.** One data
  point (this machine's `~/.ctxloom/config.yaml` declares two agents, one of
  which shares a name with a project agent) is not a distribution.
- **Whether `.ctxloom/approvals/` is populated in any real project.** The write
  path exists (`operations.resolveCountersignStore`, `project` branch) and is
  reachable only via `review --project`; no tree examined here has the
  directory.
