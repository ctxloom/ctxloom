package agent

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeliveredFuncParity is the U029-F04/U055-F09 parity gate. Six packages
// carried a private `deliveredFunc func() error` retype of the exported
// agent.DeliveredFunc; this package carried BOTH at once (managed_commands.go's
// unexported copy alongside managedcontext.go's exported one), which is the only
// pair a compiler-checked parity test can reach — the other five copies are
// unexported in packages that already import this one.
//
// It pins the behaviour the collapse onto agent.DeliveredFunc must preserve:
// Cleanup invokes the wrapped closure exactly once and returns its error
// verbatim (including nil). Written BEFORE the collapse, against both types, so
// "they were identical" is a measured fact rather than an assumption.
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

			localCalls := 0
			var local Delivered = deliveredFunc(func() error {
				localCalls++
				return tc.err
			})

			gotExported := exported.Cleanup()
			gotLocal := local.Cleanup()

			require.Equal(t, 1, exportedCalls, "DeliveredFunc.Cleanup must invoke the closure exactly once")
			require.Equal(t, 1, localCalls, "deliveredFunc.Cleanup must invoke the closure exactly once")
			assert.Equal(t, tc.err, gotExported, "DeliveredFunc.Cleanup must return the closure's error verbatim")
			assert.Equal(t, gotExported, gotLocal, "the two retypes must be behaviourally identical — divergence here is the defect")
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
