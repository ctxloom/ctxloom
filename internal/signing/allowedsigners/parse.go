package allowedsigners

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Sentinel causes wrapped by ParseError.Err, for errors.Is INSIDE this
// package. They are deliberately unexported, and callers outside it get no
// way to tell one cause from another.
//
// That is the contract, not an oversight: every one of these means the same
// thing to a caller — this line contributes no Entry and grants no trust —
// and there is no decision downstream that turns on WHICH of them fired.
// Widening the cause into public API would invite exactly the thing this
// package must not have: a caller branching on a parse failure and doing
// something other than withholding trust.
//
// What a caller may rely on is the pairing: every ParseError names a line and
// carries a cause that renders, and ParseErrors() is the complete list of the
// lines the file lost. Matching ParseError.Error()'s TEXT is not supported —
// it is a diagnostic string, not an interface.
var (
	errNoPrincipals                = errors.New("missing or empty principals field")
	errNoKey                       = errors.New("no recognizable key type/blob")
	errUnknownOption               = errors.New("unknown or malformed option")
	errDuplicateOption             = errors.New("duplicate option")
	errUnquotedValue               = errors.New("option value must be double-quoted")
	errBadTimestamp                = errors.New("invalid valid-after/valid-before timestamp")
	errByteOrderMark               = errors.New("line begins with a UTF-8 byte-order mark, which would become part of the first principal")
	errLineTooLong                 = errors.New("line is longer than the 1 MiB limit and was not read")
	errUnterminatedPrincipalsQuote = errors.New("principals field has an unterminated double quote")
)

// ParseError describes one allowed_signers line that could not be used.
// Malformed lines are never used to grant trust (see the package doc,
// "Fail-closed decisions"); Parse reports them here instead of aborting
// the whole file.
type ParseError struct {
	// Line is the 1-based source line number.
	Line int
	// Text is the raw source line, verbatim, so a user can find and fix it.
	Text string
	// Err is the specific cause; wraps one of the sentinel errors above.
	Err error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("allowedsigners: line %d: %v: %q", e.Line, e.Err, e.Text)
}

func (e *ParseError) Unwrap() error { return e.Err }

// Parse reads an allowed_signers file from r.
//
// Parse never fails outright on malformed content: each line that cannot
// be turned into an Entry is skipped and reported in the returned
// []*ParseError, exactly matching real ssh-keygen's own tolerant,
// line-oriented behavior (verified: a garbage line mixed into a real
// allowed_signers file does not stop ssh-keygen from using the rest of
// the file). A skipped line contributes no Entry, so a malformed line can
// never grant trust — the file overall degrades toward less trust, never
// more, which is the fail-closed property this package guarantees.
//
// Parse returns a non-nil error only when reading r itself fails (an I/O
// error, not a content error). An over-long line is content: it is reported
// as a ParseError like any other unusable line — see maxLineBytes.
func Parse(r io.Reader) (*Store, []*ParseError, error) {
	var entries []Entry
	var perrs []*ParseError

	br := bufio.NewReaderSize(r, readBufBytes)
	for lineNo := 1; ; lineNo++ {
		raw, tooLong, err := readLine(br)
		if err != nil && err != io.EOF {
			return nil, perrs, err
		}
		last := err == io.EOF
		if last && raw == "" && !tooLong {
			break
		}
		entry, perr := classifyLine(raw, lineNo, tooLong)
		switch {
		case perr != nil:
			perrs = append(perrs, perr)
		case entry != nil:
			entries = append(entries, *entry)
		}
		if last {
			break
		}
	}
	return &Store{entries: entries, parseErrors: perrs}, perrs, nil
}

// classifyLine turns one raw line into exactly one of: an Entry, a ParseError,
// or neither (a blank line or a comment). Nothing else is representable, which
// is the fail-closed property stated in Parse's doc — a line the reader cannot
// turn into an Entry contributes none, and says so.
func classifyLine(raw string, lineNo int, tooLong bool) (*Entry, *ParseError) {
	if tooLong {
		// A line this long cannot be an entry, but discarding the FILE over it
		// revokes every signer in it — see maxLineBytes. readLine kept only a
		// bounded prefix; the ellipsis says so, because pe.Error() is rendered
		// to a terminal.
		return nil, &ParseError{Line: lineNo, Text: raw + "…", Err: errLineTooLong}
	}
	// A UTF-8 BOM is NOT Unicode whitespace, so TrimSpace leaves it in place
	// and it is absorbed into the first PRINCIPAL — an entry that still grants
	// trust (TrustedForNamespace matches on the key) under an identity nothing
	// can name: TrustedAs never matches it and `signer remove`, which compares
	// principals literally, cannot revoke it. It is cut here only to classify
	// the line; an entry line carrying one is then reported, never silently
	// repaired, because matching a principal real ssh-keygen would refuse to
	// match is a divergence in the MORE-trust direction (see the package doc).
	body, hasBOM := strings.CutPrefix(raw, "\ufeff")
	trimmed := strings.TrimSpace(body)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return nil, nil
	}
	if hasBOM {
		return nil, &ParseError{Line: lineNo, Text: raw, Err: errByteOrderMark}
	}
	entry, err := parseLine(trimmed, lineNo)
	if err != nil {
		return nil, &ParseError{Line: lineNo, Text: raw, Err: err}
	}
	return entry, nil
}

