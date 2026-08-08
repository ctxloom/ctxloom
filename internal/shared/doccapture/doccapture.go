// Package doccapture is the ONE shape shared by the living-docs capture
// writer (tests/acceptance/steps_doc_capture.go, built under the acceptance
// tag) and its reader (scripts/gendocs/livingdocs, a standalone `go run`
// tool) — the JSON contract for one Gherkin scenario's per-step evidence.
//
// These two packages cannot import each other (a test package and
// a standalone script live on opposite sides of a process boundary — the
// writer runs inside `go test`, the reader runs afterward as a separate
// `go run` invocation reading the files the writer left behind) so the two
// structs used to be declared twice, byte-for-byte identical down to the json
// tags, with only a comment ("MUST match that struct's json tags exactly")
// holding the two copies in sync. That is exactly this project's
// characteristic defect shape: a dropped or renamed field on one side is
// invisible to the other until a generated page silently comes up short.
// internal/shared is the one tree both a tests/ package and a scripts/
// package can import without creating a cycle, so it is the natural home for
// a wire contract neither side owns outright.
package doccapture

// DocCapture is one Gherkin scenario's (or one Examples row's) accumulated
// evidence: its steps, in order, plus the identifying metadata a rendered
// page needs.
type DocCapture struct {
	Scenario string           `json:"scenario"`
	Outline  string           `json:"outline,omitempty"` // Examples row, e.g. "engine=claude-code"
	Feature  string           `json:"feature"`           // source .feature file URI
	Tags     []string         `json:"tags"`
	Steps    []DocCaptureStep `json:"steps"`
}

// DocCaptureStep is one step's text, pass/fail outcome, and whatever real
// terminal evidence the harness observed became available immediately after
// that step ran.
type DocCaptureStep struct {
	Text    string `json:"text"`
	Keyword string `json:"keyword,omitempty"` // reconstructed Gherkin keyword (Given/When/Then/And) for syntax highlighting
	Status  string `json:"status"`

	CLIOutput    string `json:"cli_output,omitempty"`
	MockRecorded string `json:"mock_recorded,omitempty"`
	// Materialized is evidence a step observed that never flowed through
	// w.env.LastOutput(): a file it read (J000700 teammate's materialized command,
	// J001500's assembled CLAUDE.md / generated .mcp.json / settings.json), a
	// captured PTY session (J001500 forgery refusal), or a sync notice that a later
	// materialize overwrote (J001500 retraction). Marker-bearing proof no CLI stdout
	// carries; rendered as its own captured block.
	Materialized string `json:"materialized,omitempty"`
}
