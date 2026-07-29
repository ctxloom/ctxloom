package agent

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeliveredFuncParity is the U029-F04/U055-F09 contract gate. Six packages
// used to carry a private `deliveredFunc func() error` retype of the exported
// agent.DeliveredFunc (claude ×2, codex, kiro, antigravity, opencode, and this
// package's own managed_commands.go copy). They were collapsed onto the single
// exported type; this test is what the collapse rests on.
//
// It was written BEFORE the collapse and asserted BOTH types side by side — the
// only pair a compiler-checked parity assertion could reach, since the other
// five copies were unexported in packages that already import this one. It
// passed, so "the copies had not diverged" is a measured fact. The local half
// was dropped with the type it covered; every one of the ~14 former call sites
// now depends on exactly this behaviour: Cleanup invokes the wrapped closure
// exactly once and returns its error verbatim (including nil).
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
