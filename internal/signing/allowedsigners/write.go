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
	if len(e.Principals) == 0 {
		return "", fmt.Errorf("allowed_signers entry needs at least one principal")
	}
	if e.PublicKey == nil {
		return "", fmt.Errorf("allowed_signers entry needs a public key")
	}

	var b strings.Builder
	b.WriteString(strings.Join(e.Principals, ","))

	// Options are a single COMMA-separated token with no internal spaces
	// (the authorized_keys tail format ssh.ParseAuthorizedKey tokenizes,
	// which this package's own Parse hands the tail to — see parse.go).
	// Space-separating them, as an authorized_keys "options keytype" line
	// might suggest, parses as garbage: verified against real ssh-keygen
	// (interop_test.go) and covered here by
	// TestFormatEntry_RoundTripsThroughParse.
	var opts []string
	if e.CertAuthority {
		opts = append(opts, "cert-authority")
	}
	if e.Namespaces != nil {
		opts = append(opts, fmt.Sprintf("namespaces=%q", strings.Join(e.Namespaces, ",")))
	}
	if e.ValidAfter != nil {
		opts = append(opts, fmt.Sprintf("valid-after=%q", formatTimestamp(*e.ValidAfter)))
	}
	if e.ValidBefore != nil {
		opts = append(opts, fmt.Sprintf("valid-before=%q", formatTimestamp(*e.ValidBefore)))
	}
	if len(opts) > 0 {
		b.WriteString(" ")
		b.WriteString(strings.Join(opts, ","))
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

// formatTimestamp renders t in the 14-digit UTC form parseTimestamp accepts,
// with the trailing "Z" so it parses back as UTC rather than the parsing
// process's local zone (see parseTimestamp's utc/loc handling) — required
// for the round trip to reproduce the same instant regardless of the
// machine's local timezone.
func formatTimestamp(t time.Time) string {
	return t.UTC().Format("20060102150405") + "Z"
}
