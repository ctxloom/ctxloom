package clifmt

import "io"

// ErrorEnvelope is the structured shape RenderError emits for json/yaml/
// toml. Its single field goes through the same reflective path as any
// other Result, which is also what gives text/markdown their "Error: msg"
// human line for free.
type ErrorEnvelope struct {
	Error string `json:"error"`
}

// RenderError renders err as a structured envelope in json/yaml/toml, or a
// single human line ("Error: <msg>" / "**Error:** <msg>") in text/markdown.
// Callers use this instead of Render for failures so machine consumers
// always get a parseable error object instead of a bare stderr string.
func RenderError(w io.Writer, err error, f Format) error {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return Render(w, ErrorEnvelope{Error: msg}, f)
}
