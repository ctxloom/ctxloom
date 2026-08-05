# allowedsigners

A Go implementation of OpenSSH's "allowed signers" file format: parsing,
pattern matching, and trust queries. It answers one question — given a public
key, is it authorized to make a signed assertion in a given namespace? — and
optionally, whether a specific claimed identity (a "principal") is
corroborated by the matching entry.

This package has no dependency on ctxloom beyond `golang.org/x/crypto/ssh`,
which it uses for the underlying SSH key/authorized_keys tokenizer. It does
not read files from any well-known path, does not implement any
application-level trust policy (precedence between multiple trust roots,
embedded-vs-user-vs-project layering), and exposes no CLI.

## What this is

The "allowed signers" format is defined by OpenSSH's `ssh-keygen(1)`, in the
section titled ALLOWED SIGNERS, as the trust root consulted by
`ssh-keygen -Y verify` and related `-Y` subcommands (`find-principals`,
`match-principals`, `check-novalidate`). It is not an OpenSSH-only concern:
Git reads the identical file format via the `gpg.ssh.allowedSignersFile`
configuration option to verify SSH-signed commits and tags. A file this
package can parse is, by design, a file both tools already understand — this
package does not invent any part of the format.

Every line has the shape:

```
principals options? keytype base64-key comment?
```

`principals`, `options`, and the `keytype base64-key` pair are
whitespace-separated; `options` (present or absent as a whole) is itself a
single comma-separated token. Blank lines and lines starting with `#` are
comments.

## Spec conformance

The following is implemented as OpenSSH documents it, and is exercised by
this package's own test suite (`*_test.go`) plus real-binary interop tests
(`interop_test.go`, `testdata/interop_allowed_signers`) that were built by
verifying each line's accept/reject outcome against the actual `ssh-keygen`
binary before being committed.

- **Principals field is a pattern-list.** `ssh-keygen(1)` states the
  principals field is "a pattern-list (see PATTERNS in ssh_config(5))
  consisting of one or more comma-separated USER@DOMAIN identity patterns."
  This package splits on `,` and matches each element as a glob pattern
  (`matchPatternList`, `Entry.MatchesPrincipal`). **VERIFIED** by reading the
  man page text directly on this host.
- **The glob dialect is restricted.** `ssh_config(5)` PATTERNS defines a
  pattern as "zero or more non-whitespace characters, `*` ... or `?`" — no
  character classes, no brace expansion, nothing else. `globMatchBytes`
  implements exactly `*` (zero or more characters) and `?` (exactly one
  character); every other byte matches itself literally. **VERIFIED** by
  reading the man page text directly on this host, and cross-checked against
  `TestGlobMatchBytes_DialectEdges`.
- **Negation, including the "negation alone never matches" rule.**
  `ssh_config(5)` PATTERNS: patterns in a pattern-list may be prefixed with
  `!`, and "a negated match will never produce a positive result by itself" —
  a list of only negated patterns can never match anything; a non-negated
  pattern (often a bare `*`) must be present to produce a positive result at
  all. `matchPatternList` implements this: a negated pattern that matches
  makes the whole list refuse immediately, but only a *non-negated* match
  sets the list's own "matched" state. **VERIFIED** by reading the man page
  text directly on this host, and by `TestMatchPatternList_Negation_ExcludesEvenIfOtherPatternMatches`.
- **The options grammar**, one comma-separated token, case-insensitive
  keywords:
  - `cert-authority` — the key is a certificate authority. This package
    recognizes the option (`Entry.CertAuthority`) but implements no SSH
    certificate verification, so it never grants trust directly through a
    cert-authority-flagged entry — there is no certificate for it to
    validate. **VERIFIED**: a cert-authority-flagged entry refuses to verify
    a plain signature against real `ssh-keygen` even when key, namespace, and
    validity window otherwise match.
  - `namespaces=namespace-list` — a quoted pattern-list of namespaces the key
    is accepted for, same pattern-list rules as the principals field.
    Absent means unrestricted; present-but-empty (`namespaces=""`) means
    accepted for no namespace at all. **VERIFIED** against real `ssh-keygen`
    for both the absent and empty cases.
  - `valid-after=timestamp` / `valid-before=timestamp` — `YYYYMMDD[Z]` or
    `YYYYMMDDHHMM[SS][Z]`, local time unless a trailing `Z` forces UTC.
    **VERIFIED** against real `ssh-keygen -Overify-time=...` with and without
    a trailing `Z`.
  - An unrecognized option, or a key=value option whose value is not
    double-quoted, invalidates the *whole entry* — OpenSSH does not ignore
    unknown options for forward compatibility, and neither does this
    package. **VERIFIED**: real `ssh-keygen` reports "bad options: unknown
    key option" / "bad options: missing start quote" for these respectively.

