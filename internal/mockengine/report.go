package mockengine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// The discovery report is the whole point of the mock: it inverts a context
// assertion from EXISTENCE ("a turn appeared") to EVIDENCE ("here is exactly
// what I received, and where I found it"). ctxloom's characteristic bug is the
// silent no-op — exit 0, success message, zero bytes delivered — and the record
// that makes it visible is the present:false row: a path the engine PROBED and
// found ABSENT. That row is the most valuable output here, not an afterthought,
// so it is a first-class part of the shape rather than an omission.

// ProbeRecord is one context-surface observation: a path the engine's CLI
// declaration (agent.CLIProbe) says the vendor reads, resolved against this
// process's real cwd/home/argv, then STATTED and HASHED. A probed-and-absent
// path produces a record with Present=false — never a dropped row and never a
// crash.
type ProbeRecord struct {
	// Order is the probe's index in the declaration, so a reader sees the
	// vendor's precedence (first-wins within a kind).
	Order int `json:"order"`
	// Kind is the surface category (context, mcp, settings, commands, skills,
	// agents) — agent.ProbeKind's label.
	Kind string `json:"kind"`
	// Scope is where the search root came from (cwd, home, env-dir, flag-value).
	Scope string `json:"scope"`
	// Root is the resolved search root, or "flag:<name>" for a flag-value probe
	// (human diagnostics only; excluded from DiscoveryDigest because it is
	// machine-specific — a tempdir, a container path).
	Root string `json:"root"`
	// Rel is the declaration's root-relative path ("CLAUDE.md",
	// ".claude/settings.json"); empty for a flag-value probe whose path is argv.
	Rel string `json:"rel"`
	// Path is the absolute path probed (human diagnostics only; excluded from
	// DiscoveryDigest for the same reason as Root). Empty when a flag-value
	// probe's flag was absent, or when the value was an inline literal.
	Path string `json:"path"`
	// Present is whether the probed surface EXISTS. A false here on a surface a
	// test delivered is a silent no-op caught red-handed — but read it WITH
	// Unreadable: Present=false only establishes absence when Unreadable is
	// false.
	Present bool `json:"present"`
	// Unreadable marks an observation that FAILED rather than an absence that
	// was established. os.Stat reports "not there" and "I could not look"
	// through the same error return, and collapsing both into Present=false
	// made the instrument that exists to prove delivery report a delivered
	// surface as undelivered (U079-F06). It is in the digest because a
	// conformance assertion of absence must not go green on a failed
	// observation. Note carries the underlying error.
	Unreadable bool `json:"unreadable,omitempty"`
	// Size is the byte length of the file, the inline literal, or (for a
	// directory) the count of entries beneath it.
	Size int64 `json:"size"`
	// SHA256 is lowercase-hex sha256 over the RAW bytes, no normalization. Empty
	// when Present is false. For a directory it is the hash of the canonical
	// entry rendering (see Entries), so a directory surface has one assertable
	// value too.
	SHA256 string `json:"sha256"`
	// Dir marks a directory surface; Entries is then populated.
	Dir bool `json:"dir,omitempty"`
	// Entries are a directory's files, name-sorted, each hashed. Present but
	// empty means the directory exists and is empty — a distinct signal from an
	// absent directory (Present=false, Entries nil).
	Entries []EntryRecord `json:"entries,omitempty"`
	// Head is a bounded prefix of the content for eyeballing (never the
	// assertion target — hashes are). Omitted for directories and absent paths.
	Head string `json:"head,omitempty"`
	// Fallback marks a ScopeEnvDir probe whose env var was UNSET, so the root
	// came from $HOME/<EnvHomeDefault> instead. It is in the digest because
	// the alternative is a false green on the host path: a developer with a
	// real ~/.codex gets present:true and a hash of their personal config for
	// a surface ctxloom never delivered (U079-F04). Root would show it, but
	// Root is machine-specific and deliberately outside the digest.
	Fallback bool `json:"fallback,omitempty"`
	// Note carries the vendor precedence/merge rule or a resolution caveat
	// ("flag absent from argv", "inline JSON literal in argv, not a file").
	Note string `json:"note,omitempty"`
}

// EnvRecord is one observation of the engine's DECLARED environment contract
// (agent.EngineCLI SetEnv/StripEnv). That contract existed with no observer at
// all (U079-F05): a run in which ctxloom stopped setting CTXLOOM_CONTEXT_FILE,
// or stopped stripping a credential, produced a report identical to one where
// it did — in the instrument whose entire job is to prove delivery.
type EnvRecord struct {
	// Name is the variable.
	Name string `json:"name"`
	// Expect is what the declaration says must be true of it on the child:
	// EnvExpectSet (ctxloom sets it) or EnvExpectStripped (ctxloom removes it).
	Expect string `json:"expect"`
	// Present is whether the variable reached the child at all.
	Present bool `json:"present"`
	// Empty distinguishes "set to the empty string" from "set" — the same
	// distinction ProbeRecord draws between an empty directory and an absent
	// one, and this project's own silent-no-op shape: a delivery mechanism
	// that ran, reported success, and passed nothing.
	Empty bool `json:"empty,omitempty"`
}

