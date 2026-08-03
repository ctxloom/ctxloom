# Apply-time hook reorder — design (NOT BUILT)

**Status:** design only, 2026-08-03. No code accompanies this document. Parts 1
and 2 of `operable-bright` (order-as-data, `manage hooks list`) ARE built; this is
part 3, which was explicitly scoped as design-and-stop.

Written against `internal/config.extractHooksFromBundle`,
`backends.AssembleManagedHooks`, `operations.ResolveHooks`, and
`wire.HookOrderLess` as they exist at this commit.

---

## 1. The problem, stated precisely

A bundle's `order:` sequences its OWN hooks within an event. It cannot say
anything about other bundles, and it should not: bundles are frequently remote,
and a bundle author has no view of the other bundles a consumer composes.

So the CROSS-bundle sequence is still emergent from merge order
(`config` → inline profiles → gated directory profiles → builtins → companions →
profile bundles), and a consumer who needs `acme/audit`'s `pre_tool` hook to run
before `builtin:core`'s has exactly one lever today: edit a bundle that is not
theirs to edit.

## 2. The constraint that shapes everything else

> An override that silently disagrees with what inspection reports would be worse
> than no override at all.

This is not a caveat to bolt on at the end; it dictates WHERE the override
executes. `operations.ResolveHooks` deliberately calls the same
`backends.AssembleManagedHooks` the apply path calls, precisely so there is one
merge implementation and not two.

**Therefore: the override must be applied INSIDE `AssembleManagedHooks`, not by
either caller.** Any design that reorders in `applyHooksToBackend` (or in the CLI)
creates a second ordering authority, and inspection would report the pre-override
order while the engine ran the post-override one — the exact failure named above.

The corollary is a test, not a comment: an acceptance scenario must assert that
`manage hooks list` and the hooks written into `.claude/settings.json` agree,
WITH an override in force.

## 3. Where the override lives

**Project config, at `hook_order:` in `.ctxloom/config.yaml`.**

Config keys, exactly:

```yaml
hook_order:
  pre_tool:
    - bundle: acme/audit          # by origin, all of that bundle's hooks
    - bundle: builtin:core
  session_start:
    - match: "ctxloom hook inject-context"
```

Rejected alternatives, with the reason each loses:

- **Profile** (`.ctxloom/profiles/<name>.yaml`). Profiles compose and inherit, so
  two profiles in one scope could each declare a different order for the same
  event, and the merge would have to invent a winner. Ordering is a property of
  the ASSEMBLED set, and the assembled set is a project-scoped fact.
- **A new file** (`.ctxloom/hook-order.yaml`). One more place to look for
  behaviour, for a key that is a handful of lines. `config.yaml` already holds
  `hooks:`, so hook policy has a home.
- **Inside a bundle.** This is the thing the task exists to avoid.
- **Home/global config.** Hook order depends on which bundles a project composes;
  a global default would be wrong in every project that composes differently.

## 4. Signatures

New, in `internal/config`:

```go
// HookOrderOverride is one project-level ordering rule for one event.
// Exactly one of Bundle or Match must be set; both or neither is a load error.
type HookOrderOverride struct {
    Bundle string `yaml:"bundle,omitempty"` // origin ref, as reported by ResolvedHook.Source
    Match  string `yaml:"match,omitempty"`  // exact hook command or prompt
}

// HookOrderConfig maps each lifecycle event to its override sequence.
type HookOrderConfig map[string][]HookOrderOverride

// configDoc gains:
//     HookOrder HookOrderConfig `yaml:"hook_order,omitempty"`

// GetHookOrder returns the project's hook-order overrides (nil when none).
func (c *Config) GetHookOrder() HookOrderConfig
```

New, in `internal/shared/wire` (beside `HookOrderLess`, so the whole ordering
vocabulary stays in one package):

```go
// HookOrderRank returns a hook's rank under an override sequence, and ok=false
// when no rule names it. Unnamed hooks are NOT reordered.
func HookOrderRank(rules []HookOrderRule, h Hook) (rank int, ok bool)

// HookOrderRule is the wire-level shape of one rule, decoupled from config's
// YAML type so wire keeps depending on nothing.
type HookOrderRule struct {
    Bundle string
    Match  string
}

// ApplyHookOrder stable-sorts one event's resolved hooks under rules, returning
// the hooks it could not place (every rule that matched nothing).
func ApplyHookOrder(hooks []Hook, rules []HookOrderRule) (unmatched []HookOrderRule)
```

New, in `internal/lm/backends` — the ONE call site:

```go
// applyHookOrderOverrides reorders each event of u under cfg's hook_order,
// after every source has been merged in and before the assembled set is
// returned. Called from AssembleManagedHooks; nothing else may call it.
func applyHookOrderOverrides(u *wire.UnifiedHooks, cfg *config.Config) []string
```

`operations.ResolvedHook` gains one field, and this is what makes the effect
visible rather than merely applied:

```go
type ResolvedHook struct {
    // ...existing fields...

    // Overridden reports that a hook_order rule placed this hook, rather than
    // its position being the merge's own output. Position ALWAYS reports the
    // post-override position; this says whether that position was chosen.
    Overridden bool `json:"overridden,omitempty"`
}

type ResolveHooksResult struct {
    // ...existing fields...

    // UnmatchedOverrides names hook_order rules that matched no hook. Reported
    // rather than dropped — see §6.
    UnmatchedOverrides []UnmatchedOverride `json:"unmatched_overrides,omitempty"`
}

type UnmatchedOverride struct {
    Event string `json:"event"`
    Rule  string `json:"rule"`   // "bundle:acme/audit" or "match:<command>"
}
```

## 5. How it composes with bundle-declared order

Three layers, each strictly narrower in scope than the one below:

1. **A bundle's `order:`** sequences that bundle's hooks within an event.
   Consumed in `config.extractHooksFromBundle`. Unchanged by this design.
2. **Merge sequence** orders the sources. Unchanged.
3. **`hook_order`** is a project-level PERMUTATION of the result of 1 and 2.

Rule: **an override moves hooks; it never invents, drops, or edits one.** The
output is a permutation of the input, and that is worth asserting directly —
`ApplyHookOrder` must be provably length- and multiset-preserving, because a
reorder that could delete a hook is a silent security change (a `pre_tool` guard
that stops running) wearing the costume of a formatting preference.

Hooks named by no rule keep their relative order and sit AFTER every hook a rule
placed. Same reasoning as `wire.HookOrderLess`'s absent-sorts-last: an explicit
claim beats no claim, and a rule the user did write must not be overtaken by a
hook they never mentioned.

A `bundle:` rule places all of that bundle's hooks for that event as a contiguous
block, preserving their bundle-declared order among themselves — otherwise the
override would silently destroy layer 1.

## 6. When a rule names a hook that no longer exists

**Report it; do not fail, and do not silently drop it.**

- `manage hooks list` prints it under `unmatched_overrides`, and exits non-zero
  under `--strict` only.
- `manage hooks install` emits `clidiag.Warn` per unmatched rule and still
  applies.

The reasoning, both directions:

- **Hard-failing is wrong.** Bundles are remote and move under the consumer.
  `remote upgrade` dropping a hook would then break every subsequent apply for a
  preference that no longer has a subject — a bundle author would be able to
  break a consumer's project by renaming their own command.
- **Silence is worse.** A stale rule is a user's belief about ordering that is no
  longer true. Leaving it inert and unmentioned means the belief survives while
  the behaviour has changed underneath it, which is exactly the class of failure
  the inspection surface exists to close.

Rules are matched by ORIGIN (`bundle:`) or by exact command text (`match:`).
Neither is a hook's tree identity (`<event>/<name>`), because on the apply path
that identity does not survive the merge — `wire.Hook` carries only `SCM`. If a
future change carries tree identity through to `wire.Hook`, a third rule form
`ref:` should be added and preferred; it would be the only stable one.

## 7. Trust

Hooks are arbitrary-command executables. Three claims, in order of how easily
they are gotten wrong:

1. **Reordering is not a trust decision, and must not be able to become one.**
   `hook_order` cannot admit a hook the executable gate denied, because the gate
   runs in `config.extractHooksFromBundle` — strictly upstream — and
   `ApplyHookOrder` operates on the already-gated slice. This is a consequence of
   the call-site choice in §2, not a separate check, and it is why the override
   must not be moved earlier.

2. **Reordering does not stale any signature or approval.** A hook's trust
   identity on the apply path is `<bundle>#hooks/<event>/<authored-index>`, and
   in the tree it is `<event>/<name>`; neither mentions resolved position.
   `hook_order` lives in project config and touches no bundle bytes. Nothing to
   re-approve — a deliberate contrast with the retired `<NN>-` ordinal, where
   reordering rewrote filenames and invalidated approvals.

3. **Reordering can still change what a hook DOES, and the docs must say so.**
   `pre_tool` hooks can block a tool call. Moving a permissive hook ahead of a
   restrictive one can mean the restrictive one never runs. So `hook_order` is a
   security-relevant project setting even though it is not a trust decision:
   trust asks "may this run", order asks "does it get the chance". `hook_order`
   therefore belongs in `.ctxloom/config.yaml`, which is committed and reviewable,
   and NOT in any home-global or environment-variable form where it would be
   invisible to a reviewer of the project.

## 8. Test obligations before this ships

- `ApplyHookOrder` is a permutation: same length, same multiset, for arbitrary
  rule sets including contradictory ones.
- More than nine hooks in one event, since that is where any width or lexical bug
  shows.
- A gate-denied hook cannot be resurrected by a rule naming it.
- **The agreement test**: with an override in force, the sequence
  `manage hooks list --format json` reports and the sequence written into
  `.claude/settings.json` are the same. This is the constraint in §2 made
  executable, and it is the one test whose absence would make this feature worse
  than not shipping it.
