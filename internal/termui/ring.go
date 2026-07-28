package termui

// ring is a bounded drop-oldest byte buffer: the engine-output hold while the
// viewer is engaged. Not goroutine-safe — the outputGate serializes access
// under the tty lock.
type ring struct {
	buf     []byte
	start   int
	n       int
	dropped int64
}

// newRing allocates a ring of the given capacity (minimum 1).
func newRing(capacity int) *ring {
	if capacity < 1 {
		capacity = 1
	}
	return &ring{buf: make([]byte, capacity)}
}

// Write appends p, evicting the oldest bytes on overflow. Always succeeds.
func (r *ring) Write(p []byte) (int, error) {
	w := len(p)
	if w >= len(r.buf) {
		// p alone overflows the ring: keep its tail, drop everything held
		// plus p's own head.
		r.dropped += int64(r.n + w - len(r.buf))
		copy(r.buf, p[w-len(r.buf):])
		r.start = 0
		r.n = len(r.buf)
		return w, nil
	}
	if overflow := r.n + w - len(r.buf); overflow > 0 {
		r.start = (r.start + overflow) % len(r.buf)
		r.n -= overflow
		r.dropped += int64(overflow)
	}
	end := (r.start + r.n) % len(r.buf)
	c := copy(r.buf[end:], p)
	copy(r.buf, p[c:])
	r.n += w
	return w, nil
}

// Drain returns the held bytes in write order plus the count dropped to
// overflow, and resets the ring.
func (r *ring) Drain() (data []byte, dropped int64) {
	data = make([]byte, r.n)
	c := copy(data, r.buf[r.start:min(r.start+r.n, len(r.buf))])
	copy(data[c:], r.buf[:r.n-c])
	dropped = r.dropped
	r.start, r.n, r.dropped = 0, 0, 0
	return data, dropped
}