// Env expectations. Values are the strings that appear in the report JSON and
// in the discovery digest, so they are part of the assertable contract.
const (
	// EnvExpectSet means the declaration lists the variable in SetEnv.
	EnvExpectSet = "set"
	// EnvExpectStripped means the declaration lists it in StripEnv.
	EnvExpectStripped = "stripped"
)

// Honored reports whether the observation matches the declaration. A set
// variable must be present and non-empty; a stripped variable must be absent.
// It is derived rather than stored so the rule has one home and cannot drift
// from the fields it is derived from across the JSON boundary.
func (e EnvRecord) Honored() bool {
	if e.Expect == EnvExpectStripped {
		return !e.Present
	}
	return e.Present && !e.Empty
}

// EntryRecord is one file inside a directory surface.
type EntryRecord struct {
	// Name is the path relative to the directory root, slash-separated and
	// sorted, so the listing is deterministic across filesystems.
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Unreadable marks an entry that was listed but whose bytes could not be
	// read. Without it the entry carried an empty SHA256, which looks like a
	// hash nobody bothered to compute rather than a read that failed
	// (U079-F07).
	Unreadable bool `json:"unreadable,omitempty"`
}

// Report is the full discovery answer for one launch.
type Report struct {
	// Engine and Surface identify the personality this launch impersonated.
	Engine  string `json:"engine"`
	Surface string `json:"surface"`
	// Records are the probe observations in declaration order.
	Records []ProbeRecord `json:"records"`
	// Env is the declared environment contract, observed. One record per
	// SetEnv/StripEnv variable, in declaration order.
	Env []EnvRecord `json:"env"`
	// PromptPresent is whether ANY prompt bytes arrived on the surface's
	// declared channel. It is separate from the hash for the reason
	// ProbeRecord.Present is separate from ProbeRecord.SHA256: zero bytes is a
	// first-class observation, and it is the one this project's characteristic
	// bug produces (U079-F03).
	PromptPresent bool `json:"promptPresent"`
	// PromptSHA256 is sha256 over the EXACT prompt bytes the engine received
	// (stdin for oneshot), so a test can prove the composed context reached the
	// child unmangled. EMPTY when no bytes arrived — never the hash of the
	// empty string, which is indistinguishable from a real delivery to anyone
	// not holding a table of well-known digests.
	PromptSHA256 string `json:"promptSha256"`
	// PromptSize is the prompt's byte length.
	PromptSize int64 `json:"promptSize"`
	// PromptHead is a bounded prefix of the prompt for debugging.
	PromptHead string `json:"promptHead,omitempty"`
	// DiscoveryDigest is a single sha256 over a canonical one-line-per-record
	// rendering of the STABLE fields (see canonicalRendering), so a test can
	// assert ONE value instead of walking every record. It deliberately excludes
	// absolute Root/Path so two runs of the same delivery on different machines
	// produce the same digest.
	DiscoveryDigest string `json:"discoveryDigest"`
}

// headLimit bounds the echoed content prefix.
const headLimit = 256

// head returns a bounded, printable prefix of b for human debugging.
//
// Bounded on a RUNE boundary, not a byte offset: a probed surface is arbitrary
// vendor content, and half a multi-byte rune is not a prefix of anything. And
// printable in fact, not just in the doc — a probed path can be a binary file,
// and its raw control bytes have no business riding into a report a person
// reads or a terminal renders. Newlines and tabs survive; everything else
// non-graphic becomes the replacement character. Plain text — the ordinary
// case — passes through byte for byte.
func head(b []byte) string {
	return printablePrefix(string(b), headLimit)
}

// printablePrefix takes at most limit BYTES of s, cutting only between runes,
// and substitutes the replacement character for anything non-graphic. Ranging
// over a string already decodes an invalid byte as one RuneError, so an
// undecodable surface degrades to visible replacement characters instead of raw
// bytes, and never to a truncated sequence.
func printablePrefix(s string, limit int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		w := utf8.RuneLen(r)
		if w < 0 {
			w = 1
		}
		if n+w > limit {
			break
		}
		n += w
		if isPrintableInReport(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteRune(utf8.RuneError)
	}
	return b.String()
}

