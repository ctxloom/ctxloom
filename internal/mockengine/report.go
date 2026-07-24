package mockengine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
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
	// test delivered is a silent no-op caught red-handed.
	Present bool `json:"present"`
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
	// Note carries the vendor precedence/merge rule or a resolution caveat
	// ("flag absent from argv", "inline JSON literal in argv, not a file").
	Note string `json:"note,omitempty"`
}

// EntryRecord is one file inside a directory surface.
type EntryRecord struct {
	// Name is the path relative to the directory root, slash-separated and
	// sorted, so the listing is deterministic across filesystems.
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Report is the full discovery answer for one launch.
type Report struct {
	// Engine and Surface identify the personality this launch impersonated.
	Engine  string `json:"engine"`
	Surface string `json:"surface"`
	// Records are the probe observations in declaration order.
	Records []ProbeRecord `json:"records"`
	// PromptSHA256 is sha256 over the EXACT prompt bytes the engine received
	// (stdin for oneshot), so a test can prove the composed context reached the
	// child unmangled.
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
func head(b []byte) string {
	if len(b) > headLimit {
		return string(b[:headLimit])
	}
	return string(b)
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
func canonicalRendering(recs []ProbeRecord) string {
	var b strings.Builder
	for _, r := range recs {
		fmt.Fprintf(&b, "%d|%s|%s|%s|%t|%d|%s\n",
			r.Order, r.Kind, r.Scope, r.Rel, r.Present, r.Size, r.SHA256)
		for _, e := range r.Entries {
			fmt.Fprintf(&b, "  entry|%s|%d|%s\n", e.Name, e.Size, e.SHA256)
		}
	}
	return b.String()
}

// BuildReport assembles the discovery records and prompt into a Report, filling
// the prompt hash and the single discovery digest.
func BuildReport(engine, surface string, recs []ProbeRecord, prompt []byte) Report {
	return Report{
		Engine:          engine,
		Surface:         surface,
		Records:         recs,
		PromptSHA256:    hashBytes(prompt),
		PromptSize:      int64(len(prompt)),
		PromptHead:      head(prompt),
		DiscoveryDigest: hashBytes([]byte(canonicalRendering(recs))),
	}
}

// Record returns the first record of the given kind, or false — a convenience
// for tests asserting on one surface.
func (r Report) Record(kind string) (ProbeRecord, bool) {
	for _, rec := range r.Records {
		if rec.Kind == kind {
			return rec, true
		}
	}
	return ProbeRecord{}, false
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
// shared by the binary's own round-trip test and the container test.
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
