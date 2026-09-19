package allowedsigners

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// FormatEntry renders e as one allowed_signers line, in the exact OpenSSH
// ALLOWED SIGNERS format `ssh-keygen -Y verify -f allowed_signers` (and this
// package's own Parse) reads. It is the write half of the format Parse
// already reads — `ctxloom signer add` is the only production caller, and
// TestFormatEntry_RoundTripsThroughParse verifies Parse(FormatEntry(e))
// reproduces e's trust-relevant fields.
//
// FormatEntry only ever emits the namespaces= option among the pattern-list
// options (valid-after=/valid-before=/cert-authority are supported for
// completeness and round-trip correctly, but no ctxloom CLI surface sets
// them yet — see the signer-CLI deferred-items list).
func FormatEntry(e Entry) (string, error) {
	if err := validEntry(e); err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(strings.Join(e.Principals, ","))
	if opts := formatOptions(e); opts != "" {
		b.WriteString(" ")
		b.WriteString(opts)
	}

	keyType := e.KeyType
	if keyType == "" {
		keyType = e.PublicKey.Type()
	}
	fmt.Fprintf(&b, " %s %s", keyType, base64.StdEncoding.EncodeToString(e.PublicKey.Marshal()))

	if e.Comment != "" {
		b.WriteString(" ")
		b.WriteString(e.Comment)
	}
	return b.String(), nil
}

// validEntry refuses everything this format cannot carry, BEFORE a single byte
// is written. Validating here rather than at the CLI is deliberate: FormatEntry
// is the only way an allowed_signers line is produced, so this cannot be
// bypassed by a second caller that forgets.
func validEntry(e Entry) error {
	if len(e.Principals) == 0 {
		return fmt.Errorf("allowed_signers entry needs at least one principal")
	}
	if e.PublicKey == nil {
		return fmt.Errorf("allowed_signers entry needs a public key")
	}
	for _, p := range e.Principals {
		if err := validPrincipal(p); err != nil {
			return err
		}
	}
	for _, ns := range e.Namespaces {
		if err := validNamespace(ns); err != nil {
			return err
		}
	}
	return validComment(e.Comment)
}

// formatOptions renders e's options as the single COMMA-separated token the
// authorized_keys tail format expects, or "" when there are none.
//
// One token with no internal spaces: space-separating them, as an
// authorized_keys "options keytype" line might suggest, parses as garbage —
// verified against real ssh-keygen (interop_test.go) and covered by
// TestFormatEntry_RoundTripsThroughParse.
func formatOptions(e Entry) string {
	var opts []string
	if e.CertAuthority {
		opts = append(opts, "cert-authority")
	}
	if e.Namespaces != nil {
		opts = append(opts, "namespaces="+quoteOptionValue(strings.Join(e.Namespaces, ",")))
	}
	if e.ValidAfter != nil {
		opts = append(opts, "valid-after="+quoteOptionValue(formatTimestamp(*e.ValidAfter)))
	}
	if e.ValidBefore != nil {
		opts = append(opts, "valid-before="+quoteOptionValue(formatTimestamp(*e.ValidBefore)))
	}
	return strings.Join(opts, ",")
}

// validPrincipal rejects a principal this format cannot carry.
//
// The principals field is ONE whitespace-delimited token whose members are
// comma-separated, so whitespace inside a principal re-tokenizes the whole
// line (ssh.ParseAuthorizedKey then reads the tail as options and drops the
// entry) and a comma inside one principal silently becomes TWO principals —
// a LARGER grant than the confirmation prompt displayed. Both used to be
// written happily, with `ctxloom signer add` reporting success and a key
// fingerprint for an entry that delivered no trust at all.
//
// Validating here rather than at the CLI is deliberate: FormatEntry is the
// only way an allowed_signers line is produced, so this cannot be bypassed by
// a second caller that forgets.
func validPrincipal(p string) error {
	if p == "" {
		return fmt.Errorf("allowed_signers principal cannot be empty")
	}
	if i := strings.IndexAny(p, " \t\r\n"); i >= 0 {
		return fmt.Errorf("allowed_signers principal %q cannot contain whitespace: the reader would drop the entry, granting no trust at all", p)
	}
	if strings.Contains(p, ",") {
		return fmt.Errorf("allowed_signers principal %q cannot contain a comma: it is the separator, so this would silently grant trust to two principals — pass them separately instead", p)
	}
	return nil
}

// quoteOptionValue renders v as a double-quoted option value using EXACTLY the
// escape alphabet unescapeQuoted decodes: a backslash before a '"' or a '\',
// and nothing else.
//
// It replaces Go's %q, whose alphabet is wider (\n, \t, \x.., \u....). Those
// extra escapes are not part of the allowed_signers grammar and the reader
// passes them through as literal backslash sequences, so `namespaces="a\tb"`
// written for a tab was read back as the five characters a\tb — a different
// namespace than the one written, silently, on both sides. Write and read must
// share one alphabet or the round trip is not one.
func quoteOptionValue(v string) string {
	var b strings.Builder
	b.Grow(len(v) + 2)
	b.WriteByte('"')
	for i := 0; i < len(v); i++ {
		if c := v[i]; c == '"' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(v[i])
	}
	b.WriteByte('"')
	return b.String()
}

// validNamespace rejects one namespace pattern this format cannot carry.
//
// The two rules are the two things the grammar has no escape for, and both
// were previously written happily:
//
//   - a line break. quoteOptionValue's alphabet is \" and \\ and nothing else
//     — that is the grammar's alphabet, not a shortcut — so a newline would be
//     emitted verbatim and split the entry across two lines, exactly the
//     hazard validComment exists to stop for the comment field.
//   - a comma. It is the pattern-list SEPARATOR, so one namespace containing
//     it silently becomes two, and the reader cannot tell the difference —
//     the same widening validPrincipal refuses for the principals field.
func validNamespace(ns string) error {
	if i := strings.IndexAny(ns, "\r\n"); i >= 0 {
		return fmt.Errorf("allowed_signers namespace %q cannot contain a line break: the quoted-value grammar has no escape for one, so it would split the entry across two lines", ns)
	}
	if strings.Contains(ns, ",") {
		return fmt.Errorf("allowed_signers namespace %q cannot contain a comma: it is the pattern-list separator, so this would silently grant trust for two namespaces — pass them separately instead", ns)
	}
	return nil
}

// validComment rejects a comment that would break FormatEntry's contract of
// rendering e as ONE line.
//
// Comment arrives from the unvalidated `signer add --comment` flag and was
// written verbatim, so a newline in it emitted a SECOND allowed_signers line
// — a complete, fully-trusted entry the CLI's confirmation prompt never
// displayed. Parse splits on "\n", so the injected tail parses as real trust.
func validComment(c string) error {
	if i := strings.IndexAny(c, "\r\n"); i >= 0 {
		return fmt.Errorf("allowed_signers comment cannot contain a line break: it would append a second, undisplayed trusted signer")
	}
	return nil
}

// formatTimestamp renders t in the 14-digit UTC form parseTimestamp accepts,
// with the trailing "Z" so it parses back as UTC rather than the parsing
// process's local zone (see parseTimestamp's utc/loc handling) — required
// for the round trip to reproduce the same instant regardless of the
// machine's local timezone.
func formatTimestamp(t time.Time) string {
	return t.UTC().Format("20060102150405") + "Z"
}
