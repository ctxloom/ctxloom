package clifmt

import (
	"encoding/json"
	"io"
)

// WarningEnvelope is the structured shape a non-fatal diagnostic takes on
// the machine-readable warning channel. It mirrors ErrorEnvelope's
// {"error": "..."} shape, with Prog added: unlike RenderError's single
// terminal failure, a warning stream can carry diagnostics from several
// components in one invocation (config load, remote fetch, session
// distill, ...), so each line names its source.
type WarningEnvelope struct {
	Prog    string `json:"prog"`
	Warning string `json:"warning"`
}

// EncodeWarning writes env as one compact JSON object followed by a
// newline: JSON Lines, not env's caller's selected --format. A command's
// primary Result is a single value rendered once, so json/yaml/toml/text/
// markdown are all equally valid (Render's job). Warnings are a
// zero-or-more stream interleaved with a still-running command, and only
// JSON has a self-delimiting multi-record form on a shared stream —
// concatenating two bare YAML mappings or two TOML documents with no
// "---"/blank-line separator does not re-parse as two records, while two
// JSON objects separated by newlines are exactly the standard JSON Lines
// convention. So every structured-mode warning is JSON Lines regardless of
// whether the primary --format is json, yaml, or toml.
func EncodeWarning(w io.Writer, env WarningEnvelope) error {
	enc := json.NewEncoder(w)
	return enc.Encode(env)
}
