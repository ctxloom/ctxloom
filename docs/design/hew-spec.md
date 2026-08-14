# Hew: a structured patch format — specification, v0 draft

**Status: DRAFT for human review (P1 gate).** Nothing here is implemented. No parser,
no backend, no CLI exists. This document plus `tests/hewcorpus/` is the deliverable.

**Name (human, 2026-08-14, final): the tool and the standard are `hew`.** Binary `hew`,
extension `.hew`, transform lists `.hewt`, CLI verbs `hew apply` and `hew diff`. *Structured
patch* remains the generic descriptive phrase for what hew is; **hew** is the proper noun.
This supersedes the working name "Structured Patch (SP)" used while the workstream was scoped
in `ugly-icy-squid/structured-patch-standard.plan.md`; the error-code prefix moved with it
(`SP010` → `HEW010`). Formats implemented: JSON,
JSONC, YAML, TOML, Markdown, HCL. Formats documented only: INI-family, dotenv, XML (§12).

**Notation (human, 2026-08-14, superseding the earlier hybrid ruling): Hew v0 is one grammar.**
A shape-mirroring body with in-place `+`/`-` margins and match/assert annotations. There is
**no op-list escape hatch** — not fenced, not severable, not present. The two cases the hatch
was going to carry are absorbed into the mirror grammar itself: HCL repeated-label blocks are
selected with an ordinal annotation (§7.2, the survey's design-A rendering made normative),
and assert-only patches are annotation-only mirrors (§7.4). The operations the mirror grammar
still cannot say — node moves and copies — are written as a **transform list** (§9.6), the
IR's canonical serialization, which had to exist for the corpus and for RFC 6902 interop
anyway and is therefore also an accepted input. **Appendix C** is the honest inventory of that
boundary. There is no third surface and none is reserved.

**Architecture (human, 2026-08-14): four components around one IR.** Parser (`.hew` → IR) and
renderer (IR → `.hew`) are notation-side inverses; differ (two sources → IR) and applier
(IR + file → file) are format-side inverses. The IR — the **transform list** — is the internal
boundary, the interop surface, and what the corpus pins (§9, §13.1).

**Derivation order.** The operations catalog (§11) is a survey of every verb in the prior art
plus ctxloom's own eight mechanisms; the catalog's *IR record* rows define the transform-list
schema (§9.6); the schema defines the Go signatures (Appendix A). Never the reverse — a future
catalog entry extends the IR by construction.

**Reduction.** The survey is exhaustive; the normative set is not. 52 catalogued operations
reduce to **five IR primitives** (`test`, `add`, `remove`, `replace`, `copy`) plus addressing
and a minimal qualifier set — *the IR is essentially ASM*. Everything else is either sugar the
parser compiles away or an explicit `OUT` with a reason (§11.10).

**Tolerance.** `patch(1)`'s fuzz is the wrong benchmark. Hew addresses nodes by path, so
reordered keys, reordered keyed arrays, reformatting, and unrelated edits are **invisible** to
a patch — while value drift, missing nodes and ambiguous matches fail loudly and by name. The
normative table is §6.4.

**The corpus is the standard.** Where this prose and `tests/hewcorpus/` disagree, the corpus
wins, and the disagreement is a spec bug to be fixed here. The Go implementation (P2/P3) and
the later Rust port (P6) are conformant exactly insofar as they pass the same corpus.

---

## Table of contents

1. [Why Hew exists, and the one property everything else serves](#1-why-hew-exists)
2. [File format: preamble, file sections, hunks](#2-file-format)
3. [Margins: the six column-1 characters](#3-margins)
4. [Hew paths: the address grammar](#4-hew-paths)
5. [Hunk semantics: the two projections](#5-hunk-semantics-the-two-projections)
6. [Context, position, exhaustiveness — and the tolerance model](#6-context-position-and-exhaustiveness)
7. [Annotations: `?` assertions and `!` directives](#7-annotations)
8. [Per-format binding: what a body line *is*](#8-per-format-binding)
9. [The transform list: Hew's IR, and the four components around it](#9-the-transform-list)
10. [Error taxonomy](#10-error-taxonomy)
11. [**Operations catalog** — every surveyed verb, in or out, and why](#11-operations-catalog)
12. [Documented-only formats](#12-documented-only-formats)
13. [The conformance corpus: three seams and two round-trip identities](#13-the-conformance-corpus)
14. [Appendix A — proposed Go API surface](#appendix-a--proposed-go-api-surface)
15. [Appendix B — proposed CLI surface](#appendix-b--proposed-cli-surface)
16. [Appendix C — operations the mirror grammar cannot express](#appendix-c--operations-the-mirror-grammar-cannot-express-non-normative)
17. [Open questions for ratification](#open-questions-for-ratification)

---

## 1. Why Hew exists

`patch(1)` won over `ed` scripts because a reviewer can read one hunk and see both the change
and its surroundings without mentally executing anything. Every structured-data patch format
in the survey lost that property: op-lists (RFC 6902, go-patch, RFC 5261, jd) read as
instructions, and shape-mirroring overlays (RFC 7386, strategic merge, spruce, CUE, jsonnet)
either restate whole arrays to change one element or cannot express deletion at all.
Coccinelle's SmPL is the only surveyed notation that shape-mirrors *and* keeps `-`/`+` margins
in place — and it silently no-ops when its match fails.

Hew is that shape-mirroring-with-margins notation, generalized off C and onto config trees,
with SmPL's one gap closed. **The governing property, from which the rest of this spec is
derived:**

> A hunk that does not match its target is an **error with a named cause and a location**.
> Never a no-op. Never a best-effort apply. Never a fuzzy line-offset guess.

That is ctxloom's silent-no-op discipline (exit 0, success message, zero bytes written)
applied to a file format. Three concrete consequences, stated up front because they surprise
people who expect `patch(1)`:

- **No fuzz factor.** Hew has no analogue of `patch --fuzz`. Context either matches or the
  apply fails.
- **Atomic per target file.** Either every hunk against a file applies or none does. There
  are no `.rej` files and no partially-patched output. (§10.5)
- **Already-applied is a failure, not a success** — by default. A `-` line whose node is
  absent is `HEW010 stale-target` even if the corresponding `+` line's node is already
  present. The `! idempotent` directive (§7.5) opts a hunk into treating that case as
  satisfied; see [O3](#open-questions-for-ratification), because ctxloom's own managed-file
  mechanisms are idempotent by construction and will want it.

**And the second property, from the v0 notation ruling:** there is exactly one way to write a
patch. A reader of a `.hew` file never has to learn a second grammar, and a reviewer never has
to ask why *this* edit was written as instructions when *that* one was written as shape. The
price is paid in Appendix C, honestly and in one place.

---

## 2. File format

An Hew file is UTF-8 text, LF-terminated lines. A trailing newline on the last line is
optional. The file is **line-oriented at the top level**: the first character of every line
is structural.

```
patchfile   := preamble filesection+
preamble    := ( comment | blank | directive )*
directive   := key ":" Hew value LF
filesection := targetline ( hunk | comment | blank )+
targetline  := "--- " path [ Hew attrlist ] LF
hunk        := hunkheader bodyline*
hunkheader  := "@@ " address " @@" [ Hew attrlist ] LF
bodyline    := margin Hew text LF | comment | blank
comment     := "#" [ Hew text ] LF
attrlist    := attr ( Hew attr )*
attr        := key "=" value
```

### 2.1 Preamble

```
# ctxloom v0.7 → v0.8 settings migration
hew: 1
format: yaml
```

- `hew:` — **required**, must be the first non-comment, non-blank line. Value is the format
  version integer. This document specifies `1`. A reader seeing an unknown version MUST fail
  with `HEW002` rather than attempt a best-effort parse.
- `format:` — optional default format for every file section that does not declare its own.

No other preamble keys are defined in v0. An unknown preamble key is `HEW001` (fail loud;
forward-compatible ignoring is deliberately not offered — see [O9](#open-questions-for-ratification)).

### 2.2 File sections — the `---` target line

Borrowed verbatim from unified diff's visual grammar, and from kustomize's lesson that a
patch document needs to say *which* thing it patches:

```
--- .claude/settings.json format=jsonc
--- config.yaml
--- pyproject.toml
```

- The path is relative to the apply root (the CLI's `--root`, default: the process working
  directory). Absolute paths are legal; `..` traversal above the root is `HEW003`.
- `format=` is optional. Resolution order: `format=` attr → preamble `format:` → extension
  inference (§8.0) → `HEW021 unsupported-format`.
- A path containing a space must be quoted: `--- "my config.yaml"`.
- **Multiple file sections may name the same path.** Their hunks are merged and the atomicity
  rule (§10.5) covers the union.

Because a target line's first character is `-` and a hunk body line's first character is a
margin *followed by a space*, `--- ` (three dashes, space) can never be confused with a
removal line: a removal line is `- ` (one dash, one space) and its text would have to begin
`-- `. Removing a target line's worth of literal text is expressed as `- -- foo` with no
ambiguity for the parser.

### 2.3 Hunks and anchors

```
@@ /server @@
@@ /mcpServers/name=github @@
@@ / @@
@@ /provider/"aws" @@
```

A hunk header names the **anchor**: the Hew path (§4) of the node the body mirrors. `@@ / @@`
anchors at the document root.

The anchor node must exist and must resolve to exactly one node, or the hunk fails
(`HEW013 no-match` / `HEW012 ambiguous-match`) — with two exceptions: a trailing `?` on the last
path segment (§4.4) permits the anchor to be created, and a `! match ord=` first body line
(§7.2) selects among same-tuple siblings.

**Body indentation is relative to the anchor.** The body is written as though the anchor's
subtree were the whole document: the anchor's own container delimiters (`{`/`}` in JSON, the
`server:` key line in YAML, the `[table]` header in TOML, the `provider "aws" {` line in HCL,
the `## Heading` line in Markdown) are **not** written in the body. Only its children are.

```
@@ /server @@
  port: 8080
- timeout: 30
+ timeout: 60
```

is the same patch as

```
@@ / @@
  server:
    port: 8080
-   timeout: 30
+   timeout: 60
```

Choose the anchor that makes the hunk read best. A deeper anchor means less context to write;
a shallower anchor means more visible surroundings. Same trade unified diff makes with
`-U`/`--unified=N`, made per hunk instead of per file.

---

## 3. Margins

Column 1 is the margin. Column 2 is a mandatory single space. The body text begins at column 3.

| Margin | Name | Meaning |
|---|---|---|
| `` ` ` `` (space) | **context** | This node must exist and be equal. It is not modified. It pins insertion position (§6.2). |
| `-` | **remove** | This node must exist and be equal. It is deleted. |
| `+` | **add** | This node is created. It must not already exist (unless `! idempotent`). |
| `?` | **assert** | An annotation line carrying an assertion (§7.1, §7.4). Not part of either projection. |
| `!` | **directive** | An annotation line changing how application works (§7.2, §7.3, §7.5). Not part of either projection. |
| `#` | **comment** | An Hew comment. Ignored entirely. Not part of either projection. |

A completely blank line (zero characters, or whitespace only) is **insignificant**: it is a
visual separator and is ignored. To express a blank line in the *target*, see §8.6 — only
Markdown has a body-line notion of blank, and there it is structural rather than written.

A line whose column 1 is none of the six margin characters is `HEW001 parse-error`. There is
no "loose" mode.

**Target comments are ordinary body text.** A `#` comment in a YAML/TOML/HCL target, or a
`//` comment in JSONC, is written as context/add/remove like any other line:

```
@@ /server @@
  # ports below 1024 need CAP_NET_BIND_SERVICE
  port: 8080
+ # bumped for the slow upstream (ctxloom, 2026-08)
- timeout: 30
+ timeout: 60
```

The Hew margin `#` and the target's `#` never collide because the target's lives at column 3.

---

## 4. Hew paths

An Hew path addresses a node. It is **RFC 6901 JSON Pointer plus four extensions**, chosen so
that the common config edit is expressible without positional indices.

```
hewpath   := "/" | ( "/" segment )+
segment  := key | index | "-" | keymatch | label | heading | block | marker
```

There is **no ordinal segment**. Selecting among same-shaped siblings that a path cannot
distinguish is an annotation (§7.2), not an address — so that a path is always a statement
about identity and never about position in the file.

### 4.1 Key and index segments (RFC 6901, unchanged)

```
/server/timeout          object key "timeout" under object key "server"
/tags/0                  element 0 of the sequence "tags"
/tags/-                  the append position of "tags" (RFC 6901's literal "-")
```

Escapes: `~0` = `~`, `~1` = `/`, **and Hew adds `~2` = `=`** so that an object key containing
a literal `=` cannot be mistaken for a key-match segment. (Extending RFC 6901's escape set is
a compatibility decision — [O5](#open-questions-for-ratification).)

### 4.2 Key-match segments — `name=value`

Adopted from go-patch, whose `/instance_groups/name=zookeeper/instances` idiom is the survey's
only proven answer to keyed-array addressing:

```
/mcpServers/name=github            the element of "mcpServers" whose "name" field == "github"
/tool/x/id="a b"                   quoted value, for values containing spaces or "="
/servers/enabled=true              non-string scalars compare after format-native decoding
```

- The container must be a sequence. The field must be a direct child of each element.
- **Exactly one element must match**, or `HEW012 ambiguous-match` / `HEW013 no-match`. This is
  the loud-staleness rule applied to addressing: an array that grew a duplicate key is a
  drifted array and Hew says so by name.
- Values are compared **after decoding**: `port=8080` matches the number `8080` and the string
  `"8080"` is written `port="8080"`. Booleans and null are bare tokens `true`/`false`/`null`.
- Only equality is defined in v0. No regex, no substring, no numeric comparison
  ([O6](#open-questions-for-ratification)).

**The empty-field form — `/tags/=gamma`.** With no field name, the segment matches the
element that *is* the value, addressing scalar sequences by content rather than by index:

```
/tags/=gamma                       the element of "tags" equal to "gamma"
/permissions/deny/="Bash(curl *)"  quoted, for values with spaces
```

Same uniqueness rule: zero matches is `HEW013`, more than one is `HEW012` — a duplicated scalar
in a list is drift, and Hew names it instead of picking the first. This is the address the
differ prefers for primitive lists (§9.4-R4) and the one that makes a removal survive
reordering (OP-15, adopted from strategic merge's `$deleteFromPrimitiveList`).

### 4.3 Label segments — HCL blocks

An HCL block is keyed by a tuple of (block type, ordered labels), not by a name. Labels are
written as **quoted segments** following the block-type segment:

```
/provider/"aws"                    block: provider "aws" { ... }
/resource/"aws_instance"/"web"     block: resource "aws_instance" "web" { ... }
/terraform                         block: terraform { ... }  (no labels)
/provider/"aws"/region             the region attribute inside that block
```

A quoted segment is a label; an unquoted segment is an attribute name or a nested block type.
That is the whole disambiguation rule, and it works because HCL attribute names are bare
identifiers.

**A repeated `(type, labels)` tuple is `HEW012 ambiguous-match` unless the hunk carries an
ordinal annotation** (§7.2). The path stays an identity statement; the ordinal is a separate,
visible admission that identity was insufficient here.

### 4.4 Optional segments — trailing `?`

A trailing `?` on the **last** segment means "match it, or create it":

```
/mcpServers/name=ctxloom?          the ctxloom element, inserted if absent
/server/tls?                       the tls object, created if absent
```

Legal only on the last segment, and only on a hunk **anchor** (never inside `? expect`). Its
effect: `HEW013 no-match` at that segment becomes a creation instead of an error. Creation
inserts at the end of the container unless the body's context pins a position (§6.2).

### 4.5 Heading, block, and marker segments — Markdown

```
/# Getting started                       the h1 section with that exact heading text
/# Getting started/## Install            the h2 "Install" nested inside it
/# Install/code:0                        the first fenced code block in that section
/# Install/list:0                        the first list block
/# Install/para:1                        the second paragraph block
/@ctxloom:context                        a ctxloom managed-marker block (§8.6)
```

A heading segment is the literal heading marker plus a single space plus the exact heading
text. `/` inside heading text escapes as `~1`. Duplicate headings at the same level under the
same parent are `HEW012 ambiguous-match`.

Block segments are `<kind>:<ordinal>` where kind ∈ `para | code | list | table | quote | html`
and the ordinal counts **within that kind, within that section**. Markdown blocks have no
keys of any sort, so this is the one place a path carries a number that is not an array
index; see [O7](#open-questions-for-ratification).

### 4.5b Comment segments

Comments are nodes in JSONC, YAML, TOML and HCL (§8), so they need addresses. Two forms:

```
/server/#0                 the first standalone comment node inside "server"
/server/#2                 the third
/server/timeout/#t         the trailing comment on the "timeout" member
```

`#<n>` is kind-scoped within the container, exactly like a Markdown block ordinal. `#t` is the
trailing comment attached to the preceding member. Comment addresses are what let a comment be
removed or replaced as a node (OP-32, OP-33) and are why the mirror grammar's
comment-attachment spelling (OP-31) needs no IR qualifier of its own — it desugars into an
`add` at a comment address.

### 4.6 Relative paths in annotations

Inside a hunk, an annotation's path may begin with `.` meaning "relative to the enclosing
hunk's anchor":

```
@@ /server @@
? expect ./port = 8080
```

---

## 5. Hunk semantics: the two projections

This is the normative core of Hew, and it is what makes the notation implementable once per
format instead of once per operation.

**Every hunk body defines exactly two documents:**

- the **before-image** = every context and `-` line, margins stripped;
- the **after-image** = every context and `+` line, margins stripped.

Annotation and comment lines are in neither. Both images are parsed by the target format's
**fragment parser** as a fragment of the same node kind as the anchor.

Application is then defined in three steps:

1. **Match.** The before-image must match the target subtree at the anchor, under the
   matching rules of §6.1. Failure is `HEW010 stale-target`, naming the first mismatching Hew
   path and the patch line number.
2. **Diff.** The before-image and after-image are diffed at the *node* level, producing an
   ordered RFC 6902 op list (§9).
3. **Apply.** The op list is handed to the format backend, which mutates the target
   byte-preservingly and re-serializes.

Worked example. Target `config.yaml`:

```yaml
name: myapp
version: 1.2.0
server:
  host: localhost
  port: 8080
  timeout: 30
tags:
  - alpha
  - beta
mcpServers:
  - name: filesystem
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem"]
  - name: github
    command: npx
    args: ["-y", "@modelcontextprotocol/server-github"]
```

The survey's canonical four-operation change-set, as one Hew file:

```
hew: 1

--- config.yaml format=yaml

@@ /server @@
- host: localhost
  port: 8080
- timeout: 30
+ timeout: 60

@@ /tags @@
  - beta
+ - gamma

@@ /mcpServers @@
  - name: github
+ - name: ctxloom
+   command: ctxloom
+   args: [mcp]
```

Its projections, hunk by hunk:

| Hunk | before-image | after-image |
|---|---|---|
| `/server` | `{host: localhost, port: 8080, timeout: 30}` | `{port: 8080, timeout: 60}` |
| `/tags` | `[beta]` | `[beta, gamma]` |
| `/mcpServers` | `[{name: github}]` | `[{name: github}, {name: ctxloom, …}]` |

And the derived op list (§9):

```json
[
  { "op": "test",    "path": "/server/host",    "value": "localhost" },
  { "op": "test",    "path": "/server/port",    "value": 8080 },
  { "op": "test",    "path": "/server/timeout", "value": 30 },
  { "op": "remove",  "path": "/server/host" },
  { "op": "replace", "path": "/server/timeout", "value": 60 },
  { "op": "test",    "path": "/tags/1",         "value": "beta" },
  { "op": "add",     "path": "/tags/2",         "value": "gamma" },
  { "op": "test",    "path": "/mcpServers/1/name", "value": "github" },
  { "op": "add",     "path": "/mcpServers/2",   "value": { "name": "ctxloom", "command": "ctxloom", "args": ["mcp"] } }
]
```

Note what the `test` ops are: **every context line and every removal becomes an assertion.**
That is where loud staleness lives in the lowered form, and it is why an Hew patch is
down-compilable to 6902 without losing its safety property.

### 5.1 Partial elements in a match

In the `/mcpServers` hunk above, the context element is written as `- name: github` — one
field, not the whole element. Object-valued context is matched as a **subset** by default
(§6.1): the listed fields must be present and equal, unlisted fields are neither required nor
forbidden. This is what lets a keyed-array context line be one line long instead of eight,
and it is the single largest readability win Hew has over RFC 7386 Merge Patch, which must
restate every untouched element of a touched array.

---

## 6. Context, position, and exhaustiveness

### 6.1 Matching is partial by default

| Node kind in the before-image | Match rule |
|---|---|
| Mapping / object | **Subset.** Every listed key must exist with an equal value. Unlisted keys are ignored. |
| Sequence / array | **Ordered subsequence.** Listed elements must appear in the target in the listed relative order. Unlisted elements may appear before, between, or after. |
| Scalar | **Exact**, after format-native decoding (`8080` == `8080`, `8080` != `"8080"`). |
| Comment (JSONC/YAML/TOML/HCL) | **Exact text**, after stripping the comment marker and one leading space. |
| Markdown block | **Exact source bytes** of the block, after trailing-whitespace normalization. |

`? exhaustive` (§7.1) upgrades subset to exact-set and subsequence to exact-sequence for the
container it governs. Use it when "nothing else was added here" is part of what you are
asserting — the case Merge Patch handles by accident and expensively, and Hew handles on
purpose and cheaply.

### 6.2 Insertion position comes from surrounding context

An added node is inserted **relative to the context lines around it in the hunk body**:

- If a context (or `-`) sibling precedes the `+` run, insert immediately after that sibling.
- Otherwise, if a context sibling follows the `+` run, insert immediately before it.
- Otherwise (no sibling context at all in this container), insert at the **end** of the
  container.

This is unified diff's rule, and it is the reason context lines earn their keystrokes:

```
@@ /server @@
  host: localhost
+ tls: true
  port: 8080
```

places `tls` between `host` and `port` — something no op-list notation can express without a
numeric index, and no merge-patch notation can express at all.

For mappings, position is a *formatting* property, not a semantic one; a backend whose format
has no stable key order (none of ours — all six preserve source order) may ignore it. For
sequences it is semantic.

### 6.3 Equality and formatting

Matching compares **values**, not bytes: `port: 8080`, `port:  8080`, and `"port": 8080` all
match the number 8080. Quoting style, flow-vs-block YAML style, and whitespace inside a line
are not matched.

Conversely, **application preserves the target's bytes everywhere it did not have to change
them.** An unchanged sibling keeps its exact original bytes, comments, blank lines, and
quoting. A changed scalar keeps the *container's* formatting and adopts the patch's rendering
for the new value. This is the byte-preservation contract every backend must meet, and the
corpus enforces it by byte-exact comparison of expected output (§13.4).

### 6.4 The tolerance model — what drift a patch survives

Normative, and the section to read if you are coming from `patch(1)`.

**`patch(1)`'s fuzz is the wrong benchmark.** Fuzz relocates a hunk *textually*: it searches
nearby line offsets and, with `--fuzz`, drops context lines until something matches. It
tolerates the drift it can find by scanning, and it silently mis-applies when the scan lands
somewhere plausible but wrong. Hew has no fuzz (§1) — and yet it tolerates *more* real-world
drift than fuzz does, because it does not match by position at all.

> **Hew addresses nodes by path. Reordering is invisible to a path.** A user who reorganized
> their `settings.json` has not broken any Hew patch against it. Under `patch(1)` the same
> reorganization breaks every hunk.

#### 6.4.1 Map and object asserts are order-insensitive

A mirror context line for a sibling key compiles to a `test` on that key's **presence and
value** (§9.0) — never on its position. This is normative for every mapping-shaped node in
all six formats: JSON/JSONC objects, YAML mappings, TOML tables, HCL bodies, and the child
sets of Markdown sections.

```
@@ /server @@
  host: localhost
- timeout: 30
+ timeout: 60
```

applies unchanged whether the target reads `host, port, timeout`, `timeout, port, host`, or
has gained six new keys in between. The one place order re-enters is **insertion position**
(§6.2), and that is an instruction about where to *write*, not a condition on what must be
true — a `+` whose context has moved is placed relative to where that context now is.

#### 6.4.2 Keyed arrays are addressed by identity

`/mcpServers/name=github` (§4.2) and `/tags/=gamma` survive reordering of the sequence, because
neither address mentions a position. This is why §9.4-R4 requires the differ to *prefer*
identity addressing, and it is the strategic-merge lesson made general: SMP's merge-by-key
behavior was the right idea trapped behind a schema registry, and Hew's answer is to put the
key in the address where the patch itself declares it.

**"A usable key"**, normatively, for both the differ and a human author:

1. The field is present on **every** element of the sequence, and
2. its value is a **scalar** (not a map or sequence), and
3. the values are **unique** across the sequence.

If no field satisfies all three, the sequence has no identity and Hew addresses it positionally
— honestly, and with the consequences in §6.4.3.

#### 6.4.3 The two order-sensitive spots, named

| Spot | Why | Mitigation |
|---|---|---|
| **Plain positional arrays** (`/tags/0`) | The elements have no identity; position is genuinely all there is. | Prefer the `=value` address (§4.2) for scalar lists — `/tags/=gamma` is identity addressing for a list that has no key field. Only fall back to `/tags/0` when the list has duplicates. |
| **HCL repeated-label blocks** (`! match ord=1`) | Two `provider "aws"` blocks are indistinguishable by path; an inserted earlier sibling shifts every later ordinal. | §7.2, tightened below. |

**Normative mitigation for ordinals** (strengthening §7.2, and adopting ytt's
`overlay.subset()` idiom): an ordinal is the **last resort**, not the first tool.

1. If the block has a distinguishing child attribute, **address it by that attribute
   instead** of by ordinal — an Hew path may descend into the block and assert
   (`? expect ./alias = "east"`).
2. **An ordinal-addressed transform MUST carry at least one distinguishing assert** — a
   context line or a `? expect` on a child that differs between the same-label siblings. A
   patch that carries `! match ord=` with no distinguishing assert is `HEW001`. The reason is
   the whole tolerance model in one sentence: *if the ordinal shifts, the patch must fail
   loudly rather than silently edit the wrong block.*
3. If the siblings are genuinely indistinguishable in every child, the ordinal stands alone —
   and that is the one construct in Hew that can silently patch the wrong node. The corpus
   pins the diagnostic, and [O25](#open-questions-for-ratification) asks whether such a patch
   should be refused outright.

#### 6.4.4 Two different things called "move"

Spec language keeps these apart, because conflating them is how a reader concludes Hew is
either too strict or too loose:

| Term | Meaning | Hew's stance |
|---|---|---|
| **Target drift** | The *user* reordered or relocated nodes in the file since the patch was written. | **Tolerated, and invisible** — node addressing does not see it (§6.4.1, §6.4.2). |
| **Move as an operation** | The *patch* relocates a node from one path to another (OP-21). | Not expressible in the mirror grammar; written as a transform list (§9.6, Appendix C.1). |

"This patch survives a move" means the first. "This patch performs a move" means the second.

#### 6.4.5 The tolerance table

Normative. Each row is corpus-case material (§13), one per format family.

| Target changed how | Hew's response | Why |
|---|---|---|
| Keys reordered within a map | **Survives** | Path addressing; no positional assert (§6.4.1) |
| Keyed-array elements reordered | **Survives** | Identity addressing (§6.4.2) |
| Whitespace, indentation, line wrapping changed | **Survives** | Matching compares values, not bytes (§6.3) |
| Quoting style changed (`'x'` → `"x"`, bare → quoted) | **Survives** | Same |
| YAML block ↔ flow style changed | **Survives** | Same |
| Comments added, edited, or removed elsewhere | **Survives** | Comments are nodes; unasserted nodes are unconstrained (§6.1) |
| Unrelated keys added anywhere | **Survives** | Subset matching (§6.1) — unless `? exhaustive` was asserted, which is the point of asserting it |
| Unrelated keys removed elsewhere | **Survives** | Same |
| An asserted node's **value** changed | **`HEW010` stale-target** | The assert is the contract |
| An addressed node is **missing** | **`HEW013` no-match** | Never a silent no-op (§1) |
| A key-match or heading now matches **two** nodes | **`HEW012` ambiguous-match** | Drift Hew will not resolve by guessing |
| The patch was **already applied** | **`HEW010`**, unless `! idempotent` (§7.5) | [O3](#open-questions-for-ratification) |
| Plain-array elements reordered | **`HEW010`** if a positional address was used | The one honest failure; use `=value` addressing |
| An earlier same-label HCL block was inserted | **`HEW010`/`HEW011`** via the required distinguishing assert (§6.4.3) | Loud, not silent |

---

## 7. Annotations

Annotation lines carry margin `?` (assertion — can fail) or `!` (directive — changes how
application works, cannot itself fail). They are the second half of the notation: the mirror
body says *what the shape is*, and the annotations say *what must be true about it* and *which
of several identical-looking nodes is meant*.

**Attachment.** An annotation's text is written at the indentation of the body lines it sits
among. Annotations fall into three attachment classes:

| Class | Directives | Attaches to |
|---|---|---|
| **Free-standing** | `? expect`, `? absent`, `? count`, `? kind` | Nothing — they carry their own path. |
| **Container-scoped** | `? exhaustive`, `! surface` | The container whose children are at this indentation (the anchor, for top-level body lines). |
| **Line-scoped** | `! match`, `! anchor`, `! optional`, `! idempotent`, `! upsert`, `! default` | The immediately following body line; or the **anchor** if the annotation is the first body line of the hunk. |

A line-scoped annotation not followed by a body line, and not first in the hunk, is `HEW001`.

### 7.1 `?` assertions

| Directive | Meaning | Failure |
|---|---|---|
| `? expect <hewpath> = <value>` | The node exists and equals the value. Does not modify. | `HEW011` |
| `? absent <hewpath>` | The node does not exist. | `HEW011` |
| `? exhaustive` | The listed children are the *complete* child set of the governed container. | `HEW011` |
| `? count <hewpath> = <n>` | The container has exactly `n` children. | `HEW011` |
| `? kind <hewpath> = <k>` | The node's kind is `k` ∈ `map\|seq\|scalar\|block\|section`. | `HEW011` |

```
@@ /server @@
? expect /version = 1.2.0
? absent /server/tls
- timeout: 30
+ timeout: 60
```

Container-scoped `? exhaustive`, shown at both levels so the attachment rule is concrete:

```
@@ / @@
? exhaustive
  server:
? exhaustive
    port: 8080
```

The first asserts `server` is the document's only top-level key; the second asserts `port` is
`server`'s only key.

### 7.2 `! match` — ordinal selection among identical siblings

This is the notation's answer to HCL's repeated-label case, promoted from the survey's
design-A rendering into normative grammar. Target:

```hcl
provider "aws" {
  region  = "us-west-1"
  profile = "default"
}

provider "aws" {
  alias  = "east"
  region = "us-east-1"
}
```

`/provider/"aws"` names two nodes, so it is `HEW012` on its own. The ordinal is written as an
annotation, in place, beside the block it selects:

```
--- main.tf format=hcl

@@ / @@
! match label=["aws"] ord=0
  provider "aws" {
-   region  = "us-west-1"
+   region  = "us-west-2"
    profile = "default"
  }
! match label=["aws"] ord=1
  provider "aws" {
    alias  = "east"
    region = "us-east-1"
+   profile = "ctxloom"
  }
```

Or, hunk-anchored, using the first-body-line form:

```
@@ /provider/"aws" @@
! match ord=1
  alias = "east"
+ profile = "ctxloom"
```

Grammar: `! match [label=[<label>, …]] ord=<n>`.

- `ord` is **required**, 0-based, and counts same-`(type, labels)` siblings in source order.
- `label=[…]` is **optional and redundant by design**: when present it is checked against the
  selected block's actual labels and a mismatch is `HEW011`. It exists because a bare `ord=1`
  is unreadable in a long file, and because a redundant assertion is the cheapest possible
  guard on a fragile selector.
- An ordinal is only legal where the path is genuinely ambiguous. `! match` on a path that
  resolves to exactly one node is `HEW001` — an unnecessary ordinal is a latent misapply
  waiting for the file to grow a sibling, and Hew refuses it rather than tolerating it.
- **Every hunk using `! match ord=` MUST carry at least one distinguishing assert** — a
  context line or a `? expect` on a child that differs between the same-label siblings. A
  hunk with `! match ord=` and no distinguishing assert is `HEW001`. The `alias = "east"`
  context line above is what makes the second example safe: if a third `provider "aws"` block
  is inserted earlier, the ordinal still selects index 1 but the context no longer matches,
  and the apply fails by name instead of editing the wrong provider. See §6.4.3 for the
  tolerance rationale, and prefer addressing by the distinguishing attribute outright when one
  exists.

### 7.3 `! anchor` and `! surface`

Format-specific directives, specified with their wrinkles: `! anchor rewrite|fork` in §8.3
(YAML aliases), `! surface table|dotted` in §8.4 (TOML duality).

### 7.4 Assert-only hunks

A hunk whose body contains context and `?` lines but **no `+` or `-` lines** is a legal
assert-only hunk. It changes nothing, contributes only `test` ops (§9), and fails loudly if
its assertions do not hold. This is how Hew expresses "check that the world is what I think it
is" — a precondition patch, a drift check in CI, a guard shipped alongside a migration:

```
hew: 1

--- .claude/settings.json format=jsonc

@@ /permissions @@
? exhaustive
  "deny": ["Bash(rm -rf *)"]
  "allow": ["Bash(git *)"]

@@ / @@
? absent /env/ANTHROPIC_API_KEY
? kind /permissions = map
```

Note that the mirror body is still a mirror — the shape is what supplies the context — so an
assert-only patch reads exactly like the patches around it. It has no second grammar.

### 7.5 `! idempotent`

Attached to a hunk (as the first body line) or to a single `+`/`-` line. It changes the
failure rule to:

> If the before-image does not match **but the after-image does**, the hunk is satisfied and
> contributes zero ops.

It does not weaken anything else: a *partially* applied state — where neither image matches —
is still `HEW010 stale-target`, which is exactly the dangerous case a naive "just merge it"
tool papers over.

This is the directive ctxloom's own managed-file writers (M1–M8 in the config-patching
review) will reach for immediately, since every one of them is idempotent by construction.
It is specified normatively here and flagged for ratification as
[O3](#open-questions-for-ratification) because "re-running a patch is an error" is a defensible
alternative default.

### 7.6 `! optional`

Attached to a `-` line: the removal is satisfied whether or not the node exists. Discouraged —
it disables loud staleness for that line, which is the property the whole format exists to
provide. A conformant linter should warn on every use, and the Hew comment above it should say
why. (OP-06.)

### 7.7 `! upsert` and `! default` — the two add-semantics variants

Attached to a `+` line. They exist because the operations sweep (§11) found three distinct
"add" semantics in the surveyed systems, and a single `+` margin can only mean one of them:

| Directive | Node already present → | Catalog |
|---|---|---|
| *(none)* | `HEW014 already-exists` — the strict default | OP-02 |
| `! upsert` | replaced, whatever it held | OP-03 |
| `! default` | **left alone**, zero ops, exit 0 | OP-04 |

```
@@ /server @@
# seed a timeout only if the user has not chosen one
! default
+ timeout: 30
```

`! upsert` is the one mapping write that asserts nothing about the prior state, so it cannot
detect drift; use it only where ctxloom owns the key outright (`agent.InstallMCPServerJSON`'s
exact case). `! default` is its opposite and is safe by construction.

---

## 8. Per-format binding

A format binding must answer exactly four questions. Everything else in this spec is
format-agnostic.

1. **Detection** — which files am I? (§8.0)
2. **Fragment parsing** — given body text and an anchor node kind, what tree does it denote?
3. **Node identity** — what are this format's node kinds, and what does "equal" mean?
4. **Byte-preserving application** — given an op list, how do I edit the source?

### 8.0 Detection

| Format | Extensions | Notes |
|---|---|---|
| `json` | `.json` | Also the default for `.json` files known to forbid comments (`package.json`). |
| `jsonc` | `.jsonc`, well-known names | `settings.json`, `tasks.json`, `launch.json`, `tsconfig.json`, `.mcp.json` are JSONC by convention despite the extension. The well-known-name list is data, not spec — [O4](#open-questions-for-ratification). |
| `yaml` | `.yaml`, `.yml` | |
| `toml` | `.toml` | |
| `hcl` | `.tf`, `.hcl`, `.tfvars`, `.nomad`, `.pkr.hcl` | `.tf.json` is `json`, not `hcl`. |
| `markdown` | `.md`, `.markdown` | |

Explicit `format=` on the target line always wins. Ambiguity without an explicit declaration
is `HEW021 unsupported-format` — Hew never sniffs content to guess.

### 8.1 JSON

Node kinds: object, array, string, number, boolean, null.

**Body text is written without the anchor's own delimiters** (§2.3) and is read by a
*tolerant* member-list reader:

- **Trailing and separating commas are optional and ignored.** `port: 8080` and
  `"port": 8080,` are the same body line. The backend emits correct separators.
- **Keys may be written bare** if they are valid JSON identifiers: `port: 8080` ≡
  `"port": 8080`. A key needing quotes must be quoted.
- Nested values on one line carry their own braces/brackets normally: `+ args: ["mcp"]`.

```
--- .mcp.json format=json

@@ /mcpServers @@
  "ctxloom": { "command": "ctxloom" }
+ "taskloom": {
+   "command": "taskloom",
+   "args": ["mcp"]
+ }
```

Byte preservation: JSON has no comments, but it has indentation, key order, and **numeric
literal form**. A backend MUST NOT round-trip numbers through float64 (the `MCPFileConfig`
lesson: foreign large integers must be re-emitted as their original bytes). Untouched members
keep their exact source bytes.

### 8.2 JSONC — comment anchoring

Everything from §8.1, plus comments as first-class nodes.

**The anchoring rule** (this is the wrinkle, made normative):

- A comment line (`//` or `/* */`) **immediately preceding** a member, with no blank line
  between, is that member's **leading comment** and moves/deletes with it.
- A comment on the **same line as** a member, after it, is that member's **trailing comment**
  and moves/deletes with it.
- A comment separated from the next member by a blank line, or standing at the end of a
  container, is a **free comment** bound to the *container* at its position. It is never
  moved or deleted by an operation on a sibling.

Consequences the corpus pins:

```
--- .claude/settings.json format=jsonc

@@ /permissions @@
  // ctxloom-managed — do not edit
- "deny": ["Bash(rm -rf *)"]
+ "deny": ["Bash(rm -rf *)", "Bash(curl *)"]
```

Removing a member with a leading comment removes both. To keep the comment, make it a context
line (as above) — the comment is context, the member changes, the comment survives. To remove
a member *and* keep its leading comment, promote the comment to free by adding a blank line
first, which is itself an edit.

### 8.3 YAML — anchors, aliases, merge keys

Node kinds: mapping, sequence, scalar, comment, anchor, alias, document.

Style is preserved: a block mapping stays a block mapping, a flow sequence stays flow. An
added node adopts the style of its siblings, or the patch's own rendering if it has none.

**The anchor/alias wrinkle, made normative.** One authored node can be referenced from many
tree locations. An edit whose path resolves *through or at* an alias site is:

- `HEW040 anchor-ambiguity` by default. Hew will not guess whether you meant to change every
  use site or just this one.
- `! anchor rewrite` — the edit is applied to the **anchor definition**. Every alias site
  observes it. The patch is asserting that shared-value semantics are intended.
- `! anchor fork` — the alias at this site is **materialized** into an independent concrete
  node carrying the anchor's current value, and the edit is applied to that copy. Other alias
  sites are unaffected.

```yaml
# target
defaults: &defaults
  timeout: 30
  retries: 3
service_a:
  <<: *defaults
  port: 8080
service_b:
  <<: *defaults
  port: 8081
```

```
@@ /service_a @@
! anchor fork
- timeout: 30
+ timeout: 60
```

yields `service_a` with its own explicit `timeout: 60` (the merge key stays, the key is
shadowed), while `service_b` still sees 30. With `! anchor rewrite`, `defaults.timeout`
becomes 60 and both services see it.

**Merge keys (`<<:`) specifically:** a key that a mapping only has *via* a merge is **not
present at that site**. Removing it with `-` is `HEW013 no-match`, not a silent success — you
cannot delete an inherited key, only shadow it. The error message names the anchor the key
came from.

### 8.4 TOML — dotted-key / table-header duality

`a.b.c = 1`, `[a.b]` + `c = 1`, and `[a]` + `b.c = 1` denote the same tree with three
different surface forms.

**Normative rules:**

1. An edit to a path that **already exists** is applied at whichever surface form the target
   actually uses. Hew never adds a second surface for an existing path.
2. If a path is defined at **two** surfaces in the same document (which TOML forbids but real
   files occasionally contain), that is `HEW041 surface-ambiguity`.
3. A **creation** adopts the surface of the nearest existing ancestor: creating `/a/b/c` where
   `[a.b]` exists appends `c = …` to that table; where only `a.b = {}` exists (inline table)
   it edits the inline table; where nothing exists it creates a `[a.b]` table header at the
   end of the document.
4. `! surface table` / `! surface dotted` overrides rule 3 for a creation. It is **not**
   permitted to rewrite an existing path's surface — surface migration is not a patch
   operation in v0 ([O10](#open-questions-for-ratification)).

```
--- ~/.codex/config.toml format=toml

@@ /mcp_servers @@
? absent /mcp_servers/taskloom
! surface table
+ [mcp_servers.taskloom]
+ command = "taskloom"
+ args = ["mcp"]
```

Note the body writes the `[mcp_servers.taskloom]` header even though §2.3 says the anchor's
own delimiters are omitted — because here the header belongs to the *added child*, not to the
anchor `/mcp_servers`. Array-of-tables (`[[x]]`) children are addressed as sequence elements
and support key-match (`/tool/plugins/name=foo`).

### 8.5 HCL — attributes, blocks, labels

Node kinds: body, attribute, block, expression, comment.

- An **attribute** body line contains a top-level `=`: `region = "us-west-1"`.
- A **block** body line ends in `{` and its body is written indented, closing with `}`:

```
--- main.tf format=hcl

@@ /terraform @@
  required_version = ">= 1.6"
+ required_providers {
+   aws = {
+     source  = "hashicorp/aws"
+     version = "~> 5.0"
+   }
+ }
```

- **Expressions are compared as source text**, normalized for whitespace only. Hew does not
  evaluate HCL: `"${var.x}"` and `var.x` are different values even where HCL would agree.
  This keeps the format binding honest about what it can prove.
- Alignment: `hclwrite` re-aligns `=` within a body. Hew adopts the backend's alignment on any
  body it modified, and leaves untouched bodies byte-identical. The corpus pins this.
- **Repeated `(type, labels)` tuples**: `HEW012 ambiguous-match` unless a `! match ord=`
  annotation (§7.2) selects one. This is the case the notation was checked against in the
  survey's HCL section, and the check is now the grammar.

### 8.6 Markdown — sections and blocks

Markdown is the one format where Hew is not addressing a keyed tree, and it gets a dialect
rather than a variant.

- The document is a tree of **sections** (by ATX heading level) containing **blocks**
  (paragraph, fenced code, list, table, blockquote, HTML). Setext headings are normalized to
  their ATX level for addressing but preserved byte-for-byte if untouched.
- **A body line's margin applies to its whole line, but matching and ops are per block.**
  Every line of a multi-line block carries the same margin. A block whose lines carry mixed
  margins is `HEW001`. To change one line of a paragraph, remove the paragraph and add the
  replacement — Markdown has no sub-block addressing in v0.
- **Blank lines are structural, not written.** The patch does not carry blank lines between
  blocks; the backend emits exactly one blank line between blocks it inserts and preserves the
  target's existing separation elsewhere.

```
--- README.md format=markdown

@@ /# ctxloom/## Install @@
  Install with:
- ```sh
- go install github.com/ctxloom/ctxloom@v0.6.0
- ```
+ ```sh
+ go install github.com/ctxloom/ctxloom@v0.7.0
+ ```
```

**Managed-marker blocks.** ctxloom's own in-file ownership markers (M2:
`<!-- ctxloom:context:begin (managed — do not edit between markers) -->` …
`<!-- ctxloom:context:end -->`) are addressable as a single node:

```
@@ /@ctxloom:context @@
! idempotent
- (the previous managed body, as context/removal lines)
+ (the new managed body)
```

The `@<name>` segment addresses the region between an HTML comment pair whose begin marker
matches `<!-- <name>:begin` and end marker `<!-- <name>:end`. The markers themselves are never
part of the node's value, so a patch replacing the region cannot destroy them. An unclosed
begin marker is `HEW002 target-parse-error` — refuse, do not repair. This is the direct Hew
expression of `agent.WriteManagedContext`, and it is why Markdown is in the implement tier at
all.

### 8.7 Markdown: Hew dialect vs unified diff — evaluation plan

**Markdown's place in the implement tier is not settled** (human, 2026-08-14). It may be
better served by plain `patch(1)`. The dialect above stays in the draft as designed; this
section is the plan for deciding, and it runs **after** the spec is complete, before any
Markdown backend is built.

#### 8.7.1 Why this is a real question and not a formality

The tolerance model (§6.4) is where Hew earns its keep, and **its central asymmetry runs
against Markdown**:

| | Keyed trees (JSON/JSONC/YAML/TOML/HCL) | Prose (Markdown) |
|---|---|---|
| Is sibling order meaningful? | **No.** Reordering `settings.json` changes nothing. | **Yes.** Paragraph order *is* the document. |
| So reorder-blindness is… | the single largest win over `patch(1)` | **worth approximately nothing** |
| Is there a stable identity per node? | Yes — keys, `name=` fields, labels | **No.** A paragraph's only identity is its text and its position — which is exactly what a unified-diff hunk already matches on |
| Is `patch(1)` at home? | No — it cannot see that two keys are siblings | **Yes.** Line-oriented prose is its native material |

Put bluntly: Hew's Markdown dialect addresses blocks by *kind-scoped ordinal within a section*
(§4.5), which is a positional address dressed up as a structural one. `patch(1)` addresses
lines by position with fuzz. Both are positional. One of them already exists, is universally
installed, has thirty years of tooling, and is what every reviewer already reads.

#### 8.7.2 What the evaluation must do

Not a discussion — an **analysis and simulation**, in the style of the notation survey that
produced this spec: render the same realistic scenarios in both notations, side by side, and
score where each fails.

**Scenarios (all drawn from ctxloom's real managed-Markdown surfaces, not invented):**

1. **Managed-block replacement** — `CLAUDE.md` / `AGENTS.md` with a
   `<!-- ctxloom:context:begin -->` … `end` region rewritten wholesale (M2's actual job,
   OP-45). Both notations attempt it.
2. **User edits surrounding prose** — the same replacement, but the user has rewritten three
   paragraphs above and added a section below since the patch was authored.
3. **Block moved within the file** — the user relocated the managed block (or a section) to a
   different position in the document. This is the scenario the tolerance model claims as Hew's
   win; the evaluation must measure whether it *is* one for prose.
4. **Concurrent prose edits adjacent to the block** — a paragraph immediately above the
   managed region changed in the same window. `patch(1)`'s fuzz behavior here is the thing to
   observe: does it apply, mis-apply, or reject?
5. **Section addressed by heading, heading text edited** — Hew's `/## Install` address breaks;
   `patch(1)`'s context breaks too. Which fails more usefully?
6. **A section gains a duplicate heading** — Hew raises `HEW012`; `patch(1)` picks by position.

**Scoring, per scenario, per notation:**

| Criterion | Question |
|---|---|
| Applies correctly? | Does the intended edit land? |
| Fails loudly when it should? | Or does it apply somewhere plausible-but-wrong (`patch(1)`'s fuzz failure mode) or silently no-op? |
| Survives the drift? | Per §6.4's table, adapted to prose |
| Reviewable? | Is the patch legible in a PR — the criterion that started this whole workstream |
| Authoring cost | Hand-writing the patch; and, for the differ, generation cost |
| Implementation cost | A Markdown block model with byte preservation is the most expensive of the six backends — there is no `hclwrite` equivalent to lean on |

#### 8.7.3 Possible outcomes

1. **Keep the dialect** — the evaluation finds managed-block addressing (OP-45) and
   heading-path addressing genuinely beat unified diff for ctxloom's surfaces.
2. **Drop Markdown from the implement tier** — it moves to §12 (documented-only) with a
   normative "use `patch(1)`" note, and `hew` gains no Markdown backend.
3. **Narrow to the managed-marker case only** — Hew implements *only* `/@name` marker regions
   (OP-45) and nothing else, which is the one Markdown operation with a real structural
   address and the one `patch(1)` handles worst. This is the outcome to beat: it is a small
   backend with a clear win, and it is what ctxloom actually needs.

#### 8.7.4 Severability

The dialect is already structurally severable and must stay that way while the question is
open:

- All Markdown corpus cases live in **`tests/hewcorpus/markdown/`** and nowhere else, so a
  drop is `git rm -r` on one directory plus §8.6 and §4.5.
- **No rule in §§1–7 or §9–§11 depends on Markdown.** The block and heading segments (§4.5)
  and the comment/section node kinds are the only cross-references, and each is guarded by a
  format check.
- Markdown is the only format whose `NodeKind` set includes `KindSection`; nothing else reads
  it.

Tracked as [O29](#open-questions-for-ratification).

---

## 9. The transform list

**Architecture ruling (human, 2026-08-14).** Hew is four components around one intermediate
representation. The IR is the **transform list**: an ordered list of node addresses, RFC
6902-modeled operations, and assertions. It is simultaneously the internal boundary, the
interop surface, and the thing the corpus pins.

```
        NOTATION SIDE                    IR                    FORMAT SIDE
                                 ┌─────────────────┐
   .hew text ──[ parser ]───────► │                 │ ──[ applier ]──► patched bytes
                                 │  transform list │       (per format)
   .hew text ◄──[ renderer ]───── │   addresses     │ ◄──[ differ ]──── (old, new) bytes
                                 │   ops (6902)    │       (per format)
                                 │   assertions    │
                                 └─────────────────┘
```

- **Parser** (`.hew` → IR). Owns the notation. **Never touches a target file, never opens
  anything, knows no format mechanics.** Its output is fully determined by the patch text.
- **Applier** (IR + target bytes → patched bytes). One implementation per format, each
  wrapping that format's byte-preserving editor library (`tailscale/hujson`,
  `pelletier/go-toml/v2` `unstable/edit`, `hashicorp/hcl/v2/hclwrite`, `yaml.v3` node surgery,
  a Markdown block editor). **Never sees a margin, a hunk, or an annotation.**
- **Differ** (old bytes, new bytes → IR). One implementation per format, parsing both sides
  with the *same* library its applier uses, computing a structural diff into the IR. P4 work;
  §9.4 specifies its requirements now because they constrain the IR's design.
- **Renderer** (IR → `.hew`). Notation side, format-agnostic like the parser. Writes the mirror
  grammar back out, generating context lines from the assertions in the IR.

Parser and renderer are notation-side inverses; differ and applier are format-side inverses.
The four-way closure is what makes the round-trip identity in §13.5 a meaningful conformance
test rather than a slogan.

**The IR has a canonical serialized form (§9.6), and that form is also an accepted input.**
It had to exist anyway — the corpus pins the parser by comparing against it, and it is the
RFC 6902 interop surface. Making it readable *in* costs nothing and resolves the
escape-hatch question permanently: an edit the mirror grammar cannot express (a move, a copy)
is written as a transform list directly. **This is not a second notation the standard
optimizes for humans.** It is machine-first, it is where a tool emits and a tool consumes, and
the `.hew` mirror grammar remains the only authoring surface designed to be read in a pull
request.

### 9.0 Context lines are not decoration — they compile into assertions

This is the load-bearing consequence of the pipeline, and it is why the parser can be
format-agnostic while the format still fails loudly on drift.

> **Every context line and every `-` line in the mirror body compiles into a `test`
> transform in the IR.** Nothing about "loud staleness" lives in the applier. The applier is
> a dumb executor of a transform list; the reason a drifted target fails is that the list it
> was handed *begins with assertions the parser derived from the shape the author wrote*.

A reviewer reading a hunk sees context lines and understands them as "the surroundings". The
machine reads the same lines and understands them as "these must hold". They are the same
lines. That equivalence is the whole design:

```
@@ /server @@
  port: 8080          →  { test,    /server/port,    8080 }
- timeout: 30         →  { test,    /server/timeout, 30 } then { replace, /server/timeout, 60 }
+ timeout: 60
```

Two practical consequences the corpus pins:

1. **Writing more context makes a patch stricter, not merely more readable.** `-U3` in
   unified diff buys reviewability; context radius in Hew buys reviewability *and* drift
   detection, from the same keystrokes.
2. **An applier that ignores `test` transforms is not merely lax — it is non-conformant**,
   and the corpus's `apply-error` cases catch it, because those cases fail at the IR level
   before any byte is touched.

### 9.1 Lowering algorithm (parser → transform list)

Purely textual. No target is read. For each hunk, in file order:

1. Take the anchor path verbatim, attaching any `! match ord=` as a **selector** on the last
   segment. Emit no transform for this step.
2. For each context and `-` line, in body order, emit a `test` transform at that node's Hew
   path with its before-image value. For a subset-matched object context line, emit one
   `test` per listed field, not one for the object.
3. For each `? ` assertion, emit the corresponding transform: `expect` → `test`; `absent` →
   `test` with `absent: true`; `exhaustive` → `test` with `exhaustive: true`; `count`/`kind` →
   `test` with the corresponding qualifier. (These qualifiers are Hew extensions to 6902's
   `test`; a strict-6902 consumer will reject them, which is correct — it cannot honor them.)
4. For each `-` line whose key does not also appear as a `+` line at the same position, emit
   `remove`.
5. For each `+` line whose key also appears as a `-` line at the same position, emit
   `replace`. Otherwise emit `add`, with the insertion position carried as a **relative
   placement** (`after: <sibling path>` / `before: <sibling path>` / `end`) derived from
   §6.2 — *not* as a numeric index, since the parser has no target to count against.
6. `!` directives emit no transform of their own. They ride the affected transform as
   qualifiers (`anchor: fork`, `surface: table`, `idempotent: true`, `optional: true`).

**The parser never emits `move` or `copy`.** The mirror grammar compiles to four of RFC
6902's six operations. The IR itself carries all six, and the missing two are reachable by
authoring a transform list directly (§9.6) — which is what Appendix C now points at instead
of a future spec revision.

### 9.2 Two forms of the transform list

The IR exists in two forms, and conflating them is the mistake this section exists to prevent.

| Form | Paths | Placement | Produced by | Consumed by |
|---|---|---|---|---|
| **Abstract** | Hew paths, key-match and selectors intact | relative (`after:`/`before:`/`end`) | parser (§9.1), differ (§9.4) | applier, renderer |
| **Resolved** | RFC 6901 pointers, indices concrete | numeric indices | `Lower(ir, doc)` against a specific target | interop consumers, `hew apply --ops`, corpus `expected-ops.json` |

Only the **abstract** form is the pipeline's IR. It is target-independent, which is exactly
what lets the parser never open a file and the renderer never need one.

The **resolved** form is a projection for interop and for corpus assertions: key-match
segments (`/mcpServers/name=github`) become indices (`/mcpServers/1`) against *this* target,
and relative placements become array indices. It is lossy in one direction — anchor/alias
directives, surface directives, ordinal selectors, comment nodes, and Markdown block
structure have no RFC 6902 representation at all and are consumed during resolution.

Therefore: **the resolved op list is a derived artifact, not a serialization of the patch.**
`hew apply --ops` prints it; `hew` cannot read it back, and v0 defines no way to author one.

### 9.3 What the applier is, and is not

An applier receives `(abstract transform list, target bytes)` and returns `(patched bytes)`.
It resolves paths against the document it just parsed, evaluates every `test` transform
before performing any mutation, and either produces a fully patched document or an error with
nothing written (§10.5).

An applier **must not**: see the `.hew` text, know what a margin is, apply a fuzz factor,
reorder transforms, skip a `test` it does not understand, or emit a partial document. An
applier that encounters a transform qualifier it does not implement must fail `HEW020`, never
ignore it — ignoring an `anchor: fork` qualifier silently produces exactly the wrong edit.

### 9.4 Diff generation requirements (P4 — specified now because the IR depends on them)

The differ is not implemented in v0. Its requirements are normative anyway, because a
transform list that a differ cannot produce is an IR that only half exists.

**Inputs are pure content.** The core differ signature takes two byte sources of the same
format. It has **zero git awareness** — no repository, no revision, no subprocess. Descriptor
resolution (`HEAD:config.yaml`) is a CLI-boundary concern (§9.5, Appendix A.8), and keeping it
out of the core is load-bearing: it is what keeps the library embeddable and the Rust port's
dependency list short.

**R1 — Determinism.** The same `(old, new, options)` triple MUST produce a byte-identical
`.hew` file, on every run, in every implementation. This is what makes a diff case pinnable in
the corpus at all. Concretely it requires: a specified sequence-diff algorithm (v0: Myers over
node equality, ties broken toward earlier deletion), key-order-preserving traversal for
mappings, deterministic anchor selection (R3), and deterministic value rendering (R5).

**R2 — Context radius, the `-U3` analog.** The renderer includes unchanged siblings around
each changed run. The unit is **siblings within the anchored container**, not lines.

- **Default: 1.** One unchanged sibling before and one after each changed run.
- `--context=N` sets it; `--context=0` emits margins only; `--context=all` emits every sibling
  of every touched container (which, combined with `? exhaustive`, is the strictest patch Hew
  can express).
- **Identity lines are exempt from the radius and always emitted.** The key field of a touched
  keyed-array element (`- name: github`) is not context — it is the address, rendered as a
  line. Suppressing it at `--context=0` would make the hunk unaddressable.
- Two changed runs whose context windows overlap or abut are coalesced into one hunk, exactly
  as unified diff coalesces hunks.

Because context lines compile into assertions (§9.0), the radius is a **strictness dial, not
a verbosity dial** — a fact the CLI's help text must state, since users will otherwise reach
for `--context=0` to make patches smaller and quietly disable drift detection.

**R3 — Anchor selection.** The anchor of a hunk is the deepest container that contains every
changed node in that hunk. Deterministic, and it produces the shallowest bodies.

**R4 — Address preference.** For sequences, the differ MUST prefer a key-match segment
(`/mcpServers/name=github`) over a positional index when the sequence has a usable identity
field, because a positional address drifts the moment the user reorders the list. A field is
usable when it is present on every element, scalar, and unique across the sequence. Candidate
fields are tried in order: `name`, `id`, `key`, then any single field satisfying the
condition. If more than one field qualifies and none is a candidate, the differ uses indices
and emits an Hew comment saying so. See [O18](#open-questions-for-ratification) — a hardcoded
candidate list in a standard is exactly the kind of thing that ages badly.

**R5 — Rendering values.** An added node is rendered from the *new* document's own source
bytes, re-indented to the hunk body, so that a diff-then-apply round trip preserves the
author's formatting rather than re-emitting canonical form.

**R6 — Inexpressible changes.** If the structural diff contains a change Hew v0 cannot express
(Appendix C), the differ fails with `HEW020` naming the change. It MUST NOT silently degrade —
a differ that emits delete-and-add for a detected move without saying so would make Appendix
C.1's honest limitation into a silent data-shape change. (Note the asymmetry with the *apply*
side, which does not detect moves at all: [O16](#open-questions-for-ratification).)

### 9.5 Source descriptors (CLI layer only)

The CLI resolves a **source descriptor** to bytes before calling the core differ:

| Descriptor | Meaning |
|---|---|
| `path/to/file` | Working-tree file. |
| `-` | Standard input. Legal at most once per invocation. |
| `REV:path` | A git anchor, following git's own `<tree-ish>:<path>` convention: `HEAD:config.yaml`, `main:config.yaml`, `abc1234:sub/dir/config.yaml`. |

Canonical invocation: `hew diff HEAD:config.yaml config.yaml` — "what have I changed since the
last commit, as a structured patch".

**Git anchors are resolved by invoking git plumbing as a subprocess** (`git cat-file blob
<rev>:<path>`, or `git show <rev>:<path>`), never by linking a git library. Rationale: the
resolver is a hundred lines in any language, the subprocess boundary is the same in Go and
Rust, and linking libgit2 or go-git into a patch tool to read one blob is a dependency the
core spent §9.4-R0 avoiding. If `git` is not on `PATH`, a descriptor containing `:` is a
usage error (`exit 2`), never a silent fallback to treating it as a filename.

A literal path containing `:` is disambiguated with `./` (`./weird:name.yaml`), which is
git's own rule.

### 9.6 Canonical serialization of the transform list — `.hewt`

Normative. The IR's serialized form is a **YAML document** (and therefore also readable as
JSON, since JSON is a YAML subset — one reader serves both). Extension `.hewt`, media type
`application/vnd.ctxloom.hew-transforms+yaml`. Name and extension flagged as
[O21](#open-questions-for-ratification).

```yaml
hew-transforms: 1
target: config.yaml
format: yaml
transforms:
  - op: test
    path: /server/timeout
    value: 30
  - op: replace
    path: /server/timeout
    value: 60
  - op: test
    path: /mcpServers/name=github/command
    value: npx
  - op: add
    path: /mcpServers
    after: /mcpServers/name=github
    value:
      name: ctxloom
      command: ctxloom
      args: [mcp]
```

**Document keys**

| Key | Required | Meaning |
|---|---|---|
| `hew-transforms` | yes | Version integer. `1` here. Must be the first key. |
| `target` | yes | Target path, same semantics as a `--- ` line (§2.2). |
| `format` | no | Same resolution order as §2.2. |
| `transforms` | yes | Ordered sequence. Empty is `HEW001` (an empty patch is refused, §10.2). |

A multi-target transform list is a multi-document YAML stream (`---`-separated), one document
per target, applied in order under §10.5's per-file atomicity.

**Transform record — the reduced core**

The IR is **ASM**: few primitives, stable, composable. Richness lives in the notation and in
the compiler (the parser), never in the IR. The derivation that produced this set is §11.10.

**Five operations.** RFC 6902's six, minus `move`, which is a lossless composition
(`copy` + `remove`) and therefore desugars.

| `op` | Meaning | Why it cannot be composed away |
|---|---|---|
| `test` | Assert. | The only assertion primitive; every context line compiles here (§9.0). |
| `add` | Create a node. | The only creation primitive. |
| `remove` | Delete a node. | The only deletion primitive. |
| `replace` | Swap a node's value **in place**. | `remove` + `add` provably loses the node's attached comments (§8.2) and re-derives its position. Not the same operation. |
| `copy` | Create a node from the value at another **path in the target**. | The only primitive that takes its value *by reference*. An `add` would have to restate the value, which requires reading the target and would break the IR's target-independence. |

**Fields.**

| Field | Applies to | Meaning |
|---|---|---|
| `op` | all | One of the five above. |
| `path` | all | Hew path (§4), abstract form. **All addressing richness lives here** — key-match, `=value`, labels, headings, blocks, comments, markers, `[n]` ordinal selectors. |
| `from` | `copy` | Source Hew path. |
| `value` | `add`, `replace`, `test` | The value, in YAML. |
| `before` / `after` | `add`, `copy` | Placement (§6.2). Mutually exclusive; absence means append at end. |
| `on_conflict` | `add` | `fail` (default, OP-02) \| `keep` (OP-04) \| `replace` (OP-03). |
| `absent` | `test` | Assert non-existence. |
| `count` | `test` | Assert child count. |
| `kind` | `test` | Assert node kind. |
| `anchor` | any | `rewrite` \| `fork` — YAML alias policy (§8.3). |
| `surface` | `add` | `table` \| `dotted` — TOML placement (§8.4). |
| `optional` | `remove`, `test` | Tolerate absence (§7.6). |
| `idempotent` | `add`, `remove`, `replace` | Tolerate an already-applied state (§7.5). |
| `line` | any | Provenance into the `.hew` file. **Emitted, ignored on input.** |

Exactly one of `value` / `absent` / `count` / `kind` on a `test`. `on_conflict` is the only
conditional on `add`; `optional` and `idempotent` are the two tolerance flags, and there are
no others.

**What is NOT in the record, and where it went.** These were in earlier drafts and were
removed by the reduction; each is now produced by the parser as a composition:

| Removed | Desugars to |
|---|---|
| `exhaustive` | `test`+`count` on the container, plus one `test`+`value` per listed child (OP-26). |
| `comment: {leading, trailing}` | A separate `add` of a comment node at a comment address (§4.5b), placed with `before`/`after` (OP-31). |
| `ord` | A `[n]` selector on the path's last segment — addressing, not operation (OP-37). |
| `labels` | Nothing. The labels are already the path's label segments; the parser checks the redundant `label=[…]` spelling and drops it. |
| `move` | `copy` + `remove` (OP-21). |

An unknown `op` or an unknown field is `HEW001`. Fields whose combination is meaningless
(`value` with `absent`, `from` on `add`) are `HEW001`.

**This table is not an independent design.** It is the union of the **IR record** rows of the
operations catalog (§11), rendered as data — the ordering ruling for this workstream is that
the surveyed catalog fixes the vocabulary and the IR schema falls out of it, never the
reverse. Concretely: `on_conflict` exists because the sweep found ytt's `missing_ok`,
jsonnet's absent-field merge and ctxloom's M5 install all needing add-semantics variants
(OP-02/03/04); `comment` exists because OP-30/OP-31 found a capability *no surveyed patch
format has* and ctxloom's own managed writers require. **A future catalog entry that needs a
new qualifier extends this schema by construction**, and a qualifier with no catalog entry is
a spec bug.

**Move and copy.** These are the operations the mirror grammar cannot express, and this is
where they live. A move is written as its two core records — which is also exactly what the
`.hew` mirror grammar could never have expressed as one gesture:

```yaml
hew-transforms: 1
target: config.yaml
format: yaml
transforms:
  - op: test
    path: /server/host
    value: localhost
  - op: copy
    from: /server/host
    path: /network/host
  - op: remove
    path: /server/host
```

An applier MUST implement all five core operations, and `copy` MUST preserve the source
subtree's bytes and attached comments where its format's editor library allows. That
requirement is what makes the copy-then-remove pair a *move* rather than a transcription, and
it is why reaching for the IR form beats writing delete-and-add in the mirror grammar.

A reader who prefers the word may write `op: move` with `from:`/`path:`: it is accepted on
input as sugar and is normalized to the two core records on the way in. It never appears in an
emitted transform list, and the corpus pins that.

**What the serialized IR is not.** It is not a review artifact. A pull request containing a
`.hewt` file where a `.hew` file would do is a review-quality regression, and a project may
lint for it. It is not a *lossless* record of a `.hew` file either: Hew comments, hunk
boundaries, and the author's chosen anchors are notation-side and do not survive the round
trip (§13.5 pins `render → parse == identity on the IR`, not `parse → render == identity on
the text`).

---

## 10. Error taxonomy

Every failure is one of these codes. Every error message carries: the code, the Hew path in the
target, the patch file line number, and — where applicable — the expected and actual values.

| Code | Name | Raised when |
|---|---|---|
| `HEW001` | `parse-error` | The `.hew` file is malformed: bad margin, bad path, unknown directive, mixed margins in a Markdown block, `! match` on an unambiguous path, an unattached line-scoped annotation. |
| `HEW002` | `target-parse-error` | The target file will not parse in its declared format, or an unclosed managed marker, or an unknown `hew:` version. **Nothing is written.** (`agent.RefuseCorrupt`'s stance, as a spec rule.) |
| `HEW003` | `target-path-error` | The target path escapes the apply root, or is not a regular file. |
| `HEW010` | `stale-target` | A context or `-` line's node is absent or unequal. The characteristic drift error. |
| `HEW011` | `assertion-failed` | A `? expect` / `? absent` / `? exhaustive` / `? count` / `? kind`, or a `! match label=` cross-check, did not hold. |
| `HEW012` | `ambiguous-match` | A key-match, HCL label tuple, or Markdown heading selected more than one node and no ordinal annotation resolved it. |
| `HEW013` | `no-match` | A required node does not exist: a `-` line's node, an anchor without `?`, an `ord=` beyond the sibling count, a merge-key-inherited key. |
| `HEW014` | `already-exists` | A `+` line's node already exists and the hunk is not `! idempotent`. |
| `HEW020` | `inexpressible` | The requested edit cannot be represented in Hew v0 or in the target format — a node move or copy (Appendix C), a comment node in JSON, a sub-block Markdown edit, a TOML surface migration. The message names Appendix C's condition for a spec revision. |
| `HEW021` | `unsupported-format` | No binding for the declared or inferred format. |
| `HEW030` | `conflict` | Two hunks in one apply touch overlapping nodes with incompatible ops. |
| `HEW040` | `anchor-ambiguity` | A YAML path resolves through or at an alias with no `! anchor` directive. |
| `HEW041` | `surface-ambiguity` | A TOML path is defined at two surfaces. |

### 10.1 What is deliberately *not* an error

- Extra unlisted siblings (§6.1) — that is what `? exhaustive` is for.
- A hunk that produces zero ops because the after-image equals the before-image. This is a
  legal no-op hunk (an assert-only hunk, §7.4); the file is unchanged and the exit code is 0.

### 10.2 What is deliberately an error even though other tools tolerate it

- Line-offset drift. There is no fuzz.
- An already-applied patch (`HEW010`), absent `! idempotent`.
- An empty patch file, or a file section with no hunks: `HEW001`. (`cli.runConfigWrite`'s
  refuse-empty-patch rule, promoted to the format.)
- A hunk that matched nothing. SmPL's silent context-mode no-op is the single behavior this
  format was designed to not have.

### 10.3 Message shape

```
hew: config.yaml:/server/timeout: HEW010 stale-target
  patch.hew:9: expected 30
  config.yaml:6:  found 45
```

Human-readable diagnostics go to **stderr**; `--format-out json` emits one JSON object per
error on stdout. (ctxloom's diagnostic channel split, applied to the CLI.)

### 10.4 First error wins

Application stops at the first error. There is no "collect all failures" mode in v0
([O11](#open-questions-for-ratification)).

### 10.5 Atomicity

Per target file, all-or-nothing. A file is written only if every hunk against it matched and
applied. There is no `.rej` file, no partial output, no backup file — the write goes through
an atomic temp-and-rename, and a failed apply leaves the target byte-identical. (This is
`iox.WriteFileAtomicFs`'s contract and the deliberate no-backup ruling, restated as a format
property so the Rust port inherits it.)

Across *multiple* target files in one patch, v0 is **not** transactional: files are applied in
file-section order and a later failure does not roll back an earlier file. The CLI prints
which files were written. [O12](#open-questions-for-ratification) asks whether it should be.

---

## 11. Operations catalog

**This section is derived by survey, not invented.** Its input is the operation vocabulary of
every system in `patch-notation-survey.md` — RFC 6902, RFC 7386, Kubernetes strategic merge,
ytt overlays, go-patch/yaml-patch, jd, kustomize, RFC 5261 XML Patch, spruce, CUE, jsonnet,
Coccinelle — plus the eight mechanisms ctxloom actually runs today (M1–M8 in
`config-patching-review.md`), plus the format-specific necessities each of the six implement
formats imposes.

**The completeness contract:** for any operation any of those systems can perform, a reader
can find it here and determine whether Hew v0 does it, how it is written, or why it does not.
Deferral is a legitimate answer. Omission is a spec bug.

### 11.0 How to read this catalog

**Read this section before §9.6.** The drafting order is normative for the workstream: the
survey fixes the operation vocabulary here, the catalog's **IR record** rows collectively
*define* the transform-list schema, and §9.6's canonical serialization is this catalog
rendered as data. The IR was not designed and then documented; it was derived. A future
addition to this catalog is an IR extension by construction.

The IR has **six operations** (`add`, `remove`, `replace`, `move`, `copy`, `test`) — RFC
6902's set, unchanged, because the survey found it to be the emergent cross-tool consensus.
Everything below is a *user-level* operation, expressed as one of those six plus path features
(§4) and qualifiers (§9.6). There is no seventh op and v0 adds none: the sweep produced 52
distinct user-level operations and every one of them landed on the six, which is itself the
strongest available evidence that 6902's vocabulary is the right skeleton.

Each entry states:

- **Status** — `v0` (normative and corpus-covered), `deferred` (named, not in v0, with the
  condition for adding it), `rejected` (deliberately never, with the reason).
- **Disp** — the disposition against the reduced core (§11.10):
  - `CORE` — reaches the IR as one core op plus addressing and core qualifiers.
  - `SUGAR` — a notation spelling that the **parser desugars** into a composition of core
    records. **The IR never carries it.** Richness in the compiler, not the ASM.
  - `OUT` — not in the normative set: rejected or deferred, with the reason stated.
- **Sources** — where in the survey the operation was found.
- **Absent/empty behavior** — the silent-no-op discipline, per operation: what happens when
  the target node is missing, or the container is empty. Every `v0` entry answers this.
- **Mirror** — the `.hew` rendering, or `IR-only` when the mirror grammar cannot express it.
- **IR** — the transform record.
- **Formats** — applicability across the six implement formats.
- **Errors** — the named failures.
- **Corpus** — the case(s) that pin it.

Format column key: `✓` supported · `—` not applicable to this format's data model ·
`✗` explicitly unsupported (raises `HEW020`).

### 11.1 Source sweep — where every surveyed verb landed

| Surveyed system | Its vocabulary | Landed as |
|---|---|---|
| **RFC 6902** | `add`, `remove`, `replace`, `move`, `copy`, `test` | The IR's six ops verbatim. OP-01–OP-06, OP-21, OP-22, OP-24. |
| **RFC 6901** | `-` append token, `~0`/`~1` escapes | §4.1, OP-11. |
| **RFC 7386 merge patch** | implicit set; `null` = delete; whole-array replace | OP-01, OP-05, OP-08. `null`-as-delete **rejected** (OP-10). |
| **K8s strategic merge** | `$patch: delete`, `$patch: replace`, `$patch: merge`, merge-key list semantics, `$setElementOrder`, `$deleteFromPrimitiveList` | OP-05, OP-08, OP-09 (rejected), OP-16, OP-19 (deferred), OP-15. |
| **ytt overlay** | `@overlay/match` (`by=`, `expects=`, `missing_ok=`), `@overlay/remove`, `@overlay/replace`, `@overlay/insert before=/after=`, `@overlay/append`, `@overlay/assert`, `@overlay/replace via=λ` | §4.2 + §7.2 (`by=`/`ord=`), OP-05, OP-01, OP-13, OP-11, OP-24–OP-28, OP-27 (`expects=`), OP-04 (`missing_ok=`), OP-29 (rejected: `via=λ`). |
| **go-patch / yaml-patch** | `type: replace/remove`, `path` with `name=value`, trailing `?` | §4.2, §4.4, OP-01, OP-05, OP-16. |
| **jd** | path-scoped hunks, `-`/`+` values, set/multiset modes | The margin grammar itself; OP-20 (rejected: set semantics). |
| **kustomize** | target selector; `patchesStrategicMerge`; `patchesJson6902`; `replacements` (field→field) | §2.2 `--- ` target line; OP-23. |
| **RFC 5261 XML Patch** | `<add>` with `pos="before"\|"after"\|"prepend"`, `type="@attr"`, `<replace>`, `<remove ws=>` | OP-12, OP-13; attribute addressing sketched in §12.3. |
| **spruce** | `(( delete ))`, `(( append ))`, `(( prepend ))`, `(( insert after/before ))`, `(( merge on <key> ))` | OP-05, OP-11, OP-12, OP-13, OP-16. |
| **CUE** | unification; **no delete** | Not an op vocabulary. Its missing delete is why Hew is not a unification language. |
| **jsonnet** | `+:` deep merge, `::` hide-from-output | OP-09 (rejected), OP-05. |
| **Coccinelle SmPL** | `-`/`+` margins in-shape, metavariables | §3 margins; metavariables not adopted (no pattern variables in v0 — OP-29). |
| **ctxloom M1 ledger** | record-owned-names, withdraw-then-re-add | OP-51 (out of scope: ownership, [O14](#open-questions-for-ratification)). |
| **ctxloom M2 managed section** | splice block, strip section, remove file when empty, never create absent file | OP-45, OP-49 (deferred), OP-48 (deferred). |
| **ctxloom M3 structural merge** | remove-owned-by-name then add current set | OP-16, OP-17. |
| **ctxloom M4 package files** | render-then-swap, delete stale tracked files, empty-render guard | File-level; OP-52, OP-49. |
| **ctxloom M5 byte-level MCP** | install/uninstall one server, installed? | OP-16, OP-17, OP-24. |
| **ctxloom M6 `config-write`** | deep merge, arrays replace wholesale, refuse empty patch | OP-09 (rejected), OP-08, §10.2. |
| **ctxloom M7 `config.yaml`** | node-tree patch preserving unchanged sections, drop removed keys, append new keys sorted | OP-01, OP-05, OP-02 — and it is the closest existing thing to an Hew applier. |
| **ctxloom M8 gitignore** | append-only idempotent block, content-match idempotency | OP-50 (deferred: needs a line-oriented text binding). |

---

### 11.2 Mapping and key operations

#### OP-01 `set-scalar` — replace the value of an existing key
**Status** v0 · **Disp** `CORE` — `replace` · **Sources** 6902 `replace`, 7386, ytt default, go-patch, jd, M3/M6/M7
**Absent/empty** Key absent → `HEW013`. Never creates. Value equal to the target already → the
`-` line still matches, the op is a no-op replace, exit 0.
**Mirror**
```
@@ /server @@
- timeout: 30
+ timeout: 60
```
**IR** `{op: test, path: /server/timeout, value: 30}` then `{op: replace, path: /server/timeout, value: 60}`
**Formats** json ✓ · jsonc ✓ · yaml ✓ · toml ✓ · hcl ✓ · markdown — (no keys)
**Errors** `HEW010` `HEW013` · **Corpus** `json/set-scalar`, `yaml/set-scalar`, `toml/set-scalar-dotted`, `hcl/set-attribute`

#### OP-02 `add-key` — create a key that must not exist
**Status** v0 · **Disp** `CORE` — `add` · **Sources** 6902 `add`, ytt `@overlay/match missing_ok=True` + insert
**Absent/empty** Key already present → `HEW014 already-exists`. This is the strict default:
adding over an existing key is a drift signal, not a convenience.
**Mirror**
```
@@ /server @@
  port: 8080
+ tls: true
```
**IR** `{op: add, path: /server/tls, value: true, after: /server/port}`
**Formats** json ✓ · jsonc ✓ · yaml ✓ · toml ✓ · hcl ✓ · markdown —
**Errors** `HEW014` · **Corpus** `json/add-key`, `hcl/add-attribute`

#### OP-03 `upsert-key` — add, or replace whatever is there
**Status** v0 · **Disp** `CORE` — `add` + `on_conflict: replace` · **Sources** go-patch trailing `?`, ytt `missing_ok`, M5 install
**Absent/empty** Absent → created. Present → replaced regardless of current value. **This
operation deliberately does not assert the prior state**, so it is the one mapping write that
cannot detect drift; use it only where ctxloom owns the key outright (M5's exact case).
**Mirror** — `! upsert` (§7.7), line-scoped:
```
@@ /mcp_servers @@
! upsert
+ taskloom = { command = "taskloom" }
```
**IR** `{op: add, path: /mcp_servers/taskloom, on_conflict: replace, value: {...}}`
**Formats** json ✓ · jsonc ✓ · yaml ✓ · toml ✓ · hcl ✓ · markdown —
**Errors** — (this op has no failure of its own) · **Corpus** `toml/upsert-key`

#### OP-04 `default-key` — add only if absent, leave an existing value alone
**Status** v0 · **Disp** `CORE` — `add` + `on_conflict: keep` · **Sources** ytt `missing_ok=True` without replace; jsonnet `+:` for absent fields
**Absent/empty** Absent → created. Present → **untouched**, zero ops, exit 0. The
"defaulting" operation: seeding a config key without stomping a user's choice.
**Mirror** — `! default`, line-scoped:
```
@@ /server @@
! default
+ timeout: 30
```
**IR** `{op: add, path: /server/timeout, on_conflict: keep, value: 30}`
**Formats** json ✓ · jsonc ✓ · yaml ✓ · toml ✓ · hcl ✓ · markdown —
**Errors** — · **Corpus** `yaml/default-key-present`, `yaml/default-key-absent`

#### OP-05 `remove-key`
**Status** v0 · **Disp** `CORE` — `remove` · **Sources** 6902 `remove`, 7386 `null`, SMP `$patch: delete`, ytt
`@overlay/remove`, spruce `(( delete ))`, jsonnet `::`, M3/M7
**Absent/empty** Absent → `HEW013`. Present but unequal to the `-` line's value → `HEW010`.
Removing the last child leaves an empty container; it does **not** cascade-delete the parent
(see OP-49 for the file-level analogue ctxloom's M2 performs).
**Mirror**
```
@@ /server @@
- host: localhost
```
**IR** `{op: test, path: /server/host, value: localhost}` then `{op: remove, path: /server/host}`
**Formats** json ✓ · jsonc ✓ (with its leading comment, §8.2) · yaml ✓ · toml ✓ · hcl ✓ · markdown —
**Errors** `HEW010` `HEW013` · **Corpus** `json/delete-key`, `jsonc/delete-key-with-comment`

#### OP-06 `remove-key-if-present`
**Status** v0, **discouraged** · **Disp** `CORE` — `remove` + `optional` · **Sources** ytt `expects="0+"`, patch(1) tolerance
**Absent/empty** Absent → no-op, exit 0. **This is the only construct in Hew that can silently
do nothing**, which is why §7.6 requires a justifying comment and a linter warning.
**Mirror**
```
@@ /server @@
# legacy key, may already be gone on fresh installs
! optional
- deprecated_flag: true
```
**IR** `{op: remove, path: /server/deprecated_flag, optional: true}`
**Formats** all six as OP-05 · **Errors** — · **Corpus** `yaml/remove-optional-absent`

#### OP-07 `rename-key`
**Status** v0, **IR-only** · **Disp** `SUGAR` — → `copy` + `remove` · **Sources** 6902 `move` within a container
**Absent/empty** Source absent → `HEW013`. Destination present → `HEW014`.
**Mirror** IR-only. The mirror form (`- old: v` / `+ new: v`) is a delete-and-add and loses
the node's comments and source bytes — see [O16](#open-questions-for-ratification).
**IR** `{op: copy, from: /server/timeout, path: /server/timeout_seconds}` then `{op: remove, path: /server/timeout}`
**Formats** json ✓ · jsonc ✓ · yaml ✓ · toml ✓ · hcl ✓ · markdown —
**Errors** `HEW013` `HEW014` · **Corpus** `yaml/ir-rename-key`

#### OP-08 `replace-container-wholesale`
**Status** v0 · **Disp** `CORE` — `replace` at the container path · **Sources** 7386 (its only array mode), SMP `$patch: replace`, M6 (arrays
replace wholesale)
**Absent/empty** Container absent → `HEW013`. Replacing with an empty container is legal and
**not** treated as a no-op — an explicit empty is a real value, and Hew will not second-guess
it the way a merge tool would.
**Mirror** anchor the hunk at the container's parent and mark the whole value:
```
@@ /server @@
- tags: [alpha, beta]
+ tags: []
```
**IR** `{op: replace, path: /server/tags, value: []}`
**Formats** json ✓ · jsonc ✓ · yaml ✓ · toml ✓ · hcl ✓ · markdown ✓ (a section's body)
**Errors** `HEW010` `HEW013` · **Corpus** `json/replace-array-wholesale`

#### OP-09 `deep-merge-container`
**Status** **rejected** · **Disp** `OUT` · **Sources** 7386, SMP `$patch: merge`, jsonnet `+:`, spruce, M6
**Why not.** A deep merge is exactly the operation whose result you cannot read off the patch
— the survey's central finding, and ctxloom's own `config-write` (M6) is the local proof: its
deep merge has no ownership record, replaces arrays wholesale without saying so, and cannot be
withdrawn. Hew's position is that a merge is a *set of explicit ops*, and the differ (§9.4) is
how you get that set without typing it. Reopening this would reintroduce the "reads as an
overlay, applies as something else" gap that motivated the format.
**Migration for M6** — `config-write`'s patch object becomes a generated transform list.

#### OP-10 `null-as-delete`
**Status** **rejected** · **Disp** `OUT` · **Sources** RFC 7386, jsonnet `::`
**Why not.** `null` is a legal JSON/YAML *value*. A format in which setting a key to null and
deleting a key are the same keystroke cannot express "set this to null", and every 7386
consumer has this bug. Hew has an explicit `-` margin and an explicit `remove` op; `+ x: null`
sets null.

---

### 11.3 Sequence operations

#### OP-11 `append-element`
**Status** v0 · **Disp** `CORE` — `add`, placement omitted · **Sources** 6901 `-` token, ytt `@overlay/append`, spruce `(( append ))`, jsonnet `+:`
**Absent/empty** Sequence absent → `HEW013` (use OP-03 to create it). Empty sequence → appends
as the only element, legal.
**Mirror** — a `+` line with no following context sibling:
```
@@ /tags @@
  - beta
+ - gamma
```
**IR** `{op: add, path: /tags, after: /tags/=beta, value: gamma}` (or `end` with no context)
**Formats** json ✓ · jsonc ✓ · yaml ✓ · toml ✓ (array + array-of-tables) · hcl ✓ (tuple exprs) · markdown ✓ (list blocks)
**Errors** `HEW013` · **Corpus** `yaml/list-append`, `json/array-append`

#### OP-12 `prepend-element`
**Status** v0 · **Disp** `CORE` — `add` + `before` · **Sources** spruce `(( prepend ))`, RFC 5261 `pos="prepend"`
**Absent/empty** As OP-11.
**Mirror** — a `+` line whose only sibling context follows it:
```
@@ /tags @@
+ - aardvark
  - alpha
```
**IR** `{op: add, path: /tags, before: /tags/=alpha, value: aardvark}`
**Formats** as OP-11 · **Errors** `HEW013` · **Corpus** `yaml/list-prepend`

#### OP-13 `insert-before` / `insert-after`
**Status** v0 · **Disp** `CORE` — `add` + `before`/`after` · **Sources** ytt `@overlay/insert before=/after=`, spruce `(( insert after ))`, RFC 5261 `pos=`
**Absent/empty** The reference sibling must exist and match, or `HEW010`. This is the operation
that makes context lines load-bearing rather than decorative (§9.0).
**Mirror** — position falls out of the surrounding context (§6.2), with no keyword at all:
```
@@ /tags @@
  - alpha
+ - alpha2
  - beta
```
**IR** `{op: add, path: /tags, after: /tags/=alpha, value: alpha2}`
**Formats** as OP-11 · **Errors** `HEW010` `HEW013` · **Corpus** `yaml/list-insert-middle`

#### OP-14 `remove-element-by-index`
**Status** v0 · **Disp** `CORE` — `remove`, index address · **Sources** 6902 (its only array removal), jd
**Absent/empty** Index out of range → `HEW013`. The element's value must match the `-` line.
**Mirror**
```
@@ /tags @@
  - alpha
- - beta
```
**IR** `{op: test, path: /tags/1, value: beta}` then `{op: remove, path: /tags/1}`
**Formats** as OP-11 · **Errors** `HEW010` `HEW013` · **Corpus** `json/array-remove-element`

#### OP-15 `remove-element-by-value` (primitive lists)
**Status** v0 · **Disp** `CORE` — `remove`, `=value` address · **Sources** SMP `$deleteFromPrimitiveList`
**Absent/empty** No element equals the value → `HEW013`. More than one equal element →
`HEW012` (a duplicated scalar in a list is drift; Hew names it rather than picking the first).
**Mirror** identical to OP-14 — the author writes `- - beta` and the *parser* chooses a
value-match address for scalar sequences rather than an index, so the patch survives
reordering.
**IR** `{op: remove, path: /tags/=beta}` — the `=value` form of §4.2 with an empty field name,
meaning "the element that equals this".
**Formats** json ✓ · jsonc ✓ · yaml ✓ · toml ✓ · hcl ✓ · markdown — · **Errors** `HEW012` `HEW013`
**Corpus** `yaml/list-remove-by-value`, `yaml/list-remove-by-value-duplicate` (error)

#### OP-16 `add-or-replace-keyed-element` — the headline operation
**Status** v0 · **Disp** `CORE` — `add` + `on_conflict`, key-match address · **Sources** go-patch `name=value` + `?`, SMP merge-key semantics, spruce
`(( merge on ))`, ytt `overlay.subset()`, **and ctxloom M3/M5** (every engine's MCP-server
registration is exactly this)
**Absent/empty** With `?` on the anchor: absent → inserted at the position the body's context
implies. Without `?`: absent → `HEW013`. Two elements with the same key → `HEW012`.
**Mirror**
```
@@ /mcpServers @@
  - name: github
+ - name: ctxloom
+   command: ctxloom
+   args: [mcp]
```
**IR** `{op: add, path: /mcpServers, after: /mcpServers/name=github, value: {...}}`
**Formats** json ✓ · jsonc ✓ · yaml ✓ · toml ✓ (`[[x]]`) · hcl ✓ (blocks by label) · markdown —
**Errors** `HEW012` `HEW013` `HEW014` · **Corpus** `yaml/keyed-array-add`, `json/keyed-array-add`, `toml/array-of-tables-add`

#### OP-17 `remove-keyed-element`
**Status** v0 · **Disp** `CORE` — `remove`, key-match address · **Sources** SMP `$patch: delete` with merge key, M3 `removeManagedMCP`
**Absent/empty** Absent → `HEW013`; ambiguous → `HEW012`.
**Mirror**
```
@@ /mcpServers @@
- - name: legacy
```
**IR** `{op: remove, path: /mcpServers/name=legacy}`
**Formats** as OP-16 · **Errors** `HEW012` `HEW013` · **Corpus** `json/keyed-array-remove`

#### OP-18 `patch-inside-keyed-element`
**Status** v0 · **Disp** `CORE` — any core op, key-match address · **Sources** go-patch `/instance_groups/name=zookeeper/instances`
**Absent/empty** As OP-16 for the selector; then per the inner operation.
**Mirror** — anchor the hunk at the element:
```
@@ /mcpServers/name=github @@
  command: npx
+ env:
+   GITHUB_TOKEN: ${GITHUB_TOKEN}
```
**IR** `{op: add, path: /mcpServers/name=github/env, value: {...}}`
**Formats** as OP-16 · **Errors** `HEW010` `HEW012` `HEW013` · **Corpus** `yaml/keyed-element-inner-add`

#### OP-19 `reorder-sequence` / `set-element-order`
**Status** **deferred** · **Disp** `OUT` · **Sources** SMP `$setElementOrder`
**Why not in v0.** No mechanism in M1–M8 reorders a list; ctxloom's own ledger sorts on
render (M1) so ordering is derived, not patched. Expressible today as a series of IR `move`
ops, verbosely. **Condition to add:** a named case where the order of a config list is
semantically load-bearing *and* changes independently of its contents.

#### OP-20 `set` / `multiset` sequence semantics
**Status** **rejected** · **Disp** `OUT` · **Sources** jd's `-set`/`-mset` modes
**Why not.** Treating a sequence as unordered changes what "the same document" means, and it
would silently make OP-12/OP-13 meaningless. Hew sequences are ordered. A user who wants set
semantics wants `? exhaustive` plus key-match addressing, which Hew has.

---

### 11.4 Move and copy

#### OP-21 `move-node`
**Status** v0, **IR-only** · **Disp** `SUGAR` — → `copy` + `remove` · **Sources** RFC 6902 `move`
**Absent/empty** Source absent → `HEW013`. Destination present → `HEW014`. Destination inside
the source subtree → `HEW001` (6902's own prohibition).
**Mirror** IR-only. §9.6 is the authoring surface; Appendix C.1 is the rationale.
**IR** `{op: copy, from: /server/host, path: /network/host}` then `{op: remove, path: /server/host}`
**Formats** json ✓ · jsonc ✓ (comments travel) · yaml ✓ · toml ✓ · hcl ✓ · markdown ✓ (a block or section)
**Errors** `HEW001` `HEW013` `HEW014` · **Corpus** `yaml/ir-move-node`, `markdown/ir-move-section`

#### OP-22 `copy-node`
**Status** v0, **IR-only** · **Disp** `CORE` — `copy` · **Sources** RFC 6902 `copy`
**Absent/empty** As OP-21, minus the containment prohibition.
**IR** `{op: copy, from: /defaults, path: /service_c}`
**Formats** as OP-21 · **Errors** `HEW013` `HEW014` · **Corpus** `json/ir-copy-node`

#### OP-23 `copy-value-between-fields`
**Status** v0, **IR-only** · **Disp** `CORE` — `copy` · **Sources** kustomize `replacements`
Same record as OP-22 with a scalar source. Cataloged separately because kustomize treats it
as a distinct feature and a reader coming from kustomize will look for it by that name.
**Corpus** covered by `json/ir-copy-node`.

---

### 11.5 Assertions

#### OP-24 `test-value`
**Status** v0 · **Disp** `CORE` — `test` + `value` · **Sources** 6902 `test`, ytt `@overlay/assert`, CUE's unification conflict
**Absent/empty** Node absent → `HEW011` (distinct from `HEW013`: an assertion that a node holds
a value fails *as an assertion* when it is missing).
**Mirror** a context line, or `? expect`. **Both compile to the same transform** (§9.0).
**IR** `{op: test, path: /version, value: 1.2.0}`
**Formats** all six · **Errors** `HEW011` · **Corpus** `yaml/assert-expect-ok`, `yaml/assert-expect-fail`

#### OP-25 `assert-absent`
**Status** v0 · **Disp** `CORE` — `test` + `absent` · **Sources** ytt `expects="0"`, no 6902 equivalent (a real gap in 6902)
**IR** `{op: test, path: /env/API_KEY, absent: true}` · **Mirror** `? absent /env/API_KEY`
**Formats** all six · **Errors** `HEW011` · **Corpus** `jsonc/assert-absent-fail`

#### OP-26 `assert-exhaustive`
**Status** v0 · **Disp** `SUGAR` — → `test`+`count` and one `test`+`value` per listed child · **Sources** implicit in 7386/SMP whole-array replace; explicit nowhere
**Semantics** The listed children are the container's complete child set (§6.1). **This is the
operation that makes Merge Patch's expensive accident cheap and deliberate.**
**IR** desugars to `{op: test, path: /permissions, count: 2}` plus one `{op: test, …, value: …}` per listed child · **Mirror** `? exhaustive`
**Formats** json ✓ · jsonc ✓ · yaml ✓ · toml ✓ · hcl ✓ · markdown ✓ (a section's block list)
**Errors** `HEW011` · **Corpus** `json/assert-exhaustive-fail`

#### OP-27 `assert-count`
**Status** v0 · **Disp** `CORE` — `test` + `count` · **Sources** ytt `expects="1"`, `expects="0+"`, `expects="1+"`
**IR** `{op: test, path: /mcpServers, count: 3}` · **Mirror** `? count /mcpServers = 3`
**Formats** all six · **Errors** `HEW011` · **Corpus** `yaml/assert-count-fail`

#### OP-28 `assert-kind`
**Status** v0 · **Disp** `CORE` — `test` + `kind` · **Sources** no direct source; derived from `agent.InstallMCPServerJSON`'s
measured refusal of an `mcpServers` key "present but of the wrong type" (and B6, the codex
twin that lacks the guard)
**IR** `{op: test, path: /mcpServers, kind: map}` · **Mirror** `? kind /mcpServers = map`
**Formats** all six · **Errors** `HEW011` · **Corpus** `json/assert-kind-fail`

#### OP-29 `computed` / `pattern` assertions
**Status** **rejected** · **Disp** `OUT` · **Sources** ytt `@overlay/replace via=lambda`, Coccinelle
metavariables, jq/starlark expressions in adjacent tools
**Why not.** A patch format with an expression language is a template engine, and a `.hew` file
that computes its own values is no longer readable as "the document with a diff on it" — the
one property §1 exists to protect. Equality and existence only.

---

### 11.6 Comment operations

Comments are nodes in JSONC, YAML, TOML and HCL, and Hew treats them as such. **Decision (this
spec, resolving the brief's open point): a patch CAN carry a comment for a node it adds.**
Two forms, both v0.

#### OP-30 `add-comment-node` — a standalone comment
**Status** v0 · **Disp** `CORE` — `add` at a comment address · **Sources** none of the surveyed patch formats can do this at all; it is
required by ctxloom's own managed-marker practice (M1/M2 both write explanatory comments)
**Absent/empty** Position follows §6.2 like any other node.
**Mirror**
```
@@ /server @@
+ # ctxloom-managed — regenerate with `ctxloom apply`
  timeout: 60
```
**IR** `{op: add, path: /server/#0, before: /server/timeout, value: {comment: "ctxloom-managed — …"}}`
**Formats** json ✗ (`HEW020`) · jsonc ✓ · yaml ✓ · toml ✓ · hcl ✓ · markdown ✓ (HTML comment block)
**Errors** `HEW020` · **Corpus** `toml/add-comment-line`, `json/comment-inexpressible` (error)

#### OP-31 `attach-comment-to-added-node`
**Status** v0 · **Disp** `SUGAR` — → two `add` records (comment node, then member) · **Sources** §8.2's anchoring rule, applied to added nodes
**Semantics** A `+` comment line immediately preceding a `+` member becomes that member's
**leading** comment and travels with it (moves on OP-21, deletes on OP-05). This is the
mirror form; the IR carries it as a qualifier so an applier need not reconstruct adjacency.
**Mirror**
```
@@ /mcp_servers @@
+ # added by taskloom manage install
+ [mcp_servers.taskloom]
+ command = "taskloom"
```
**IR** desugars to `{op: add, path: /mcp_servers/#0, value: {comment: "added by taskloom manage install"}, before: /mcp_servers/taskloom}` plus `{op: add, path: /mcp_servers/taskloom, value: {...}}`
**Formats** json ✗ · jsonc ✓ · yaml ✓ · toml ✓ · hcl ✓ · markdown —
**Errors** `HEW020` · **Corpus** `jsonc/add-with-leading-comment`

#### OP-32 `remove-comment` · #### OP-33 `replace-comment-text`
**Status** v0 · **Disp** `CORE` — `remove`, comment address · Comments are matched by exact text (§6.1) and removed/replaced like any node.
**Mirror** `- # old note` / `+ # new note`
**IR** `{op: replace, path: <comment path>, value: {comment: "new note"}}`
**Formats** as OP-30 · **Errors** `HEW010` `HEW020` · **Corpus** `yaml/replace-comment`

---

### 11.7 Format-specific operations

#### OP-34 `hcl-block-create` · #### OP-35 `hcl-block-remove`
**Status** v0 · **Disp** `CORE` — `add`, label address · **Sources** HCL's block/attribute duality (§8.5); `hclwrite`'s DOM API
**Absent/empty** Create over an existing identical tuple → `HEW014` unless the intent is a
second sibling, which requires `! match ord=` on neither line (a genuinely new sibling is an
`add` at the body level, and the corpus pins that it does not accidentally target the
existing one).
**Mirror** a `+`/`-` run covering the whole block including its braces (§8.5).
**IR** `{op: add, path: /provider/"aws", value: {region: "us-west-1"}}` — the label is in the address, never in the value
**Formats** hcl ✓ only · **Errors** `HEW012` `HEW014` · **Corpus** `hcl/add-block`, `hcl/remove-block`

#### OP-36 `hcl-block-relabel`
**Status** v0, **IR-only** · **Disp** `SUGAR` — → `copy` + `remove` · A relabel is a `move` between label tuples (OP-21).
**IR** `{op: copy, from: /provider/"aws", path: /provider/"aws-legacy"}` then `{op: remove, path: /provider/"aws"}`
**Errors** `HEW012` `HEW013` `HEW014` · **Corpus** `hcl/ir-relabel-block`

#### OP-37 `select-repeated-block` (ordinal selector)
**Status** v0 · **Disp** `CORE` — addressing mode, not an op · Not an operation but a **selector**, cataloged because the survey's HCL check
treated it as a missing capability. §7.2 `! match [label=[…]] ord=<n>`.
**Formats** hcl ✓ · others — (no format else permits repeated identical keys)
**Errors** `HEW001` (unnecessary ordinal) `HEW011` (label cross-check) `HEW012` `HEW013`
**Corpus** `hcl/repeated-label-ordinal`, `hcl/repeated-label-ambiguous` (error), `hcl/ordinal-context-guard` (error)

#### OP-38 `toml-surface-placement-on-add`
**Status** v0 · **Disp** `CORE` — `surface` qualifier · **Sources** the TOML dotted/table wrinkle (§8.4)
**Semantics** `! surface table|dotted` chooses the surface form for a **creation** only.
**IR** `{op: add, path: /a/b/c, value: 1, surface: table}`
**Formats** toml ✓ only · **Errors** `HEW041` · **Corpus** `toml/surface-directive-table`, `toml/surface-ambiguous` (error)

#### OP-39 `toml-surface-migration`
**Status** **deferred** · **Disp** `OUT` · Rewriting `[a.b]` ↔ `a.b = {…}` (§8.4 rule 4, Appendix C.4).
`HEW020` today. **Condition to add:** a named case where the surface itself, not the value, is
what needs to change — most plausibly a formatter, which is a different tool.

#### OP-40 `yaml-anchor-rewrite` · #### OP-41 `yaml-alias-fork`
**Status** v0 · **Disp** `CORE` — `anchor: rewrite` qualifier · **Sources** the YAML anchor wrinkle (§8.3); no surveyed format addresses it
**Absent/empty** Editing at an alias site with neither directive → `HEW040`, always. There is
no default, deliberately: both answers are destructive in one direction.
**IR** `{op: replace, path: /service_a/timeout, value: 60, anchor: fork}` — one core op, one core qualifier
**Formats** yaml ✓ only · **Errors** `HEW040` `HEW013` (merge-inherited key)
**Corpus** `yaml/anchor-rewrite`, `yaml/alias-fork`, `yaml/alias-ambiguous` (error), `yaml/merge-key-remove` (error)

#### OP-42 `md-replace-block` · #### OP-43 `md-insert-section` · #### OP-44 `md-remove-section`
**Status** v0 · **Disp** `CORE` — `replace`, block address · **Sources** ctxloom's own Markdown surfaces (CLAUDE.md, AGENTS.md, steering files)
**Absent/empty** Section absent → `HEW013`; duplicate heading → `HEW012`. Inserting a section
uses §6.2 placement against sibling sections.
**Mirror** per §8.6 — whole-block margins.
**IR** `{op: replace, path: "/# ctxloom/## Install/code:0", value: {...}}`
**Formats** markdown ✓ only · **Errors** `HEW010` `HEW012` `HEW013`
**Corpus** `markdown/replace-code-block`, `markdown/insert-section-after`, `markdown/remove-section`, `markdown/duplicate-heading` (error)

#### OP-45 `md-replace-managed-region`
**Status** v0 · **Disp** `CORE` — `replace` + `idempotent`, marker address · **Sources** ctxloom M2 verbatim (`agent.WriteManagedContext`)
**Absent/empty** Begin marker present without end → `HEW002` (refuse, never repair). Region
absent entirely → `HEW013`; **creating** the region is `! upsert` on the marker path. The
markers are never part of the node's value, so no operation can destroy them (§8.6).
**IR** `{op: replace, path: /@ctxloom:context, value: {...}, idempotent: true}`
**Formats** markdown ✓ only · **Errors** `HEW002` `HEW013` · **Corpus** `markdown/managed-region-replace`

#### OP-46 `md-sub-block-edit`
**Status** **rejected** · **Disp** `OUT` · Editing one line of a paragraph (Appendix C.3). `HEW020`.
**Why not.** Sub-block addressing means addressing prose by offset, which is the exact
fragility structural patching exists to escape.

#### OP-47 `md-heading-level-change`
**Status** **deferred** · **Disp** `OUT` · Promoting `##` to `#` restructures the section tree; expressible
today only as remove + add of the whole section. **Condition to add:** a named case.

---

### 11.8 File-level operations (from ctxloom M1–M8)

These are operations ctxloom's real mechanisms perform that are **not node operations**. They
are cataloged because omitting them would make the catalog look complete while leaving the
mechanisms Hew is meant to serve unaccounted for.

| # | Operation | Source | Status | Where it lives instead |
|---|---|---|---|---|
| OP-48 | `create-file-if-absent` | M2 (never creates), M4 (does create) | **deferred** | The CLI's `--create` would own it. The IR is per-target and assumes the target exists. Named because M2's "an absent file is never created" is a *deliberate* behavior Hew must be able to express. |
| OP-49 | `delete-file-when-empty` | M2 (removes the file when nothing user-authored remains), M4 (deletes stale tracked files) | **deferred** | Same. Note this is the one place ctxloom's own discipline requires a file-level effect *derived from* a node-level result. [O22](#open-questions-for-ratification). |
| OP-50 | `append-only-idempotent-block` | M8 (`.gitignore`) | **deferred** | Needs a line-oriented `text` binding, which is not one of the six. Expressible as OP-45 once such a binding exists. |
| OP-51 | `ledger-recorded-withdrawal` | M1 (`.ctxloom-managed`) | **out of scope** | Ownership records are not a patch operation. But see [O14](#open-questions-for-ratification): an applied `.hew` file is itself a candidate ownership record, which would make `hew revert` the withdrawal story. |
| OP-52 | `whole-file-replace` | M4, kiro's dedicated files | v0 | `{op: replace, path: /, value: {...}}` — a replace at the root anchor. Legal and boring; listed so nobody invents a file-level op for it. |

---

### 11.9 Catalog summary

| Status | Count | Entries |
|---|---|---|
| **v0-normative** | 36 | OP-01–08, 11–18, 21–28, 30–38, 40–45, 52 |
| **deferred** | 6 | OP-19, 39, 47, 48, 49, 50 |
| **rejected** | 5 | OP-09, 10, 20, 29, 46 |
| **out of scope** | 1 | OP-51 |

| Disposition | Count | Entries |
|---|---|---|
| **CORE** | 31 | everything v0 except the five below |
| **SUGAR** (parser desugars; never in the IR) | 5 | OP-07 rename, OP-21 move, OP-26 exhaustive, OP-31 comment-attachment, OP-36 relabel |
| **OUT** | 12 | the deferred, rejected and out-of-scope rows above |

Every rejected entry names the property it would break. Every deferred entry names the
condition that would revive it. That is what "exhaustive" is for: a future reader arguing for
`$setElementOrder` should be arguing against OP-19's stated condition, not rediscovering the
question.

### 11.10 The reduced core — how 52 operations became five

**The rule (human, 2026-08-14): the survey is exhaustive; the normative set is not.** The IR
reduces to the smallest orthogonal core that comfortably describes every `v0` row. No
duplication. No sugar that could be spelled another way. *The IR is essentially ASM* — few
primitives, stable, composable — and every bit of richness lives in the notation and the
parser that compiles it.

**The promotion test.** A surveyed verb becomes a core op **only if** no composition of
existing core ops plus addressing produces the same observable result. "Observable" includes
byte preservation and attached comments, because those are contract in this format (§6.3).

**What survived, and what it displaced:**

| Core op | Displaces (surveyed verbs that compile to it) |
|---|---|
| `test` | 6902 `test`; ytt `@overlay/assert` and every `expects=`; CUE's unification conflict; `? expect`/`? absent`/`? count`/`? kind`/`? exhaustive`; **every context line in the mirror grammar** |
| `add` | 6902 `add`; 7386 implicit set; ytt `@overlay/append` / `insert before=` / `insert after=` / `missing_ok`; spruce `(( append ))` / `(( prepend ))` / `(( insert after ))`; RFC 5261 `pos="prepend"/"before"/"after"`; jsonnet `+:`; M5 install |
| `remove` | 6902 `remove`; 7386 `null`; SMP `$patch: delete` and `$deleteFromPrimitiveList`; ytt `@overlay/remove`; spruce `(( delete ))`; jsonnet `::`; M3 withdraw |
| `replace` | 6902 `replace`; SMP `$patch: replace`; ytt `@overlay/replace`; M1/M7 rewrite |
| `copy` | 6902 `copy`; kustomize `replacements`; **and half of `move`** |

**The four reductions, each with its proof obligation:**

1. **`move` → `copy` + `remove`.** Observably identical: `copy` is defined to carry the source
   node's bytes and attached comments, so removing the source afterwards leaves exactly what a
   `move` would. 6902's six become five. `op: move` is accepted on input and normalized away.
2. **`exhaustive` → `count` + per-child `test`.** "These are all the children" is exactly "the
   child count is N" conjoined with "each of these N is present and equal", both of which are
   already core. The qualifier bought nothing but a shorter record.
3. **`comment: {leading, trailing}` → an `add` at a comment address.** This one required a
   *change elsewhere*: comments needed addresses (§4.5b) — which they needed anyway for OP-32
   and OP-33. Once they had them, the qualifier was redundant. This is the reduction working
   as intended: it forced the node model to become uniform instead of letting a qualifier
   paper over a gap.
4. **`ord` / `labels` → addressing.** An ordinal is an addressing mode, not an operation. It
   belongs on the path in the IR, exactly as it belongs *off* the path in the notation (§4.3,
   §7.2). The notation keeps ordinals visible as annotations because a human should see the
   fragility; the ASM puts them in the address because that is what they are.

**The case examined hardest: ordering-sensitive inserts (OP-11, OP-12, OP-13).**

The temptation is four ops — `append`, `prepend`, `insert-before`, `insert-after` — because
four surveyed systems spell them that way (ytt, spruce, RFC 5261, and 6901's `-` token). They
compile to **one** core op with a placement qualifier:

| Surveyed spelling | Core form |
|---|---|
| ytt `@overlay/append`, spruce `(( append ))`, 6901 `/tags/-` | `add`, no placement (default: end) |
| spruce `(( prepend ))`, RFC 5261 `pos="prepend"` | `add`, `before: <first sibling>` |
| ytt `insert before=`, RFC 5261 `pos="before"` | `add`, `before: <sibling>` |
| ytt `insert after=`, spruce `(( insert after ))`, RFC 5261 `pos="after"` | `add`, `after: <sibling>` |

**Could placement itself be reduced further — to the path?** This was the closest call in the
reduction. `add path: /tags/-` (append) and `add path: /tags/0` (prepend) are pure addressing
and need no qualifier at all. But an identity-based insert — "after the element named
github" — has no path spelling that is not an index, and an index is precisely the fragility
§6.4 exists to avoid. So placement stays, as **two fields expressing one concept**, and
`before`/`after` cannot collapse into one: `before: X` is not `after: <predecessor of X>`
when X is first, and naming a predecessor requires knowing one exists.

**The overlap that was kept deliberately.** `replace` and `add`+`on_conflict: replace` produce
the same bytes when the node exists. They are not duplicates: `replace` **requires** the node
(`HEW013` if absent), `add`+`on_conflict: replace` does not. The difference is a precondition,
which is orthogonal to the effect — see [O26](#open-questions-for-ratification), because a
reviewer could reasonably call this the one piece of redundancy that survived.

---

## 12. Documented-only formats

Named by the spec, with an addressing sketch, so that a future binding does not have to
redesign the address grammar. **No corpus cases, no v0 implementation.**

### 12.1 INI-family

There is no INI standard, and "INI" bundles at least three incompatible dialects. A binding
must therefore be **dialect-parameterized**, not universal:

| Dialect | Example | Addressing sketch |
|---|---|---|
| git-style | `[remote "origin"]` | Quoted subsection = a **label segment** (§4.3), reusing HCL's rule: `/remote/"origin"/url`. |
| systemd-style | `[Service]` with repeatable keys | `/Service/ExecStart` addresses a *sequence* when the key repeats; a repeated key that the patch addresses as a scalar is `HEW012`. |
| Java `.properties` / `.npmrc` | `a.b.c=1`, flat | **Flat, not a tree.** `/a.b.c` is one segment. The `~1` escape is mandatory for any `/` in a key. A binding MUST NOT split on `.`. |

The dialect is a required binding parameter (`format=ini dialect=git`), never inferred.

### 12.2 dotenv

Flat `KEY=VALUE`, no sections, no nesting. `/DATABASE_URL` is the whole address grammar. The
measured gap is on the *write* side (`joho/godotenv` re-serializes and does not claim comment
preservation), so a binding needs its own line-oriented editor — which is why this is
document-only despite being the simplest model surveyed.

### 12.3 XML

Node kinds: element, attribute, text, comment, processing instruction. Addressing sketch: Hew
paths map onto a **restricted XPath subset** — `/config/server/@port` for attributes,
`/config/servers/server/name="ctxloom"` reusing the key-match segment for XPath's predicate
form. RFC 5261 is prior art for the op side and should be the down-compilation target instead
of 6902 for this format. Mixed content (text interleaved with elements) has no
shape-mirroring answer and is the reason XML is document-only.

---

## 13. The conformance corpus

`tests/hewcorpus/` is the normative artifact. The Go implementation and the Rust port run the
same directory. **The corpus pins the pipeline at three independent seams, not just
end-to-end** — an end-to-end-only corpus cannot tell a parser bug from an applier bug, and the
two are written by different people in different languages.

### 13.1 The three seams

| Seam | Input → output | What it isolates |
|---|---|---|
| **`parse`** | `patch.hew` → transform list | The notation. Format-agnostic; needs no target file and no backend. A Rust port can pass every `parse` case before a single backend exists. |
| **`apply-ir`** | `transforms.hewt` + `target.*` → `expected.*` | The applier. Format mechanics and byte preservation, with the notation entirely out of the picture. |
| **`e2e`** | `patch.hew` + `target.*` → `expected.*` | The composition, which is what a user experiences. |

A case directory carrying `patch.hew`, `transforms.hewt`, `target.*` and `expected.*` pins all
three from one set of fixtures, and the runner asserts each independently. This is why
`transforms.hewt` (§9.6) is a corpus file and not an implementation detail — and why
`hew apply --transforms` (Appendix B) gets conformance coverage for free: it is the
`apply-ir` seam with a CLI in front of it.

Two further seams exist for the P4 differ:

| Seam | Input → output | What it isolates |
|---|---|---|
| **`render`** | transform list → `.hew` → transform list | The renderer, via IR identity (§13.5). |
| **`diff`** | `old.*` + `new.*` → `expected.hew` | The differ's determinism and context radius (§9.4). |

### 13.2 Layout

```
tests/hewcorpus/
  README.md                    how a runner consumes this directory
  json/<case>/
  jsonc/<case>/
  yaml/<case>/
  toml/<case>/
  hcl/<case>/
  markdown/<case>/
  cli/<case>/
```

One directory per case. The directory name is the case name.

**`markdown/` is severable.** Markdown's place in the implement tier is deferred to the
evaluation in §8.7; no case outside `markdown/` may depend on a Markdown fixture, so dropping
the dialect is the removal of one directory.

### 13.3 Case files

| File | Present when | Contents |
|---|---|---|
| `case.yaml` | always | The manifest (§13.4). |
| `patch.hew` | every seam except `apply-ir` and `diff` | The patch. |
| `transforms.hewt` | `parse`, `apply-ir`, `render` seams | The canonical transform list (§9.6) — expected output of the parser *and* input to the applier. |
| `target.<ext>` | `apply-ir`, `e2e` seams | The input document, byte-exact. |
| `expected.<ext>` | success cases | The expected output, **compared byte for byte**. |
| `expected-ops.json` | optional | The **resolved** RFC 6901 op list (§9.2), for interop pinning. |
| `old.<ext>` / `new.<ext>` | `diff` seam | The two inputs. |
| `expected.hew` | `diff` seam | The differ+renderer's expected output, byte-exact (§9.4-R1). |
| `stdout.txt` / `stderr.txt` | `cli` cases, optional | Expected streams. |

### 13.4 `case.yaml`

```yaml
name: yaml/keyed-array-add
seams: [parse, apply-ir, e2e]
kind: ok                  # ok | error | cli
format: yaml
ops: [OP-16]              # catalog entries this case pins (§11)
why: |
  One-paragraph statement of the rule this case pins, in spec terms, with the
  section number. A case whose `why` does not name a spec rule is a bug.
spec: "§6.2, §11 OP-16"
```

An error case names the seam the failure belongs to, which is itself an assertion — a
`HEW001` that surfaces at the `e2e` seam instead of the `parse` seam means the parser is
deferring work it should have refused:

```yaml
name: yaml/stale-scalar
seams: [e2e]
kind: error
format: yaml
error: HEW010
error_seam: apply-ir
error_path: /server/timeout
patch_line: 6
message_contains: ["stale-target", "30"]
why: A drifted scalar under a `-` margin fails by name and location, never silently.
spec: "§5, §10"
```

```yaml
name: cli/stale-exit-1
seams: [cli]
kind: cli
argv: ["apply", "--in-place", "patch.hew"]
exit: 1
stderr_contains: ["HEW010"]
target_unchanged: true
why: A failed apply leaves the target byte-identical and exits 1, patch(1)-style.
spec: "§10.5, Appendix B"
```

### 13.5 The round-trip identities

Two identities close the four-component architecture (§9). They are stated as normative
conformance requirements, and the corpus carries one instance per implement format.

**RT1 — full round trip.** For any `(old, new)` pair of the same format:

```
apply( parse( render( diff(old, new) ) ), old )  ==  new
```

Byte-for-byte. This is the single strongest statement the standard makes: the notation can
express what the differ finds, the renderer writes it faithfully, the parser reads back what
was written, and the applier reproduces the target exactly. A failure anywhere in the four
components fails RT1, which is why the three-seam decomposition above matters — RT1 tells you
*that* something is wrong, the seams tell you *what*.

**RT2 — notation round trip on the IR.**

```
parse( render( ir ) )  ==  ir
```

Note the direction. `render → parse` is an identity **on the IR**; `parse → render` is *not*
an identity on the text, and the corpus must not assert that it is: Hew comments, hunk
boundaries, chosen anchors and context radius are notation-side authorial choices that the IR
does not carry (§9.6).

Corpus cases: `<format>/roundtrip-basic/` for each of the six implement formats, carrying
`old.*`, `new.*`, `expected.hew`, and `case.yaml` with `seams: [diff, render, parse, apply-ir, e2e]`.

### 13.6 Runner contract

A conformant runner MUST:

1. Copy the case directory to a scratch location (cases with `--in-place` mutate).
2. For each declared seam, run **only** that seam and assert only its output. A runner that
   collapses `parse` and `e2e` into one execution is not conformant — it cannot report which
   component failed, which is the whole point.
3. For byte comparisons, compare **exactly**: no normalization, no trailing-newline tolerance,
   no whitespace folding.
4. For `error` cases: assert the exact `error` code, the exact `error_path`, the declared
   `error_seam`, and all `message_contains` substrings; assert the target is byte-unchanged.
5. For `cli` cases: run the binary with `argv` relative to the case directory; assert exit
   code and stream contents.
6. Report an unknown `seam` or `kind`, or a missing declared file, as a **corpus error**, not
   a skip. Silent skips are how a conformance suite lies.
7. Report the catalog coverage: every `OP-nn` in §11 marked `v0` must appear in at least one
   case's `ops:` list. An uncovered v0 operation is a corpus gap, and the runner names it.

---

## Appendix A — proposed Go API surface

**This appendix is checkpoint-1 material: signatures for human review, not code.** No file in
this branch implements any of it. The surface is derived, in order: operations catalog (§11) →
IR schema (§9.6) → these signatures. Nothing here introduces a concept the catalog does not.

**Package path:** `github.com/ctxloom/ctxloom/internal/hew`, with format bindings at
`internal/hew/hewjson`, `hewjsonc`, `hewyaml`, `hewtoml`, `hewhcl`, `hewmarkdown`, and the CLI at
`cmd/hew`. The core package imports nothing from ctxloom (architecture principle 6: it is
extraction-ready for the standalone repo and the Rust port). Filesystem, git and ledger
integration live in `internal/hew/hewfs` and `cmd/hew`, never in the core.

**The four components map to four entry points**, and the type that connects them is
`TransformList`:

| Component | Entry point | Side |
|---|---|---|
| Parser | `hew.Parse([]byte) (*Patch, error)` + `(*Patch).Transforms()` | notation, format-agnostic |
| Renderer | `hew.Render(TransformList, RenderOptions) ([]byte, error)` | notation, format-agnostic |
| Applier | `hew.Applier` interface, one impl per format | format |
| Differ | `hew.Differ` interface, one impl per format | format |

### A.1 The IR — `TransformList`

```go
package hew

// Version is the .hew notation version; TransformsVersion is the IR serialization version.
const (
    Version           = 1
    TransformsVersion = 1
)

// TransformList is the IR: the boundary between the notation side and the format side,
// the interop surface, and the unit the corpus pins. Its record shape is the union of
// the "IR record" rows of the operations catalog (spec §11).
type TransformList struct {
    Target    string      // target path, as declared by "--- " or "target:"
    Format    FormatID    // "" = infer
    Transform []Transform
}

// MarshalTransforms writes the canonical .hewt serialization (spec §9.6).
func MarshalTransforms(tl TransformList) ([]byte, error)

// UnmarshalTransforms reads a hand-authored or generated .hewt document. This is the
// input path behind `hew apply --transforms`, and the reason moves and copies need no
// notation surface.
func UnmarshalTransforms(src []byte) (TransformList, error)

// Transform is one record of the reduced core (spec §11.10). Five ops, one address,
// and the minimum qualifier set. Sugar (move, exhaustive, comment attachment, ord)
// is desugared by the parser and never reaches this type.
type Transform struct {
    Op    OpKind
    Path  Path  // abstract Hew path; ALL addressing richness lives here
    From  Path  // OpCopy only
    Value Value // OpAdd, OpReplace, OpTest

    // Placement (OP-11 … OP-13). Abstract, never a numeric index: the parser has no
    // target to count against. Mutually exclusive; both zero means append at end.
    Before Path
    After  Path

    // Exactly one of these four selects the assertion mode on OpTest.
    // Value (above) | Absent | Count | NodeKind
    Absent   bool
    Count    *int
    NodeKind *NodeKind

    OnConflict OnConflict // OpAdd only: OP-02 / OP-03 / OP-04
    Anchor     AnchorMode // OP-40 / OP-41, YAML alias policy
    Surface    Surface    // OP-38, TOML placement

    // The two tolerance flags, and there are no others.
    Optional   bool // OP-06
    Idempotent bool // §7.5

    PatchLine int // provenance into the .hew file; emitted, ignored on input
}

type OpKind string

const (
    OpTest    OpKind = "test"
    OpAdd     OpKind = "add"
    OpRemove  OpKind = "remove"
    OpReplace OpKind = "replace"
    OpCopy    OpKind = "copy"
    // There is no OpMove: "move" is accepted on input and normalized to
    // OpCopy + OpRemove (spec §11.10 reduction 1).
)

type OnConflict string

const (
    ConflictFail    OnConflict = "fail"    // default, OP-02
    ConflictKeep    OnConflict = "keep"    // OP-04, defaulting
    ConflictReplace OnConflict = "replace" // OP-03, upsert
)

type AnchorMode string // "", "rewrite", "fork"
type Surface    string // "", "table", "dotted"

// Resolve projects the abstract list onto the RFC 6901 form (spec §9.2) against a
// specific document: key-match segments become indices, placements become indices,
// format-specific qualifiers are consumed. Lossy by design; interop only.
func Resolve(tl TransformList, doc Document) ([]ResolvedOp, error)

type ResolvedOp struct {
    Op         OpKind
    Path       string // RFC 6901 pointer
    From       string
    Value      Value
    Absent     bool
    Exhaustive bool
    Count      *int
}
```

### A.2 Parser — notation → IR

```go
// Parse reads an Hew patch document. It performs NO I/O, opens no target, and knows
// no format mechanics. Its output is fully determined by the patch text.
func Parse(src []byte) (*Patch, error)

type Patch struct{ /* unexported */ }

func (p *Patch) Version() int
func (p *Patch) Files() []*FileSection

type FileSection struct{ /* unexported */ }

func (f *FileSection) Target() string
func (f *FileSection) Format() FormatID
func (f *FileSection) Line() int

// Transforms is the parser's product: the abstract IR for this file section.
// Context lines and "-" lines have already become OpTest records here — loud
// staleness is established at parse time, not at apply time (spec §9.0).
func (f *FileSection) Transforms() TransformList

// Hunks is retained for diagnostics and linting only. The applier never sees it.
func (f *FileSection) Hunks() []*Hunk

type Hunk struct{ /* unexported */ }

func (h *Hunk) Anchor() Path
func (h *Hunk) Line() int
func (h *Hunk) AssertOnly() bool // no +/- lines (§7.4)
func (h *Hunk) Transforms() []Transform
```

### A.3 Renderer — IR → notation

```go
// Render writes a transform list back out as .hew mirror-grammar text.
// Format-agnostic, like the parser. Deterministic: same input, same bytes (§9.4-R1).
func Render(tl TransformList, opt RenderOptions) ([]byte, error)

type RenderOptions struct {
    Context   int  // sibling radius, spec §9.4-R2. Default 1. -1 = all siblings.
    Preamble  bool // emit "hew: 1"; false when appending to an existing document
    Comment   string
}

// RenderErr reports a transform the mirror grammar cannot express (OP-21, OP-22).
// Callers that must round-trip such a list write it with MarshalTransforms instead.
var ErrInexpressible = errors.New("hew: transform not expressible in mirror grammar")
```

### A.4 Applier — IR + target bytes → patched bytes

```go
// Applier is a format binding's apply half. It never sees .hew text, a margin, a
// hunk, or an annotation.
type Applier interface {
    ID() FormatID

    // ParseDocument parses a whole target file. It must refuse a document it cannot
    // fully represent rather than dropping what it did not understand.
    ParseDocument(src []byte) (Document, error)

    // Apply evaluates every OpTest before mutating anything, then performs the
    // remaining transforms in order, and returns the re-serialized bytes.
    // All-or-nothing: on any error the returned bytes are nil.
    Apply(src []byte, tl TransformList) ([]byte, error)

    // Supports reports whether this binding implements a transform's op and
    // qualifiers. An unsupported qualifier must surface as HEW020, never be ignored.
    Supports(t Transform) error
}

// Document is a parsed target, exposed for Resolve and for diagnostics.
type Document interface {
    Resolve(p Path) ([]Node, error) // 0 results is not an error here; the engine names it
    Bytes() ([]byte, error)         // exact input bytes for an unmodified document
}

type Node interface {
    Path() Path
    Kind() NodeKind
    Value() (Value, error)
    Source() []byte // exact bytes, for diagnostics
    Line() int
}

type NodeKind int

const (
    KindMap NodeKind = iota
    KindSeq
    KindScalar
    KindComment
    KindBlock   // HCL block, Markdown block
    KindSection // Markdown section
)
```

### A.5 Differ — two content sources → IR

```go
// Differ is a format binding's diff half (P4). Its inputs are PURE CONTENT: it has
// no notion of a filesystem, a repository, or a revision. Descriptor resolution is a
// CLI concern (A.7) — keeping it out of the core is what keeps the library embeddable
// and the Rust port's dependency list short.
type Differ interface {
    ID() FormatID

    // Diff computes the structural difference. Deterministic (§9.4-R1): the same
    // (old, new, opt) triple yields the same TransformList in every implementation.
    Diff(old, new []byte, opt DiffOptions) (TransformList, error)
}

type DiffOptions struct {
    // KeyFields are the candidate identity fields for keyed-array addressing,
    // tried in order (§9.4-R4). Default: {"name", "id", "key"}.
    KeyFields []string

    // Target is stamped into the produced TransformList; it is a label, not a path
    // the differ reads.
    Target string
}
```

### A.6 Registration and detection

```go
type FormatID string

const (
    FormatJSON     FormatID = "json"
    FormatJSONC    FormatID = "jsonc"
    FormatYAML     FormatID = "yaml"
    FormatTOML     FormatID = "toml"
    FormatHCL      FormatID = "hcl"
    FormatMarkdown FormatID = "markdown"
)

// A Binding is a format's two halves plus its detection rule. A binding may ship an
// Applier without a Differ (v0 ships exactly that: Differ is P4).
type Binding struct {
    Applier Applier
    Differ  Differ // nil until P4
    Detect  DetectRule
}

type DetectRule struct {
    Extensions     []string
    WellKnownNames []string
}

func Register(id FormatID, b Binding)
func Lookup(id FormatID) (Binding, bool)
func DetectFormat(filename string) (FormatID, bool)
```

### A.7 Source resolution — CLI boundary only

```go
package hewsource // cmd/hew's helper; NOT imported by internal/hew

// Resolver turns a descriptor into bytes. The interface is deliberately tiny so the
// core never grows a dependency on it and a test can substitute a map.
type Resolver interface {
    // Resolve accepts: "path/to/file", "-" (stdin), or "REV:path" (git anchor,
    // git's own <tree-ish>:<path> convention). A literal path containing ":" is
    // disambiguated with a leading "./", as in git.
    Resolve(descriptor string) (content []byte, label string, err error)
}

// NewGitResolver resolves REV:path by invoking git plumbing as a SUBPROCESS
// (`git cat-file blob <rev>:<path>`). It never links a git library. If git is not on
// PATH, a descriptor containing ":" is a usage error, never a silent fallback to
// treating it as a filename.
func NewGitResolver(fsys afero.Fs, workdir string, stdin io.Reader) Resolver
```

### A.8 The ctxloom adapter

```go
package hewfs // may import ctxloom

// ApplyFile applies every file section of p under agent.WithFileLock, writing through
// iox.WriteFileAtomicFs, honoring §10.5 atomicity.
func ApplyFile(fsys afero.Fs, root string, p *hew.Patch, opt hew.Options) ([]FileResult, error)

// ApplyTransforms is the same path for a hand-authored or generated .hewt document —
// the `hew apply --transforms` entry point, and the seam the corpus pins as `apply-ir`.
func ApplyTransforms(fsys afero.Fs, root string, tls []hew.TransformList, opt hew.Options) ([]FileResult, error)

type FileResult struct {
    Target  string
    Changed bool
    Written bool
    Ops     []hew.Transform
}

type Options struct {
    DryRun   bool
    Format   FormatID
    MaxHunks int
}
```

### A.9 Errors — attached to the layer that raises them

```go
type Code string

const (
    // Parser layer — raised by Parse/UnmarshalTransforms, before any target is opened.
    CodeParse             Code = "HEW001"
    CodeUnsupportedFormat Code = "HEW021"

    // Resolver / IO layer — raised by hewfs and hewsource.
    CodeTargetParse Code = "HEW002"
    CodeTargetPath  Code = "HEW003"

    // Applier layer — raised while evaluating a transform list against a document.
    CodeStaleTarget      Code = "HEW010"
    CodeAssertionFailed  Code = "HEW011"
    CodeAmbiguousMatch   Code = "HEW012"
    CodeNoMatch          Code = "HEW013"
    CodeAlreadyExists    Code = "HEW014"
    CodeInexpressible    Code = "HEW020"
    CodeConflict         Code = "HEW030"
    CodeAnchorAmbiguity  Code = "HEW040"
    CodeSurfaceAmbiguity Code = "HEW041"
)

// Layer is asserted by the corpus (`error_seam`): a code surfacing from the wrong
// layer means a component is deferring work it should have refused.
type Layer int

const (
    LayerParser Layer = iota
    LayerResolver
    LayerApplier
    LayerDiffer
    LayerRenderer
)

// Error is the one error type Hew returns. Every field is part of the contract the
// corpus asserts on.
type Error struct {
    Code       Code
    Layer      Layer
    Target     string
    Path       Path
    PatchLine  int
    TargetLine int
    Want, Got  string
    Detail     string
}

func (e *Error) Error() string
func AsError(err error) (*Error, bool)
```

### A.10 Corpus runner (test-only)

```go
package hewcorpus // tests/hewcorpus is data; this is the Go runner

type Seam string

const (
    SeamParse   Seam = "parse"    // patch.hew        -> transforms.hewt
    SeamApplyIR Seam = "apply-ir" // transforms.hewt + target -> expected
    SeamE2E     Seam = "e2e"      // patch.hew + target       -> expected
    SeamRender  Seam = "render"   // transforms.hewt -> .hew -> transforms.hewt  (RT2)
    SeamDiff    Seam = "diff"     // old + new      -> expected.hew
    SeamCLI     Seam = "cli"
)

type Case struct {
    Name, Kind, Format string
    Seams              []Seam
    Ops                []string // catalog IDs, e.g. "OP-16"
    Dir                string
    // ...remaining manifest fields per §13.4
}

func Load(root string) ([]Case, error)
func (c Case) RunSeam(t *testing.T, s Seam)

// CoverageReport names every v0 catalog operation with no case (§13.6 rule 7).
func CoverageReport(cases []Case) (uncovered []string)
```

---

## Appendix B — proposed CLI surface

**Shape ruling still open** (P0 left it so): standalone companion binary `hew` (family
precedent: `ltk`, `taskloom`, `harp`; the natural shape for a general-purpose tool and for the
Rust port) versus a `ctxloom patch` subcommand. This appendix specifies the standalone shape;
a subcommand would carry the same verbs and codes. [O1](#open-questions-for-ratification).

### B.1 `hew apply`

```
hew apply [flags] <patch.hew>...
hew apply -                                    read the patch from stdin
hew apply --transforms <file.hewt>...           apply a transform list directly
```

| Flag | Meaning |
|---|---|
| `-t, --target FILE` | Override the patch's `--- ` target. Legal only with exactly one file section. |
| `-i, --in-place` | Write the result back to the target. Default when the patch declares a target and no `-o` is given. |
| `-o, --output FILE` | Write the result here instead. Requires a single file section. `-o -` writes to stdout. |
| `-R, --root DIR` | Resolve target paths under DIR. Default: cwd. |
| `--transforms FILE` | Read a canonical transform list (§9.6) instead of `.hew` notation. **This is the authoring path for moves and copies** (Appendix C) and the only way to reach OP-21/OP-22/OP-07/OP-36. Mutually exclusive with positional `.hew` arguments. Flag name at [O21](#open-questions-for-ratification). |
| `--dry-run` | Do everything including matching; write nothing; exit as if written. |
| `--ops` | Print the **resolved** RFC 6901 op list (§9.2) to stdout and write nothing. |
| `--transforms-out FILE` | Write the **abstract** transform list (§9.6) and write no target. The parser seam, exposed. |
| `--format FMT` | Override format detection for every target. |
| `--format-out json` | Machine-readable diagnostics and results on stdout. |
| `-q, --quiet` | Suppress the per-file success lines. |

### B.2 `hew diff` (P4)

```
hew diff [flags] <old> <new>
hew diff HEAD:config.yaml config.yaml          the canonical invocation
hew diff HEAD~3:.mcp.json .mcp.json
hew diff old.toml -                            new side from stdin
```

Both arguments are **source descriptors** (§9.5), not file paths: a working-tree path, `-`
for stdin, or a `REV:path` git anchor following git's own `<tree-ish>:<path>` convention. Git
anchors are resolved by invoking git plumbing as a subprocess; the library core has no git
awareness at all.

| Flag | Meaning |
|---|---|
| `-U, --context N` | Sibling context radius (§9.4-R2). Default `1`. `all` emits every sibling. **This is a strictness dial, not a verbosity dial** — context lines compile into assertions (§9.0), so a smaller radius makes the patch *weaker*, and the help text says so. |
| `--format FMT` | Override format detection. Both sides must be the same format. |
| `--key-fields a,b,c` | Candidate identity fields for keyed-array addressing (§9.4-R4). Default `name,id,key`. |
| `--transforms-out FILE` | Emit the transform list instead of `.hew` notation. |
| `-o, --output FILE` | Write the patch here. Default stdout. |

### B.3 Exit codes

patch(1)-shaped, three states, no more:

| Code | Meaning |
|---|---|
| `0` | Every hunk applied. Files written (or, with `--dry-run`, would be). For `diff`: a patch was produced, and it may be empty. |
| `1` | The patch did not apply: `HEW010`–`HEW041`. **No file was modified** (§10.5). |
| `2` | Trouble: usage error, unreadable patch (`HEW001`), unreadable/unparseable target (`HEW002`/`HEW003`), unresolvable descriptor, `git` absent for a git anchor, unsupported format (`HEW021`), I/O failure. |

Note the deliberate difference from `patch(1)`: exit 1 there means "some hunks failed and
`.rej` files were written". Here it means "nothing happened, and here is why". A caller that
treats nonzero as "unknown state" is being unnecessarily defensive; Hew's contract is that
nonzero means unchanged.

Stdout carries results (`--ops`, `--transforms-out`, `-o -`, `--format-out json`, `hew diff`).
Stderr carries human diagnostics. Never both on stdout.

---

## Appendix C — operations the mirror grammar cannot express (non-normative)

Hew v0 is one human-authoring notation, and one notation cannot express everything. This
appendix names what the mirror grammar cannot say — and, since the transform-list IR is itself
an accepted input (§9.6), **what to write instead**. The contingency costs no new surface,
now or ever: the IR had to be specified for the corpus and for interop regardless.

**Nothing here is a future spec revision.** Everything below is doable today.

### C.1 Node move (OP-21) and rename (OP-07)

Relocating a node while preserving its identity. Shape-mirroring has no notation for "this
node, but over there": the two locations are in two different regions of the document, and a
margin marks a line, not a correspondence between two lines far apart.

**Write instead:**

```yaml
hew-transforms: 1
target: config.yaml
format: yaml
transforms:
  - op: test
    path: /server/host
    value: localhost
  - op: move
    from: /server/host
    path: /network/host
```

`hew apply --transforms move.hewt`. The applier preserves the subtree's source bytes and its
attached comments (§9.6), which is exactly what the mirror-grammar substitute cannot do.

**The mirror-grammar substitute is legal and lossy.** A `-` at the old path and a `+` at the
new path applies fine — it just means delete-and-add. Comments attached to the removed node
are lost, byte-exact formatting is lost, and a large subtree must be restated in full. Hew does
**not** detect the pattern and does not warn: see [O16](#open-questions-for-ratification) for
why that was decided rather than assumed.

**Why the mirror grammar was not extended instead.** The config-patching review inventoried
eight mechanisms across five engines and **not one performs a move**. A move is what a schema
migration does, and ctxloom's schema migrations are Go code with a version gate. Paying for
cross-hunk correspondence syntax in the human notation, to serve an operation the human
notation's users do not perform, is the trade this design declines.

### C.2 Node copy (OP-22, OP-23)

Same reasoning, same remedy: `{op: copy, from: …, path: …}` in a `.hewt`. The mirror-grammar
substitute — restating the value in full under `+` — is a *transcription*, and it drifts the
moment either side changes.

### C.3 Sub-block Markdown editing (OP-46)

Changing one line of a paragraph without restating the paragraph (§8.6). **Rejected outright,
not deferred**: Markdown blocks are the addressing unit and there is no path to a sentence.
The IR cannot express it either. `HEW020`.

### C.4 TOML surface migration (OP-39)

Rewriting `[a.b]` into `a.b = {…}` or vice versa (§8.4 rule 4). Deferred, and **not** reachable
via the IR — Hew edits values at whichever surface exists; restructuring surfaces is a
formatter's job. `HEW020`, and see [O10](#open-questions-for-ratification).

### C.5 Cross-file operations

Moving a key from one file to another, or asserting a relationship between two targets. Each
transform list names one target (§9.6) and each file section is independent (§10.5). This is
the largest of the five and the one most likely to be requested first, since ctxloom's real
job is keeping five engines' configs consistent. Not reachable via the IR today;
[O12](#open-questions-for-ratification) and [O13](#open-questions-for-ratification) are its
prerequisites.

---

## Open questions for ratification

Every fork below was resolved by judgment while drafting. The spec states a decision for each;
these are the ones a reviewer should overturn deliberately rather than discover later.

| # | Question | What the draft does | Why it might be wrong |
|---|---|---|---|
| **O1** | Standalone `hew` binary vs `ctxloom patch` subcommand. | Appendix B specifies standalone. | P0 left it explicitly open. Standalone means a fourth companion binary to build, sign, and install; the ctxloom subcommand is free but ties a general-purpose standard to one host. |
| **O2** | Doc filename convention. | `docs/design/hew-spec.md`, per the brief. | The directory's existing convention is `<name>.design.md`; `docs/` also has `signature-envelope.spec.md`. Once ratified this is a *spec*, not a design, so `docs/hew.spec.md` may be the right home. |
| **O3** | Is re-applying a patch an error? | Yes by default (`HEW010`); `! idempotent` (§7.5) opts out per hunk. | Every ctxloom managed-file writer is idempotent by construction, so `! idempotent` may end up on ~every hunk ctxloom itself writes — an argument for inverting the default, or for a file-level `idempotent: true` preamble key. |
| **O4** | JSONC-by-well-known-name. | §8.0 carries a name list (`settings.json`, `tsconfig.json`, `.mcp.json`, …). | A hard-coded name list in a *standard* ages badly. Alternatives: always require `format=` for `.json`, or default `.json` to JSONC-tolerant reading and only emit comments when asked. |
| **O5** | Extending RFC 6901's escape set with `~2` for `=`. | Adopted (§4.1). | It makes Hew paths not-quite-JSON-Pointers even when they look like one. Alternative: require quoting the whole segment (`/"a=b"`), reusing the label-segment syntax. |
| **O6** | Key-match comparison operators. | Equality only (§4.2). | go-patch has only equality too, but real config has compound identity. Regex or multi-field match (`name=x,scope=y`) may be needed sooner than v1. |
| **O7** | Markdown kind-scoped block ordinals (`code:0`) in a path. | Allowed (§4.5). | It is the only number in the address grammar that is not an array index, and it is positional — the exact fragility `! match ord=` was made an annotation to keep *out* of paths. Alternative: make Markdown block selection an annotation too (`! match kind=code ord=0`), which would be more consistent and more verbose. |
| **O8** | `! match ord=` on an unambiguous path. | `HEW001 parse-error` (§7.2). | Strict, and it means a patch breaks when the file *stops* being ambiguous (someone deletes the duplicate block). The alternative — tolerate a redundant ordinal — means an ordinal can silently become meaningless. |
| **O9** | Unknown preamble keys and unknown `!` directives. | Hard failure `HEW001`. | Fail-loud is this project's discipline, but it means Hew v1 readers reject every v1.1 patch, so the version field carries the whole forward-compat burden. |
| **O10** | TOML surface migration (`[a.b]` → `a.b = {…}`). | Not a patch operation (`HEW020`, §8.4 rule 4, Appendix C.4). | It is a real thing people want to do, and refusing it pushes them to a text editor — where they will drift. |
| **O11** | Collect-all-failures mode. | No: first error wins (§10.4). | A patch with six stale hunks reports one, and the author fixes them one round-trip at a time. A `--all-errors` diagnostic mode would not weaken the apply contract. |
| **O12** | Multi-file atomicity. | Per-file atomic, not cross-file (§10.5). | A patch touching `.mcp.json` and `settings.json` can half-apply. Cross-file atomicity is implementable (stage all, then rename all) and this project's config writers are exactly the case that wants it. |
| **O13** | Should `--- ` target lines support globs or multiple paths? | No — one literal path per section. | ctxloom's real use ("apply this to every engine's MCP registry") would want it. Deliberately deferred to keep v0's target model trivial. |
| **O14** | Where does the ledger meet Hew? | Nowhere in v0: the spec has no ownership-record concept. | `config-write`'s missing ownership record (review B2, `distinct-bullpen`) is the mechanism Hew is meant to serve. An Hew patch is a natural *record* of what ctxloom added — an `hew revert` from the same file could be the withdrawal story the ledger currently provides. That may deserve a spec section rather than an adapter. |
| **O15** | Corpus location. | `tests/hewcorpus/`, sibling to `tests/acceptance` etc. | The corpus is meant to be consumed by a *different repository* (the Rust port). A top-level `hewcorpus/` or a dedicated repo may be right; `tests/` implies Go-test-only ownership. |
| **O16** | **Is delete-and-add acceptable semantics for a move?** | Yes — the spec does not detect the pattern, does not error on it, and Appendix C.1 records it as the substitute. | The alternative was to detect "a `-` and a `+` of an equal subtree at different paths" and raise `HEW020 inexpressible` pointing at Appendix C. That was rejected because the detection is a heuristic (how equal is equal? does a one-key change break it?), because a false positive would block a legitimate delete-and-add, and because Hew has no way to know whether the author *meant* a move. **The cost is real and named:** comments attached to the removed node are lost, byte-exact formatting of the moved subtree is lost, and a large subtree must be restated in full. If a reviewer wants moves detected rather than silently degraded, that is a deliberate reversal and Appendix C.1 changes with it. |
| **O18** | Differ identity-field candidates (`name`, `id`, `key`). | Hardcoded default list, `--key-fields` override (§9.4-R4). | A hardcoded list in a *standard* is the same aging problem as O4's JSONC name list. Alternatives: no default (indices unless told), or a per-format default (HCL has labels, TOML has `[[x]]` keys). |
| **O19** | Default context radius = 1 sibling. | §9.4-R2. | Unified diff chose 3 lines. One sibling is the smallest radius that still pins insertion position (§6.2). But because context compiles into assertions (§9.0), the default is also the default *strictness*, and a stricter default (3, or `all`) may be the right bias for a format built around loud staleness. |
| **O20** | Git anchors via subprocess, not a linked library. | §9.5, Appendix A.7. | Ruled by the human. Recorded here because it has a real cost: `hew` in a container without `git` silently loses `REV:path` support, and the error is a usage error at the CLI rather than a capability probe. |
| **O21** | Name and extension of the serialized IR: `.hewt`, `hew apply --transforms`. | §9.6, Appendix B.1. | Alternatives: `.hew.yaml` (signals "this is YAML"), `.spops`, or `hew apply --ir`. Also: should `hew apply` sniff the input and accept either form on the positional argument, rather than requiring a flag? |
| **O22** | File-level effects derived from node-level results (OP-48, OP-49). | Deferred; not in the IR. | ctxloom's M2 genuinely does this: it *removes the file* when nothing user-authored remains, and never creates an absent one. If Hew is to replace M2, either the IR grows file-level effects or the adapter keeps owning them — and the second answer means Hew does not actually replace M2. |
| **O23** | Comment attachment on added nodes (OP-30, OP-31). | **Yes** — a patch can carry a comment for the node it adds, and `HEW020` for JSON which has no comments. | No surveyed patch format can do this, so there is no prior art to copy and no compatibility argument either way. The cost is that every applier must implement comment attachment, which is the hardest part of `yaml.v3` node surgery. The alternative is comments-as-context-only (readable, unwritable). |
| **O24** | Catalog completeness. | 52 entries; §11.9 claims exhaustiveness over the surveyed systems. | The claim is only as good as the survey, and the survey itself records two gaps: no TOML notation candidate existed in any surveyed tool, and no format-prevalence census was obtainable. If a reviewer knows of a verb in a system not in §11.1's table, that is the bug to report. |
| **O17** | Should `! optional` exist at all? | Yes, with a linter warning (§7.6). | It is the one construct in Hew that reintroduces the silent no-op the format was built to eliminate. The argument for it is real files with genuinely conditional content; the argument against is that every escape hatch is used more than its designer expects. |
| **O25** | An HCL ordinal with no distinguishing assert available (genuinely identical sibling blocks). | The ordinal stands alone and the patch can silently target the wrong block (§6.4.3 rule 3). | The alternative is to refuse such a patch outright (`HEW001`), which is consistent with the rest of the format but leaves a legal HCL file unpatchable by Hew at all. |
| **O26** | `replace` vs `add`+`on_conflict: replace` overlap. | Both kept: they differ in precondition, not effect (§11.10). | A reviewer could call this the one surviving redundancy in an IR whose stated goal is zero duplication. Removing `replace` would make the most common operation a two-record composition. |
| **O27** | Two tolerance flags (`optional`, `idempotent`) rather than one. | Both kept — they tolerate different conditions (absent node vs already-applied state). | A single `tolerate: absent\|applied\|both` field would be tighter ASM at the cost of a less obvious spelling. |
| **O28** | Comment addresses (`/x/#0`, `/x/timeout/#t`). | Introduced (§4.5b) so that comment attachment could desugar. | Kind-scoped ordinals on comments are positional and drift when a comment is inserted earlier — the exact fragility §6.4 warns about, now present in a corner of the address grammar. Alternative: comments addressable only relative to the member they attach to, giving up standalone comment editing. |
| **O29** | **Is Markdown in the implement tier at all?** | Kept in the draft as designed, with its fate deferred to the §8.7 evaluation. | Ruled open by the human: Markdown may be better served by plain `patch(1)`. The tolerance model's asymmetry is the crux — reorder-blindness is Hew's largest win over `patch(1)` and it is worth nothing for prose, where order *is* the content. Three outcomes are on the table (keep / drop to documented-only / narrow to managed-marker regions only), and the third is the one to beat. |

### Deliberately not specified in v0

- **The diff algorithm itself.** `hew diff` is P4. §9.4 specifies its *requirements*
  (determinism, context radius, address preference) because the IR depends on them; it does
  not specify the sequence-diff implementation beyond naming Myers.
- **Merge / conflict resolution** between two Hew patches.
- **Signing.** Hew files are content like any other; whether they ride ctxloom's signature
  envelope is a P5 question.
- **Encoding other than UTF-8**, BOM handling, and CRLF. (CRLF targets: a binding must
  preserve the target's line ending; the `.hew` file itself is LF. Not corpus-covered in v0.)
- **Performance and streaming.** Every backend is assumed to load the whole target.
- **A second human-authoring notation.** The v0 ruling is one grammar. The transform list
  (§9.6) is an accepted *input*, but it is machine-first by design and a `.hewt` file where a
  `.hew` file would do is a review-quality regression.
- **Reverting an applied patch** (`hew revert`). Sketched only in [O14](#open-questions-for-ratification).
