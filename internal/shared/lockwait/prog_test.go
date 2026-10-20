package lockwait

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestProgOf: the wait line names the program that is waiting, and an argv[0]
// that names none (filepath.Base's "." and "/") falls back to ctxloom rather
// than stamping a meaningless name.
func TestProgOf(t *testing.T) {
	for arg0, want := range map[string]string{
		"/usr/local/bin/taskloom": "taskloom",
		"ltk":                     "ltk",
		"":                        "ctxloom",
		"/":                       "ctxloom",
	} {
		assert.Equal(t, want, progOf(arg0), "argv[0] %q", arg0)
	}
}