## Divergences and undocumented behavior

This is the section worth reading before depending on this package at a
trust boundary: places where real `ssh-keygen`'s *actual* behavior differs
from — or simply is not addressed by — the man page's prose. All
"VERIFIED" claims below were measured against `ssh-keygen` `OpenSSH_10.0p2`
(Debian packaging `1:10.0p1-7`) on the host this package was developed on,
using `ssh-keygen -Y match-principals`, `-Y find-principals`, and `-Y verify`
against locally generated test keys and locally generated allowed_signers
fixtures — not assumed from the specification text.

### Principals-field quoting is implementation compatibility, not spec conformance

`ssh-keygen(1)` states, of the **options** field specifically: "No spaces
are permitted, except within double quotes." It says nothing about quoting
anywhere in its description of the **principals** field. Read literally, the
man page documents no quoting mechanism for principals at all.

**VERIFIED, nonetheless:** real `ssh-keygen` accepts a double-quoted
principals field and reads it correctly. A line like

```
"alice@x.com,bob@x.com" namespaces="publish.example.org" ssh-ed25519 AAAA...
```

verifies for both `alice@x.com` and `bob@x.com` (`ssh-keygen -Y verify -I
alice@x.com` / `-I bob@x.com`, both exit 0). This is because OpenSSH
tokenizes the principals field with a general-purpose, quote-aware
delimiter function (`strdelim_internal` in OpenSSH's `misc.c`, called as
`strdelimw` for this field) that OpenSSH also uses elsewhere for
whitespace-and-quote-delimited configuration tokens generally — the
quoting behavior falls out of reusing that general tokenizer, not from a
documented feature specific to the principals field. (The options+key tail
that follows is tokenized by a *different* function,
`sshkey_advance_past_options` in `authfile.c`, which does support a
backslash-escaped `"` — see the "no backslash-escape" point below for why
that distinction matters and is easy to get wrong.)

**Consequence for a consumer of this package:** support for quoted
principals is *implementation compatibility with what the real `ssh-keygen`
binary actually accepts*, verified empirically, not a guarantee derived from
the published specification. A future OpenSSH release that reimplements
this differently would not be violating any documented contract if it
changed this behavior. This package tracks the observed binary, and — per
the trust invariant below — errs toward refusing anything genuinely
ambiguous rather than guessing.

The quoting rule itself, as measured:

- A `"` toggles a quoted region; whitespace inside it is not a field
  delimiter.
- There is **no backslash-escape for `"` inside this field.** This is
  different from the options-value grammar (`namespaces="a\"b"` — backslash
  IS special there). **VERIFIED**: `"ali\"ce@x.com"` does not read as the
  principal `ali"ce@x.com`; real `ssh-keygen -Y find-principals` reports
  "invalid options" for a line containing it, because the backslash is
  ordinary and the following `"` closes the quoted region early, leaving a
  stray fragment that fails to parse as the key/options tail.
- An unterminated quote is refused outright by real `ssh-keygen` (misc.c's
  tokenizer returns "no matching quote", which the caller reports as
  "invalid line"). This package refuses it too (a dedicated parse error),
  and deliberately does **not** fall back to treating the still-quoted text
  as if it had never been quoted — see the trust invariant section for why.

### A literal comma in one principal is inescapable

