# hew conformance corpus — v0

**This directory is the standard.** Where `docs/design/hew-spec.md` and these cases disagree,
the corpus wins and the spec has a bug. The Go implementation and the later Rust port are
conformant exactly insofar as they pass this directory — that is the single decision that
makes "port to Rust" a port rather than a rewrite-and-diverge.

Nothing here is executed yet. There is no parser, no applier, no differ and no CLI. These are
the fixtures those components will be built against, and they are reviewable on their own.

---

## What a case is

One directory per case, named `<format>/<case-name>/`. Every case carries a `case.yaml`
manifest and the fixtures its declared seams need.

```
tests/hewcorpus/
  README.md
  json/      jsonc/     yaml/     toml/     hcl/     markdown/     cli/
```

## The five seams

A case declares which seams it pins. **A runner must run each declared seam separately and
assert only that seam's output.** An end-to-end-only corpus cannot tell a parser bug from an
applier bug, and those are written by different people in different languages.

| Seam | Input → output | Isolates |
|---|---|---|
| `parse` | `patch.hew` → `transforms.hewt` | The notation. No target file, no backend. A Rust port passes every `parse` case before it has a single format binding. |
| `apply-ir` | `transforms.hewt` + `target.*` → `expected.*` | The applier: format mechanics and byte preservation, notation entirely out of the picture. |
| `e2e` | `patch.hew` + `target.*` → `expected.*` | The composition — what a user experiences. |
| `render` | `transforms.hewt` → `.hew` → `transforms.hewt` | The renderer, via IR identity (RT2). |
| `diff` | `old.*` + `new.*` → `expected.hew` | The differ's determinism and context radius (spec §9.4). |
| `cli` | `argv` → exit code + streams | The CLI contract. |

A directory carrying `patch.hew`, `transforms.hewt`, `target.*` and `expected.*` pins the
first three seams from one set of fixtures.

## Files

| File | Present when | Contents |
|---|---|---|
| `case.yaml` | always | The manifest. |
| `patch.hew` | all seams but `apply-ir` and `diff` | The patch. |
| `transforms.hewt` | `parse`, `apply-ir`, `render` | The canonical transform list — expected output of the parser *and* input to the applier. |
| `target.<ext>` | `apply-ir`, `e2e` | The input document, byte-exact. |
| `expected.<ext>` | success cases | The expected output, compared **byte for byte**. |
| `expected-ops.json` | optional | The *resolved* RFC 6901 op list, for interop pinning. |
| `old.*` / `new.*` / `expected.hew` | `diff` | The differ's inputs and output. |

## Manifest fields

```yaml
name: yaml/keyed-array-add     # must equal the directory path
seams: [parse, apply-ir, e2e]
kind: ok                       # ok | error | cli
format: yaml
ops: [OP-16]                   # operations-catalog entries this case pins (spec §11)
why: |                         # what rule this case exists to pin, in spec terms
  ...
spec: "§6.2, §11 OP-16"
```

Error cases add `error` (the code), `error_seam` (which component must raise it),
`error_path`, `patch_line`, and `message_contains`. **`error_seam` is itself an assertion:** a
`HEW001` that surfaces at `e2e` instead of `parse` means the parser deferred work it should
have refused.

CLI cases add `argv`, `exit`, and optionally `stdout` / `stderr_contains` /
`target_unchanged`.

## Runner obligations

1. Copy the case directory to scratch before running — in-place cases mutate.
2. Run each declared seam independently. Collapsing seams is non-conformant.
3. Compare bytes **exactly**: no normalization, no trailing-newline tolerance, no whitespace
   folding. Byte preservation is the contract; a lenient comparison silently retires it.
4. For `error` cases, assert the code, the seam, the path, and every `message_contains`
   substring — **and** that the target is byte-unchanged.
5. An unknown seam or kind, or a missing declared file, is a **corpus error**, not a skip.
   Silent skips are how a conformance suite lies.
6. Report catalog coverage: every operation marked `v0` in spec §11 must appear in at least
   one case's `ops:` list. All 40 are covered today; a new v0 operation with no case is a gap
   the runner must name.

## Two case families that are not about one operation

**Tolerance** (`*/tolerance-*`) — one per format family, pinning spec §6.4's table: keys
reordered, keyed-array elements reordered, reformatting, unrelated edits. These are the cases
that show why hew is not `patch(1)` with a structural parser: they *survive*, and under
`patch(1)` every one of them would break a hunk.

**Round trip** (`*/roundtrip-basic`) — one per implement format, pinning
`apply(parse(render(diff(old, new))), old) == new` byte for byte. A failure anywhere in the
four components fails this, which is why the seam decomposition above matters: RT1 says
*something* is wrong, the seams say *what*.

## Severability

`markdown/` is severable. Markdown's place in the implement tier is deferred to the evaluation
in spec §8.7 (open question O29) — it may be better served by plain `patch(1)`, since
reorder-blindness is hew's largest win over `patch(1)` and is worth approximately nothing for
prose, where order *is* the content. No case outside `markdown/` depends on a Markdown
fixture, so dropping the dialect is the removal of one directory.

## Two fixtures that are not files

- `cli/diff-git-anchor` needs a git repository. **No `.git` directory is committed** — a
  committed repo is unreviewable and unportable. The case's `fixture:` field states the
  commands the runner runs in the scratch copy to build one.
- `cli/apply-in-place` and friends mutate their target; obligation 1 covers them.

## Counts

| Directory | Cases |
|---|---|
| `json/` | 17 |
| `jsonc/` | 5 |
| `yaml/` | 22 |
| `toml/` | 8 |
| `hcl/` | 9 |
| `markdown/` | 7 (severable) |
| `cli/` | 8 |
| **total** | **76** |
