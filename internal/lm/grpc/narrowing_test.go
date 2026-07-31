package grpc

import (
	"math"
	"strconv"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
)

// Counts, indices and timeouts are `int` in Go and int32 on this wire. A bare
// int32(v) conversion of an out-of-range value WRAPS, and every one of these
// fields is read downstream as a non-negative quantity: a wrapped hook timeout
// becomes a negative duration, a wrapped count becomes a negative length. The
// hook timeout is the reachable one — wire.Hook.Timeout is `timeout:` in the
// user's own config.yaml, so any value past 2^31-1 crosses the wire negated.
func TestWireNarrowing_SaturatesInsteadOfWrapping(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("int is 32 bits on this platform; these narrowings cannot overflow")
	}
	overflowing := int(math.MaxInt32)
	overflowing++

	t.Run("hook timeout", func(t *testing.T) {
		got := hookToProto(wire.Hook{Timeout: overflowing}).GetTimeout()
		assert.Positive(t, got, "a hook timeout past int32 must not cross the wire negative")
		assert.Equal(t, int32(math.MaxInt32), got)
	})

	t.Run("tool location line", func(t *testing.T) {
		got := locationsToProto([]agent.ToolLocation{{Path: "f.go", Line: overflowing}})
		assert.Positive(t, got[0].GetLine(), "a line number past int32 must not cross the wire negative")
	})

	t.Run("session entry count", func(t *testing.T) {
		got := sessionMetaToProto(agent.SessionMeta{ID: "s", EntryCount: overflowing}).GetEntryCount()
		assert.Positive(t, got, "an entry count past int32 must not cross the wire negative")
	})

	t.Run("response boundary indices", func(t *testing.T) {
		// The watcher's marks are transcript lengths, so this site cannot be
		// driven through step() without materializing 2^31 entries. The
		// narrowing itself is what must hold.
		assert.Equal(t, int32(math.MaxInt32), int32Clamped(overflowing))
		assert.Equal(t, int32(math.MinInt32), int32Clamped(-overflowing))
		assert.Equal(t, int32(7), int32Clamped(7))
	})
}
