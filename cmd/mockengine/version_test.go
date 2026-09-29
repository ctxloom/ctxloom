package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// The runner refuses a launch whose engine cannot be shown to be at or above
// its version floor, so every personality with a floor must answer its
// engine's version command with something that engine's own Parse reads and
// CheckFloor admits — or every launch through the mock is refused.
func TestVersionAnswer_PassesTheEnginesOwnFloorCheck(t *testing.T) {
	checked := 0
	for _, name := range impersonable() {
		e, ok := engines.Registry().Lookup(engine.Name(name))
		require.True(t, ok, name)
		v := e.Root().Version
		if !v.Declared() || v.Floor == "" {
			continue
		}
		checked++
		out, ok := versionAnswer(name, v.Args)
		require.True(t, ok, "%s declares a version floor, so the mock must answer %v", name, v.Args)
		got, err := v.Parse(out)
		require.NoError(t, err, "%s's own parse must read the mock's answer %q", name, out)
		assert.NoError(t, v.CheckFloor(name, got), "%s's answer must clear its own floor", name)

		_, ok = versionAnswer(name, append(append([]string{}, v.Args...), "extra"))
		assert.False(t, ok, "%s: only the exact version command is answered; anything else is vendor argv", name)
	}
	assert.GreaterOrEqual(t, checked, 1, "at least one impersonable engine declares a version floor")
}
