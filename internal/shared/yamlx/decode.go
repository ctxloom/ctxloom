// Package yamlx holds the YAML decoding idioms shared across this module's
// binaries, so a decision about how strictly a document is read is made once.
package yamlx

import (
	"bytes"
	"errors"
	"io"

	"gopkg.in/yaml.v3"
)

// DecodeStrict decodes a YAML document into v, REJECTING any key v does not
// model and treating an empty document as the zero value.
//
// The strictness is the point. A config key the struct does not know is a
// typo — `profils:` for `profiles:`, `naem:` for `name:` — and yaml's default
// is to drop it without a word, which produces a value nobody authored and an
// exit code that says it worked.
//
// The empty-document tolerance is equally deliberate: yaml reports io.EOF for
// a zero-byte or comments-only file, which is not a parse failure. Each caller
// applies its own emptiness rule afterwards (an agent needs at least one
// profile; a rules file may legitimately be blank), so imposing one here would
// take a decision that is not this function's to make.
//
// The error is returned unwrapped so callers can add their own context.
func DecodeStrict(data []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
