package allowedsigners

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Sentinel causes wrapped by ParseError.Err. Callers that want to
// distinguish a cause programmatically should use errors.Is against
// these rather than matching ParseError.Error()'s text.
var (
	errNoPrincipals    = errors.New("missing or empty principals field")
	errNoKey           = errors.New("no recognizable key type/blob")
	errUnknownOption   = errors.New("unknown or malformed option")
	errDuplicateOption = errors.New("duplicate option")
	errUnquotedValue   = errors.New("option value must be double-quoted")
	errBadTimestamp    = errors.New("invalid valid-after/valid-before timestamp")
	errKeyTypeMismatch = errors.New("declared key type does not match the key blob")
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
// error, not a content error).
func Parse(r io.Reader) (*Store, []*ParseError, error) {
	var entries []Entry
	var perrs []*ParseError

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		entry, err := parseLine(trimmed, lineNo)
		if err != nil {
			perrs = append(perrs, &ParseError{Line: lineNo, Text: raw, Err: err})
			continue
		}
		entries = append(entries, *entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, perrs, err
	}
	return &Store{entries: entries, parseErrors: perrs}, perrs, nil
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
	i := strings.IndexAny(line, " \t")
	if i == -1 {
		return nil, errNoKey
	}
	principalsField := line[:i]
	rest := strings.TrimLeft(line[i+1:], " \t")

	principals, err := splitPrincipals(principalsField)
	if err != nil {
		return nil, err
	}

	pubKey, comment, rawOptions, _, err := ssh.ParseAuthorizedKey([]byte(rest))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoKey, err)
	}
	if declared := declaredKeyType(rest, pubKey); declared != "" && declared != pubKey.Type() {
		return nil, fmt.Errorf("%w: line declares %q but the blob is %s", errKeyTypeMismatch, declared, pubKey.Type())
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

// declaredKeyType returns the key-type token a line literally declares, or ""
// when it cannot be located unambiguously.
//
// golang.org/x/crypto's parseAuthorizedKey base64-decodes the field after the
// first space and NEVER reads the type token, so `ssh-rsa <ed25519 blob>` and
// even `not-a-key-type <ed25519 blob>` both parse there as a perfectly good
// ed25519 key. Real ssh-keygen calls both "invalid key" and refuses to verify
// against them — measured against the binary, not inferred. Granting trust
// from a line ssh-keygen rejects is the one divergence that went the WRONG
// way: this package's doc promises every divergence yields strictly less
// trust, never more.
//
// The token is located by matching the re-encoded blob rather than by field
// position, so options (which may be quoted and contain spaces) cannot
// mislead it. An unlocatable blob (some non-canonical encoding) returns "",
// which skips the check rather than rejecting a line that may be fine.
func declaredKeyType(rest string, pub ssh.PublicKey) string {
	want := base64.StdEncoding.EncodeToString(pub.Marshal())
	fields := strings.Fields(rest)
	for i, f := range fields {
		if f == want && i > 0 {
			return fields[i-1]
		}
	}
	return ""
}

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

		switch lkey {
		case "cert-authority":
			if hasValue {
				return fmt.Errorf("%w: cert-authority takes no value", errUnknownOption)
			}
			entry.CertAuthority = true

		case "namespaces":
			v, err := requireQuotedValue(key, rawValue, hasValue)
			if err != nil {
				return err
			}
			entry.Namespaces = splitPatternList(v)

		case "valid-after":
			v, err := requireQuotedValue(key, rawValue, hasValue)
			if err != nil {
				return err
			}
			t, err := parseTimestamp(v)
			if err != nil {
				return fmt.Errorf("%w: %v", errBadTimestamp, err)
			}
			entry.ValidAfter = &t

		case "valid-before":
			v, err := requireQuotedValue(key, rawValue, hasValue)
			if err != nil {
				return err
			}
			t, err := parseTimestamp(v)
			if err != nil {
				return fmt.Errorf("%w: %v", errBadTimestamp, err)
			}
			entry.ValidBefore = &t

		default:
			// Verified against real ssh-keygen ("bad options: unknown key
			// option"): an unrecognized option invalidates the whole
			// entry. We deliberately match this rather than adopting a
			// more lenient "ignore unknown options" forward-compat
			// posture — see package doc, "Fail-closed decisions".
			return fmt.Errorf("%w: %q", errUnknownOption, key)
		}
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