const (
	// readBufBytes is the read buffer; lines longer than it are assembled
	// across reads up to maxLineBytes.
	readBufBytes = 64 * 1024
	// maxLineBytes bounds one line. Beyond it the line is reported as a
	// ParseError and SKIPPED — not turned into Parse's error return.
	//
	// It used to be bufio.Scanner's token limit, which makes an over-long line
	// an I/O-shaped failure of the whole read: Parse returned a nil *Store and
	// every caller discarded the entire location, so one oversized junk line
	// revoked every signer in the file. Measured against real ssh-keygen
	// (OpenSSH_10.0p2): an allowed_signers whose first line is 2,000,004 bytes
	// of garbage still verifies against the good entry on line 2. The limit
	// stays — an unbounded line is a memory hazard on a file this package is
	// pointed at by configuration — but overrunning it costs that line only.
	maxLineBytes = 1 << 20
	// diagnosticTextBytes bounds ParseError.Text for an over-long line.
	// pe.Error() is rendered to a terminal by `signer list` and carried in the
	// SignerListing.Unreadable field, so the verbatim line would be the
	// diagnostic's own denial of service.
	diagnosticTextBytes = 256
)

// readLine reads one line, without its trailing newline (and without a CRLF's
// '\r', matching bufio.ScanLines, which this replaced).
//
// tooLong reports a line past maxLineBytes: the line is consumed and DISCARDED
// — only its first diagnosticTextBytes are kept, so a pathological line cannot
// be held in memory whole. err is io.EOF on the last line, which may still
// carry content when the file has no final newline.
func readLine(br *bufio.Reader) (line string, tooLong bool, err error) {
	var b []byte
	for {
		chunk, rerr := br.ReadSlice('\n')
		if rerr != nil && rerr != bufio.ErrBufferFull && rerr != io.EOF {
			return "", false, rerr
		}
		b, tooLong = appendBounded(b, chunk, tooLong)
		if rerr == bufio.ErrBufferFull {
			continue
		}
		if rerr == io.EOF {
			err = io.EOF
		}
		return strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r"), tooLong, err
	}
}

// appendBounded accumulates one line, and stops accumulating once it passes
// maxLineBytes: past that point only a diagnosticTextBytes prefix is kept, so a
// pathological line is never held whole.
func appendBounded(b, chunk []byte, tooLong bool) ([]byte, bool) {
	if !tooLong && len(b)+len(chunk) <= maxLineBytes {
		return append(b, chunk...), false
	}
	if !tooLong {
		b = b[:min(len(b), diagnosticTextBytes)]
	}
	if len(b) < diagnosticTextBytes {
		b = append(b, chunk[:min(len(chunk), diagnosticTextBytes-len(b))]...)
	}
	return b, true
}

// ParseFile is a convenience wrapper around Parse for a path on disk.
func ParseFile(path string) (*Store, []*ParseError, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	return Parse(f)
}

