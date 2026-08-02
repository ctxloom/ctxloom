package grpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStartHostRunner_RefusesEmptyArgs pins that StartHostRunner used to
// self-exec whatever it was given, so an empty args slice launched a BARE
// `ctxloom` — cobra prints help and exits 0 — and returned a healthy-looking
// *HostRunner for a process that can never dial home. The caller only learned
// about it as the coordinator's readiness timeout, with no error anywhere.
//
// The pre-fix red is deliberately not demonstrated by reverting: without the
// guard this call self-execs the TEST binary with no arguments, which re-runs
// this whole package (including this test) in a child, recursively. The guard
// must therefore return before exec.Command is ever reached — asserting a nil
// handle is what proves nothing was started.
func TestStartHostRunner_RefusesEmptyArgs(t *testing.T) {
	for _, args := range [][]string{nil, {}, {""}} {
		h, err := StartHostRunner(args, nil)
		require.Error(t, err, "args %q must be refused, not self-exec'd", args)
		assert.Nil(t, h, "a refused spawn must not hand back a runner handle")
		assert.Contains(t, err.Error(), "no subcommand")
	}
}
