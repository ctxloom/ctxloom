// Package allowedsigners parses the OpenSSH "allowed signers" file format
// (see ssh-keygen(1), ALLOWED SIGNERS — the same file consumed by
// `ssh-keygen -Y verify -f allowed_signers`) and answers exactly one
// question: is key K trusted to make an assertion in namespace N?
//
// This package is the ctxloom signature envelope's role system
// (signature-envelope spec §7, §7.1, §11A.3): the namespaces= option on an
// allowed_signers entry is what stops a key trusted to publish content from
// also being usable to approve or reject it. No third-party Go library
// parses allowed_signers — every sshsig dependency evaluated (hiddeco/sshsig,
// 42wim/sshsig, pault.ag/go/sshsig) handles only the detached signature blob
// format, not the trust-root file — so this package is owned, tested hard,
// and verified line-by-line against the real ssh-keygen binary's own
// ACCEPT/REJECT behavior (see interop_test.go and testdata/).
//
// This package does not verify sshsig signatures (a signature blob's
// cryptographic validity is a separate concern, owned by a different
// package/dependency) and is not wired into any trust decision — it is a
// pure, self-contained building block.
//
// # Fail-closed decisions
//
// Every decision below was chosen deliberately and is exercised by a test;
// none is a silent gap.
//
//   - Malformed lines never grant trust. Parse tolerates a malformed line by
//     skipping it (reported via the returned []*ParseError) rather than
//     aborting the whole file — this matches real ssh-keygen's own
//     behavior, verified. A skipped line contributes no Entry, so it can
//     never be matched by TrustedForNamespace/TrustedAs. The net effect of
//     any parse failure, anywhere in the file, is strictly less trust, never
//     more.
//   - Unknown options invalidate the whole entry. Verified against real
//     ssh-keygen ("bad options: unknown key option"): this package does not
//     adopt a lenient "ignore what I don't recognize" forward-compatibility
//     posture for allowed_signers options, because doing so risks silently
//     dropping a *restriction* a future OpenSSH version adds — an unknown
//     restricting option must never be treated as if it were absent.
//   - key=value options must be double-quoted. Verified against real
//     ssh-keygen: `namespaces=publish.v1.ctxloom.dev` (unquoted) and
//     `valid-after=20200101` (unquoted) are both rejected outright ("bad
//     options: missing start quote"), even though the values contain no
//     character that would otherwise need escaping. This package matches
//     that exactly rather than being more lenient.
//   - Duplicate options invalidate the entry. Verified against real
//     ssh-keygen ("bad options: multiple \"namespaces\" clauses").
//   - cert-authority is recognized but never grants trust directly. This
//     package implements no SSH certificate verification — no certificate
//     is ever presented to it, only plain public keys (per PROTOCOL.sshsig)
//     — so an entry cannot be verified as "signed by this CA". Verified
//     against real ssh-keygen: a cert-authority-flagged entry refuses to
//     verify a plain signature even when the key, namespace, and validity
//     window otherwise match exactly. TrustedForNamespace/TrustedAs skip
//     every cert-authority entry unconditionally; the same key listed a
//     second time on a plain (non-CA) line is unaffected and still grants
//     trust through that line.
//   - namespaces= absent means unrestricted, namespaces="" present-but-empty
//     means trusted for nothing. Both are OpenSSH's own documented/verified
//     semantics, not shortcuts this package takes, and the distinction is
//     represented in Entry.Namespaces as nil (absent) vs. a non-nil,
//     possibly zero-length slice (present).
//   - valid-after/valid-before ARE enforced, against a caller-supplied
//     `now`, never the system clock. An sshsig signature carries no trusted
//     timestamp (PROTOCOL.sshsig has no timestamp field), so "now" can only
//     ever be a verifier's untrusted opinion of the time — exactly
//     `ssh-keygen -Y verify -Overify-time=...`'s own contract, which this
//     package mirrors rather than pretends is stronger than it is. Time is
//     always a parameter (dependency injection), never read internally, so
//     the boundary behavior is deterministically testable.
//   - Principal matching is case-sensitive. Principal identities are opaque
//     strings to this package; folding case on an identity comparison is a
//     well-known way to silently widen a grant, so it is not done.
//   - The principals field is quote-aware, matching OpenSSH's own
//     strdelimw tokenizer for this exact field (see cutPrincipalsField's
//     doc for the full grammar, established by reading OpenSSH's misc.c and
//     verified against the real ssh-keygen binary). A field wrapped in
//     double quotes has its quotes stripped rather than treated as content,
//     and whitespace inside the quotes is not a delimiter. An unterminated
//     quote is a hard ParseError, never a silent fallback to splitting the
//     raw, still-quoted text — that fallback is what let a quoted field
//     grant trust under an unrevocable identity in the first place.
//
// # What this format can and cannot express in one principal
//
// These two constraints look alike — both are refused by this package's
// writer, validPrincipal in write.go — but they are NOT the same kind of
// constraint, and confusing them would misdescribe the format to a reader of
// this package who only sees the writer's refusal:
//
//   - Whitespace in a principal CAN be expressed by this format: quoting the
//     whole principals field protects it, exactly like any other field this
//     package quote-decodes. Verified against real ssh-keygen: `-Y
//     match-principals -I "alice smith@x.com"` against a file containing
//     `"alice smith@x.com" ssh-ed25519 ...` matches it exactly, and Parse
//     now decodes that quoting too. This package's OWN writer still refuses
//     to PRODUCE a principal containing whitespace — see validPrincipal —
//     but that is a writer-side choice (today's writer never quotes the
//     principals field it emits), not a limitation of the file format
//     itself. A hand-authored or third-party-tooling-authored allowed_signers
//     file MAY legitimately contain one, and Parse must read it correctly
//     rather than garbling or refusing it.
//   - A literal comma in a principal CANNOT be expressed by this format, at
//     all, quoted or not: the comma is always the principals-LIST separator,
//     with no escape for it anywhere in the grammar. Verified two ways: (1)
//     OpenSSH's match.c match_pattern_list, which the same principals field
//     is fed through, splits on a bare ',' with no escape handling
//     whatsoever; (2) empirically, with `-Y match-principals` against
//     `"alice\,bob@x.com" ...` — a backslash-escaped comma inside quotes —
//     which still reports TWO principals ("alice\" and "bob@x.com", the
//     backslash surviving literally into the first one's name), never one
//     principal containing a comma. This is a genuine format limitation, so
//     this package's writer refusing it is not a stricter-than-necessary
//     policy call; there is no well-formed file this package could produce
//     or read that carries a comma inside one principal.
//
// # What this package deliberately does NOT do
//
//   - It does not verify any sshsig signature. See the signature-envelope
//     spec §11A for the dependency that owns that (hiddeco/sshsig, over
//     ssh.PublicKey).
//   - It does not read allowed_signers from any well-known path, does not
//     merge the embedded/user/project precedence policy beyond providing
//     Union as a building block, and does not itself decide trust for a
//     signing/review/publish flow. That is deliberate: this package stays a
//     pure, dependency-free parser/matcher. The well-known-path reading and
//     embedded/user/project precedence merge (via Union) live in
//     internal/config.Config.TrustRoot; the resulting Store is the load-
//     bearing trust gate consumed by internal/operations (countersign_records.go,
//     review.go, signer.go) and internal/signing/publisher.go.
//   - It exposes no CLI.
package allowedsigners