// parseLine parses one non-empty, non-comment, whitespace-trimmed line
// into an Entry. The tail of the line — everything after the principals
// field — is exactly the AUTHORIZED_KEYS FILE FORMAT tail (options?,
// keytype, base64 key, comment?), so it is handed to
// ssh.ParseAuthorizedKey, which already implements that tokenizer
// (including its quote-aware option splitting) correctly and is the
// existing, well-tested implementation this package deliberately reuses
// rather than re-deriving.
func parseLine(line string, lineNo int) (*Entry, error) {
	principalsField, rest, err := cutPrincipalsField(line)
	if err != nil {
		return nil, err
	}
	if rest == "" {
		return nil, errNoKey
	}

	principals, err := splitPrincipals(principalsField)
	if err != nil {
		return nil, err
	}

	// ssh.ParseAuthorizedKey verifies the declared key-type token against the
	// type embedded in the blob, as OpenSSH's sshkey_read does, on both its
	// no-options and after-options paths. A line labelled ssh-rsa carrying an
	// ed25519 blob is refused HERE. Do not add a local key-type check beside
	// this call: the rule belongs to the format, and a second copy drifts.
	pubKey, comment, rawOptions, _, err := ssh.ParseAuthorizedKey([]byte(rest))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoKey, err)
	}

	entry := &Entry{
		Principals: principals,
		KeyType:    pubKey.Type(),
		PublicKey:  pubKey,
		Comment:    comment,
		Line:       lineNo,
	}

	if err := applyOptions(entry, rawOptions); err != nil {
		return nil, err
	}
	return entry, nil
}

// cutPrincipalsField splits a trimmed allowed_signers line into its
// principals field and the remainder (the options?/keytype/blob/comment
// tail, still raw), consuming and resolving at most one pair of double
// quotes along the way.
//
// This is a from-scratch reimplementation of OpenSSH's own tokenizer for
// this exact field, NOT the generic ssh_config-style strdelim: ssh-keygen's
// sshsig.c calls parse_principals_key_and_options, which extracts the
// principals field with strdelimw(&cp) — misc.c's
// strdelim_internal(s, /*split_equals=*/0). That function is unrelated to
// POSIX shell quoting (no single quotes, no backslash-escaped spaces) and
// unrelated to this package's OWN option-value quoting a few lines below in
// requireQuotedValue/unescapeQuoted (which delegates to
// ssh.ParseAuthorizedKey's tokenizer, itself modeled on
// sshkey_advance_past_options — a THIRD, backslash-escaping-aware grammar).
// Three different quoting rules coexist in one file format; this function
// implements only the first one, for only this one field.
//
// The grammar, established by reading misc.c:strdelim_internal in the
// OpenSSH 10.0p1 portable release tarball (cite the version: this was read
// from a downloaded source tree, not a checked-in one, so re-verifying means
// fetching that release again) and cross-checked against the real
// `ssh-keygen` binary (OpenSSH_10.0p2, via
// `ssh-keygen -Y match-principals` and `-Y find-principals`, which exercise
// this exact code path without needing a valid signature):
//
//   - VERIFIED. A field with no double quote at all is delimited by the
//     first whitespace, exactly as before this fix — no behavior change for
//     the common case. `alice@example.com,bob@example.com key...` still
//     yields the field "alice@example.com,bob@example.com".
//
//   - VERIFIED. A double quote occurring before any whitespace opens a
//     quoted region; whitespace inside it is NOT a delimiter. The scan for
//     the CLOSING quote looks only for the next `"` — it does not stop at
//     whitespace either, so it can run past intervening spaces to land on a
//     `"` far later in the line. Both quote characters are removed, not
//     content: `"alice@x.com,bob@x.com" namespaces="..."` yields the field
//     `alice@x.com,bob@x.com` (quotes gone, comma intact) and rest starting
//     at `namespaces=...`. This is the fix for the bug this change addresses:
//     ssh-keygen verifies this line for both alice@x.com and bob@x.com, and
//     now so does this package.
//
//   - VERIFIED. Unquoted content may precede the opening quote in the same
//     field, and is kept, concatenated directly onto whatever the quoted
//     region contributes: `ali"ce@x.com"` (quote opens mid-word, closes
//     cleanly right after) yields the single field "alice@x.com" — verified
//     against real ssh-keygen via -Y match-principals. This is not a
//     tolerated edge case so much as an unavoidable consequence of
//     strdelim_internal's memmove-and-rescan mechanics; this package
//     reproduces it faithfully rather than special-casing it away, because
//     special-casing it would itself be a divergence from ssh-keygen.
//
//   - VERIFIED. At most ONE quote pair is resolved per field. strdelimw is
//     called exactly once for the whole principals column, and its quote
//     handling fires at most once per call (it returns as soon as it finds
//     ONE closing quote). A second `"` occurring anywhere in what might look
//     like a comma list of individually-quoted principals — e.g.
//     `"alice@x.com","bob@x.com"` — is NOT treated as opening a second
//     quoted region: the field extraction stops at the FIRST closing quote
//     it finds (right after "alice@x.com"), and everything from the second
//     quote onward (`,"bob@x.com" key...`) is handed on as "rest", where it
//     is essentially never a syntactically valid key/options tail and the
//     whole line is rejected downstream. Measured against real ssh-keygen:
//     `-Y find-principals` on exactly this construction reports
//     "invalid options" and matches no principal. This package reproduces
//     that by construction — it also stops at the first closing quote —
//     rather than by a special check, so it needs no dedicated test to hold.
//
//   - VERIFIED, and the one place this package is DELIBERATELY STRICTER than
//     the observed mechanics above: an opening quote with NO closing quote
//     anywhere in the rest of the line. Real strdelim_internal returns NULL
//     in this case (misc.c: "no matching quote"), which propagates as
//     "invalid line" in parse_principals_key_and_options — so ssh-keygen
//     already treats this as a hard parse failure, not a fallback to
//     unquoted splitting. This package matches that with
//     errUnterminatedPrincipalsQuote and returns before ever comma-splitting
//     anything. Separately, and independently of what real ssh-keygen
//     happens to do: this package would refuse to fall back to a naive split
//     here even if the real tool's behavior were murkier, because doing so
//     is exactly how the bug this function fixes came to exist in the first
//     place (a malformed quoted field silently misparsed instead of refused).
//     Where OpenSSH's own emergent behavior for a malformed field is
//     ambiguous or path-dependent (as in the two points above), a discrepancy
//     that makes ctxloom refuse a line ssh-keygen would technically use is
//     always the acceptable direction — see the package doc's "every
//     divergence yields strictly less trust, never more".
//
//   - VERIFIED. There is NO backslash-escape of `"` inside this field
//     (unlike the separate options-value grammar this package already
//     implements in unescapeQuoted, which DOES support `\"`). A literal
//     `\"` in the principals column is just a backslash character followed
//     by a quote-toggle — `"ali\"ce@x.com"` does not yield the principal
//     `ali"ce@x.com`; verified with `-Y find-principals` against the real
//     binary, it makes the whole line report "invalid options" (the
//     backslash becomes part of the field content, and the SECOND `"`,
//     three characters later, closes the quoted region prematurely,
//     leaving `ce@x.com"` at the front of what should have been the
//     key/options tail — which then fails to parse as either).
//
// A field is therefore rejected outright (errUnterminatedPrincipalsQuote)
// only for a genuinely unterminated quote; every other case above resolves
// to SOME field-and-rest split (possibly one that is nonsense and gets
// rejected two calls later, in ssh.ParseAuthorizedKey), exactly mirroring
// where real ssh-keygen's own rejection actually happens.
func cutPrincipalsField(line string) (field, rest string, err error) {
	i := strings.IndexAny(line, " \t\"")
	if i == -1 {
		// No delimiter of any kind: the whole line is the "field" and there
		// is nothing left over. parseLine's caller treats an empty rest as
		// errNoKey — there is no key/options tail to parse.
		return line, "", nil
	}
	if line[i] != '"' {
		return line[:i], strings.TrimLeft(line[i+1:], " \t"), nil
	}

	prefix := line[:i]
	after := line[i+1:]
	closeRel := strings.IndexByte(after, '"')
	if closeRel == -1 {
		return "", "", errUnterminatedPrincipalsQuote
	}
	field = prefix + after[:closeRel]
	rest = strings.TrimLeft(after[closeRel+1:], " \t")
	return field, rest, nil
}