**VERIFIED:** there is no way to make a single principal contain a literal
comma, quoted or not. `ssh_config(5)` PATTERNS defines the pattern-list
separator as a bare `,` with no escape, and this is confirmed by OpenSSH's
own `match_pattern_list` (in `match.c`), which splits on `,` with no
backslash handling at all. Empirically, a backslash-escaped comma inside
quotes — `"alice\,bob@x.com"` — still reads back as **two** principals
(`alice\` and `bob@x.com`, the backslash surviving literally into the
first), never one principal containing a comma.

This is a genuine limitation of the file format itself, not a policy this
package or its writer chose. There is no well-formed allowed_signers line,
produced by any tool, that carries a comma inside one principal.

### Write side is stricter than the format requires — and that is a policy choice, not a format limit

This package's writer (`FormatEntry` / `validPrincipal` in `write.go`)
refuses to emit a principal containing **either** whitespace or a comma.
Only one of those two restrictions is forced by the format:

- **Whitespace CAN be expressed** by this format — quoting the principals
  field protects it, exactly as measured above (`"alice smith@x.com"` reads
  back as the single principal `alice smith@x.com`, **VERIFIED** against
  real `ssh-keygen -Y match-principals`). The writer's refusal to *emit* a
  whitespace-containing principal is a decision by this package's writer
  (which never quotes what it writes today), not something the format
  cannot represent.
- **A comma CANNOT be expressed**, as established above, quoted or not. The
  writer's refusal here mirrors an actual limit of the format.

A future maintainer changing `validPrincipal`'s doc or behavior should keep
these two facts in separate sentences: "this format cannot express X" and
"this package chooses not to write X" are different claims, and conflating
them (as `validPrincipal`'s comment currently does, at the time of writing)
invites either loosening the wrong one or over-justifying the other.

### This fix introduces a read/write asymmetry

Before this change, this package's reader and writer agreed (barely) that
neither handled quoting: the writer never emitted quotes, and the reader had
no quote awareness, so it silently mis-split any quoted field it happened to
encounter — which was the vulnerability this quote-awareness fixes (see the
trust invariant section).

After this change, that symmetry is gone. **The reader now correctly parses
a quoted principal containing whitespace, which the writer still refuses to
produce.** A hand-authored or third-party-tooling-authored allowed_signers
file containing `"alice smith@x.com" ssh-ed25519 ...` parses correctly and
grants trust to `alice smith@x.com`; `ctxloom signer add "alice
smith@x.com"` (or an equivalent call to `FormatEntry`) refuses to write that
same principal. This is intentional and was a deliberate decision at the
time this asymmetry was introduced, not an oversight: the reader's job is to
interoperate with whatever `ssh-keygen` itself accepts (files this package
did not write), while the writer's job is conservative production of files
this package's own users will later need to revoke by exact string match.
Nothing requires those two policies to be the same, and here they are not.

### Fail-closed UTF-8 BOM handling

A UTF-8 byte-order mark (`﻿`) is not Unicode whitespace, so if it were
left untouched a leading BOM would be silently absorbed into the first
principal's name. `classifyLine` detects a leading BOM and reports a parse
error for that line instead — the line contributes no entry — rather than
either stripping the BOM (which would make this package match a principal
real `ssh-keygen` does not) or leaving it in place (which would grant trust
under an identity nobody can type or revoke, since `signer remove` compares
principals literally). **VERIFIED** as consistent with real `ssh-keygen`'s
observed principal-matching behavior on a BOM-prefixed line.

### Line-level tolerance: a malformed line is skipped, not fatal to the file

> Note on wording: "tolerant" below means tolerant of *malformed input*, and
> never more trusting. A skipped line grants nothing. The change described
> here removed a place where this package was *more restrictive* than
> `ssh-keygen`; it did not make it more permissive **than `ssh-keygen`**,
> which is the only comparison the trust invariant is about.

`Parse` never fails outright for malformed content: a line that cannot be
turned into an entry is skipped and reported, and the rest of the file still
counts. **VERIFIED** as matching real `ssh-keygen`, which tolerates a
garbage line mixed into an otherwise-good file and keeps using the rest.

One boundary case went the other way at one point in this package's history
and is worth naming as a still-relevant divergence risk: an allowed_signers
file with an extremely long first line (over 1 MiB) used to make this
package discard the *entire* file/location as an I/O-shaped error, while
real `ssh-keygen` (**VERIFIED**, a 2,000,004-byte garbage first line) simply
uses the rest of the file and verifies a good entry on line 2. This package
now reports only the over-long line itself as unusable and keeps parsing —
matching the real binary rather than being more disruptive than it is.

### The declared key type is checked against the key blob

`golang.org/x/crypto/ssh`'s `ParseAuthorizedKey` base64-decodes the key
field without ever reading the key-type token that precedes it, so `ssh-rsa
<an-ed25519-blob>` parses there as a perfectly good ed25519 key. **VERIFIED**
that real `ssh-keygen` calls this "invalid key" and refuses to verify
against it. This package checks the declared token against the blob's
actual type and rejects a mismatch — one of the two divergences in this
package's history known to have gone in the *more-trust* direction before
being fixed (the other is the quoted-principals bug described under "The
trust invariant" below). Both stay covered by dedicated tests for exactly
that reason: this is the direction of error the invariant exists to catch,
and it has been reached twice.

## The trust invariant

This package's central, tested promise: **every divergence from real
`ssh-keygen` yields strictly less trust, never more.** A caller may rely on
that whenever this package's behavior does not exactly match the man page or
the observed binary: if this package differs from `ssh-keygen`, it differs
by refusing something `ssh-keygen` would accept, never the reverse.

This invariant was **violated** by the bug this quote-handling code fixes.
Before it: a quoted principals field like `"alice@x.com,bob@x.com" ...`
parsed cleanly (no error at all) into two principals literally containing
stray quote characters (`"alice@x.com` and `bob@x.com"`) — strings no
operator could type or match against. Because the trust decision for "is
this key trusted for this namespace" (`Store.TrustedForNamespace`) matches
on the **key**, not the principal, the entry still granted trust — under an
identity that could never be named by `Store.TrustedAs`, and could never be
revoked by a caller that removes entries by literal principal string match.
That is *more* trust than a correct reading (and than real `ssh-keygen`
itself) grants, for the exact same file. The fix in this package restores
the invariant for this input: the same line now parses into the same two
principals real `ssh-keygen` verifies, matchable and revocable by exact
string, and an unparseable variant (e.g. an unterminated quote) is refused
outright rather than silently misread. This is exercised by tests that
assert on the trust *decision*, not merely on the absence of a parse error.

## Do not substitute

Two library substitutions look tempting for pieces of this package and are
both wrong, for the same underlying reason: they implement a *different*,
more permissive grammar, and substituting either would move behavior in the
wrong direction for a trust boundary.

- **Do not replace the principals-field quote handling with a POSIX
  shell-word-splitting library** (e.g. `kballard/go-shellquote`,
  `google/shlex`, `mvdan.cc/sh`). Shell quoting supports single quotes and a
  much richer backslash-escape alphabet than OpenSSH's `strdelim_internal`
  does for this field (which, as measured above, has *no* backslash-escape
  for `"` at all). A shell-word-splitting library would therefore **accept
  strings real `ssh-keygen` rejects**, and **split apart strings real
  `ssh-keygen` keeps whole** (or vice versa) — on a trust boundary, in the
  permissive direction, which is exactly what this package's trust invariant
  forbids.
- **Do not replace `globMatchBytes` with `path.Match`, `filepath.Match`, or a
  doublestar-style library** for principals/namespaces matching. Those
  implement a *different* glob dialect that includes character classes
  (`[...]`) and, in some libraries, `**`. `ssh_config(5)` PATTERNS defines
  only `*` and `?` with no character classes at all; a library that adds
  character-class support would silently accept a pattern operators write
  expecting literal bracket characters, or match differently than
  `ssh-keygen` does for the same pattern, changing what a trust-root entry
  actually matches.

Both of the restricted-grammar-by-hand choices above were made deliberately,
not from an aversion to dependencies in general — no third-party Go library
was found that implements OpenSSH's allowed_signers principals-field
tokenizer or its restricted glob dialect (surveyed: `hiddeco/sshsig`,
`42wim/sshsig`, and `pault.ag/go/sshsig` all cover only the detached
signature blob format, not the trust-root file; `golang.org/x/crypto/ssh`
exports `ParseAuthorizedKey`/`ParseKnownHosts` but nothing for this field,
and its own internal quote-aware tokenizer is unexported).
