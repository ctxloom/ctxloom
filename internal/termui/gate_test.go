package termui

import (
	"bytes"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOutputGate_PassthroughWhenOpen(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := NewOutputGate(&mu, &tty, 64, nil, nil)
	_, _ = g.Write([]byte("engine says hi"))
	assert.Equal(t, "engine says hi", tty.String())
}

func TestOutputGate_HoldDivertsAndReleaseReplays(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := NewOutputGate(&mu, &tty, 64, nil, nil)

	_, _ = g.Write([]byte("before|"))
	g.Hold()
	_, _ = g.Write([]byte("held-1|"))
	_, _ = g.Write([]byte("held-2"))
	assert.Equal(t, "before|", tty.String(), "held bytes must not reach the tty")

	g.Release([]byte("<restore>"))
	assert.Equal(t, "before|<restore>held-1|held-2", tty.String(),
		"release writes the restore sequence, then the held bytes in order")

	_, _ = g.Write([]byte("|after"))
	assert.Equal(t, "before|<restore>held-1|held-2|after", tty.String())
}

func TestOutputGate_ReleaseWithOverflowAppendsTruncationNotice(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := NewOutputGate(&mu, &tty, 8, nil, nil)

	g.Hold()
	_, _ = g.Write([]byte("0123456789abcdef")) // 16 into 8: oldest dropped
	g.Release(nil)

	out := tty.String()
	assert.Contains(t, out, "89abcdef", "the newest bytes survive")
	assert.NotContains(t, out, "01234567", "the oldest bytes are gone")
	assert.Contains(t, out, "8 bytes of engine output dropped",
		"overflow surfaces as a visible truncation notice")
}

func TestOutputGate_ReleaseWhenOpenIsNoop(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := NewOutputGate(&mu, &tty, 64, nil, nil)
	g.Release([]byte("<restore>"))
	assert.Empty(t, tty.String(), "releasing an open gate writes nothing")
}

func TestOutputGate_AfterWriteHookRidesPassthroughOnly(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	calls := 0
	g := NewOutputGate(&mu, &tty, 64, nil, func() { calls++ })

	_, _ = g.Write([]byte("a"))
	assert.Equal(t, 1, calls)
	g.Hold()
	_, _ = g.Write([]byte("b"))
	assert.Equal(t, 1, calls, "held writes never trigger the bar flush hook")
}