// isPrintableInReport keeps the whitespace that carries meaning when eyeballing
// a config file and rejects the rest of the non-graphic range.
func isPrintableInReport(r rune) bool {
	switch r {
	case '\n', '\t':
		return true
	}
	return unicode.IsPrint(r)
}

// hashBytes is sha256 lowercase hex over raw bytes, no normalization — the one
// hasher the whole runtime shares.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// canonicalRendering is the one-line-per-record text the DiscoveryDigest hashes.
// It carries the STABLE identity of each observation — order, kind, scope, rel,
// present, size, content hash — and every directory entry's (name, size, hash),
// but NOT the absolute Root/Path, which vary by machine and would make the
// digest un-assertable. Two deliveries of the same bytes to the same declared
// surfaces render identically here regardless of where on disk they landed.
func canonicalRendering(recs []ProbeRecord, env []EnvRecord) string {
	var b strings.Builder
	for _, r := range recs {
		fmt.Fprintf(&b, "%d|%s|%s|%s|%t|%d|%s|%t|%t\n",
			r.Order, r.Kind, r.Scope, r.Rel, r.Present, r.Size, r.SHA256, r.Fallback, r.Unreadable)
		for _, e := range r.Entries {
			fmt.Fprintf(&b, "  entry|%s|%d|%s|%t\n", e.Name, e.Size, e.SHA256, e.Unreadable)
		}
	}
	// The env contract is rendered by NAME and OUTCOME only: a variable's value
	// is a machine-specific path or harp, and folding it in would make the
	// digest un-assertable for the same reason Root and Path are excluded.
	for _, e := range env {
		fmt.Fprintf(&b, "env|%s|%s|%t|%t\n", e.Name, e.Expect, e.Present, e.Empty)
	}
	return b.String()
}

// BuildReport assembles the discovery records, the observed env contract and
// the prompt into a Report, filling the prompt hash and the single discovery
// digest.
//
// A zero-byte prompt yields PromptPresent=false and an EMPTY PromptSHA256,
// mirroring ProbeRecord's "SHA256 is empty when Present is false": hashing
// nothing produces e3b0c442…, a perfectly ordinary-looking hash, and the field
// that exists to prove delivery then proves it on every run (U079-F03).
func BuildReport(cli agent.EngineCLI, recs []ProbeRecord, env []EnvRecord, prompt []byte) Report {
	rep := Report{
		Engine:          cli.Engine,
		Surface:         string(cli.Surface),
		Records:         recs,
		Env:             env,
		PromptPresent:   len(prompt) > 0,
		PromptSize:      int64(len(prompt)),
		PromptHead:      head(prompt),
		DiscoveryDigest: hashBytes([]byte(canonicalRendering(recs, env))),
	}
	if rep.PromptPresent {
		rep.PromptSHA256 = hashBytes(prompt)
	}
	return rep
}

// EnvViolations returns every env observation that does NOT match the
// declaration — a variable ctxloom was supposed to set and did not (or set to
// nothing), or one it was supposed to strip and did not. Empty means the
// declared environment contract was honoured in full.
func (r Report) EnvViolations() []EnvRecord {
	var out []EnvRecord
	for _, e := range r.Env {
		if !e.Honored() {
			out = append(out, e)
		}
	}
	return out
}

// Report marker lines bracket the machine-readable JSON on stderr, so a caller
// (a docker-run capturing combined output) can extract exactly the report even
// when the vendor wire format owns stdout. The markers are distinctive enough
// not to collide with ordinary engine chatter.
const (
	ReportBegin = "---CTXLOOM-MOCK-REPORT-BEGIN---"
	ReportEnd   = "---CTXLOOM-MOCK-REPORT-END---"
)

// Marshal renders the report as indented JSON.
func (r Report) Marshal() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// ExtractReport pulls the report JSON from a stream that brackets it with the
// begin/end markers and unmarshals it — the reader half of the stderr channel,
// used by the binary's own round-trip test (runtime_test.go) and by the
// container test (container_docker_integration_test.go), which cross-checks
// it against the file-channel report to prove the two agree from inside a
// real container.
func ExtractReport(s string) (Report, error) {
	var rep Report
	i := strings.Index(s, ReportBegin)
	if i < 0 {
		return rep, fmt.Errorf("no report marker %q found in output", ReportBegin)
	}
	rest := s[i+len(ReportBegin):]
	j := strings.Index(rest, ReportEnd)
	if j < 0 {
		return rep, fmt.Errorf("report begin marker with no matching %q", ReportEnd)
	}
	if err := json.Unmarshal([]byte(rest[:j]), &rep); err != nil {
		return rep, fmt.Errorf("report JSON between markers did not parse: %w", err)
	}
	return rep, nil
}
