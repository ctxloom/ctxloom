package termui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func writeString(t *testing.T, r *ring, s string) {
	t.Helper()
	n, err := r.Write([]byte(s))
	assert.NoError(t, err)
	assert.Equal(t, len(s), n, "Write must report the full input length even when evicting")
}

func TestRing_OrderPreserved(t *testing.T) {
	r := newRing(16)
	writeString(t, r, "hello ")
	writeString(t, r, "world")
	data, dropped := r.Drain()
	assert.Equal(t, "hello world", string(data))
	assert.Zero(t, dropped)
}

func TestRing_OverflowDropsOldest(t *testing.T) {
	r := newRing(8)
	writeString(t, r, "abcdef") // 6
	writeString(t, r, "ghij")   // +4 → evict "ab"
	data, dropped := r.Drain()
	assert.Equal(t, "cdefghij", string(data))
	assert.Equal(t, int64(2), dropped)
}

func TestRing_SingleWriteLargerThanCapacity(t *testing.T) {
	r := newRing(4)
	writeString(t, r, "xy")
	writeString(t, r, "abcdefgh") // keeps only the tail
	data, dropped := r.Drain()
	assert.Equal(t, "efgh", string(data))
	assert.Equal(t, int64(6), dropped, "the 2 held + 4 of the write itself")
}

func TestRing_DrainResets(t *testing.T) {
	r := newRing(4)
	writeString(t, r, "abcdef")
	_, dropped := r.Drain()
	assert.Equal(t, int64(2), dropped)

	writeString(t, r, "zz")
	data, dropped := r.Drain()
	assert.Equal(t, "zz", string(data))
	assert.Zero(t, dropped, "dropped count must reset on drain")
}

func TestRing_WrapAroundReadback(t *testing.T) {
	r := newRing(5)
	writeString(t, r, "abcd")
	writeString(t, r, "ef") // evicts "a", wraps
	data, dropped := r.Drain()
	assert.Equal(t, "bcdef", string(data))
	assert.Equal(t, int64(1), dropped)
}
