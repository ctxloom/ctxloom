package clifmt

import (
	"errors"
	"io"
)

// ErrorEnvelope is the structured shape RenderError emits for json/yaml/
// toml. Its single field goes through the same reflective path as any
// other Result, which is also what gives text/markdown their "Error: msg"
// human line for free.
type ErrorEnvelope struct {
	Error string `json:"error"`
}

// ErrNilError is returned by RenderError when err is nil. A nil error is not a
// failure to report, so there is nothing to render: emitting the envelope
// anyway would hand a machine consumer a well-formed failure ({"error": ""})
// that names nothing, which is indistinguishable from a real failure whose
// message happened to be empty.
var ErrNilError = errors.New("clifmt: RenderError requires a non-nil error")

// RenderError renders err as a structured envelope in json/yaml/toml, or a
// single human line ("Error: <msg>" / "**Error:** <msg>") in text/markdown.
// Callers use this instead of Render for failures so machine consumers
// always get a parseable error object instead of a bare stderr string.
//
// A nil err writes nothing and returns ErrNilError: the caller's own
// "did this fail?" branch is the place that decision belongs, and a caller
// that has lost it must not be handed an empty failure report to print.
func RenderError(w io.Writer, err error, f Format) error {
	if err == nil {
		return ErrNilError
	}
	return Render(w, ErrorEnvelope{Error: err.Error()}, f)
}