// splitPrincipals splits an already-extracted, already-dequoted principals
// field (see cutPrincipalsField) on commas into individual principal
// patterns. There is no comma-escaping mechanism in this field: VERIFIED two
// ways. Reading OpenSSH's match.c match_pattern_list, which the extracted
// field is ultimately matched through, shows it splits its pattern-list
// argument on a bare ',' with no escape handling at all. Empirically, `-Y
// match-principals` against a quoted field with a backslash-escaped comma
// (`"alice\,bob@x.com"`) still reports two principals — "alice\" (the
// backslash surviving literally) and "bob@x.com" — never one principal
// containing a comma. So a comma inside what was a quoted segment still
// separates two principals rather than protecting one that contains a
// literal comma; this is exactly what makes the original bug a bug:
// `"alice@x.com,bob@x.com"` is two principals, not one. See the package doc,
// "What this format can and cannot express in one principal", for why this
// is a genuine format limitation rather than a policy choice — unlike
// whitespace, which quoting DOES let this format carry.
func splitPrincipals(field string) ([]string, error) {
	if field == "" {
		return nil, errNoPrincipals
	}
	parts := strings.Split(field, ",")
	for _, p := range parts {
		if p == "" {
			return nil, errNoPrincipals
		}
	}
	return parts, nil
}

