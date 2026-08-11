package agent

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeliveredFuncParity is the contract gate for agent.DeliveredFunc, the one
// exported type that five packages would otherwise each retype privately as
// `deliveredFunc func() error` (claude ×2, codex, kiro, opencode,
// and this package's own managed_commands.go). Those copies are unexported in
// packages that already import this one, so no compiler-checked parity
// assertion can reach them — a private retype is a divergence nothing catches.
//
// Every call site depends on exactly this behaviour: Cleanup invokes the
// wrapped closure exactly once and returns its error verbatim (including nil).
func TestDeliveredFuncParity(t *testing.T) {
	sentinel := errors.New("teardown failed")

	cases := []struct {
		name string
		err  error
	}{
		{"cleanup succeeds", nil},
		{"cleanup fails", sentinel},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exportedCalls := 0
			var exported Delivered = DeliveredFunc(func() error {
				exportedCalls++
				return tc.err
			})

			gotExported := exported.Cleanup()

			require.Equal(t, 1, exportedCalls, "DeliveredFunc.Cleanup must invoke the closure exactly once")
			assert.Equal(t, tc.err, gotExported, "DeliveredFunc.Cleanup must return the closure's error verbatim")
		})
	}
}

// TestDeliveredFunc_ErrorIsNotWrapped pins that a caller can still match a
// sentinel with errors.Is after Cleanup — the surfaces' teardown errors flow
// straight out to the cell's cleanup collector.
func TestDeliveredFunc_ErrorIsNotWrapped(t *testing.T) {
	sentinel := errors.New("boom")
	d := DeliveredFunc(func() error { return sentinel })
	assert.True(t, errors.Is(d.Cleanup(), sentinel))
}