// applyOptions parses the raw option tokens ssh.ParseAuthorizedKey
// returned (quotes intact, un-decoded) and populates entry accordingly.
// Every recognized keyword is case-insensitive (verified: NAMESPACES= and
// namespaces= behave identically against real ssh-keygen).
func applyOptions(entry *Entry, rawOptions []string) error {
	seen := make(map[string]bool, len(rawOptions))
	for _, raw := range rawOptions {
		key, rawValue, hasValue := splitOption(raw)
		lkey := strings.ToLower(key)
		if seen[lkey] {
			return fmt.Errorf("%w: %q", errDuplicateOption, key)
		}
		seen[lkey] = true
		if err := applyOption(entry, lkey, key, rawValue, hasValue); err != nil {
			return err
		}
	}
	return nil
}

// applyOption populates entry from ONE recognized option. lkey is the
// lowercased keyword the switch dispatches on; key is the spelling as written,
// which is what an error message must echo back.
func applyOption(entry *Entry, lkey, key, rawValue string, hasValue bool) error {
	switch lkey {
	case "cert-authority":
		if hasValue {
			return fmt.Errorf("%w: cert-authority takes no value", errUnknownOption)
		}
		entry.CertAuthority = true
		return nil

	case "namespaces":
		v, err := requireQuotedValue(key, rawValue, hasValue)
		if err != nil {
			return err
		}
		entry.Namespaces = splitPatternList(v)
		return nil

	case "valid-after", "valid-before":
		return applyValidityBound(entry, lkey, key, rawValue, hasValue)

	default:
		// Verified against real ssh-keygen ("bad options: unknown key
		// option"): an unrecognized option invalidates the whole
		// entry. We deliberately match this rather than adopting a
		// more lenient "ignore unknown options" forward-compat
		// posture — see package doc, "Fail-closed decisions".
		return fmt.Errorf("%w: %q", errUnknownOption, key)
	}
}

// applyValidityBound handles valid-after= and valid-before=, which differ only
// in which field they land on. Sharing one body is what keeps them from
// drifting — a bound parsed one way and rejected the other is a validity
// window nobody wrote.
func applyValidityBound(entry *Entry, lkey, key, rawValue string, hasValue bool) error {
	v, err := requireQuotedValue(key, rawValue, hasValue)
	if err != nil {
		return err
	}
	t, err := parseTimestamp(v)
	if err != nil {
		return fmt.Errorf("%w: %v", errBadTimestamp, err)
	}
	if lkey == "valid-after" {
		entry.ValidAfter = &t
	} else {
		entry.ValidBefore = &t
	}
	return nil
}

func splitOption(raw string) (key, value string, hasValue bool) {
	idx := strings.IndexByte(raw, '=')
	if idx == -1 {
		return raw, "", false
	}
	return raw[:idx], raw[idx+1:], true
}

// requireQuotedValue enforces that a key=value option's value is wrapped
// in double quotes, matching real ssh-keygen exactly: it rejects
// namespaces=foo and valid-after=20200101 with "bad options: missing
// start quote" even though the value contains no character that would
// otherwise need protecting. Requiring the quote is therefore not this
// package's own added strictness — it is the observed, verified contract
// of the format, and getting it wrong in the lenient direction would
// accept files real ssh-keygen refuses to sign off on.
func requireQuotedValue(key, rawValue string, hasValue bool) (string, error) {
	if !hasValue {
		return "", fmt.Errorf("%w: %q needs a value", errUnquotedValue, key)
	}
	if len(rawValue) < 2 || rawValue[0] != '"' || rawValue[len(rawValue)-1] != '"' {
		return "", fmt.Errorf("%w: %q", errUnquotedValue, key)
	}
	// ssh.ParseAuthorizedKey's tokenizer only terminates an option token
	// at an unquoted delimiter, so by construction the quotes here are
	// already balanced (an unmatched quote fails earlier, inside
	// ssh.ParseAuthorizedKey itself, as errNoKey).
	inner := rawValue[1 : len(rawValue)-1]
	return unescapeQuoted(inner), nil
}

// unescapeQuoted processes the backslash escapes permitted inside a
// double-quoted option value: \" -> ", \\ -> \. Any other backslash is
// passed through literally (permissive: there is no legitimate allowed_signers
// value in this package's vocabulary that needs a different escape, and
// erroring on an unrecognized escape would be stricter than the format
// requires without buying any safety — the value is still bounded by the
// already-balanced closing quote either way).
func unescapeQuoted(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
			b.WriteByte(s[i+1])
			i++
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
