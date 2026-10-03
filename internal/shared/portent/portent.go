package portent

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"iter"
)

// Range is an inclusive range of port numbers, Lo..Hi. A valid range has
// 1 <= Lo <= Hi; see [Range.Validate].
type Range struct{ Lo, Hi uint16 }

// The predefined ranges. The package documentation says what each one
// promises and where that depends on the OS.
var (
	Privileged = Range{1, 1023}
	Registered = Range{1024, 49151}
	Dynamic    = Range{49152, 65535}
	Service    = Range{1024, 32767}
)

// Errors reported by [Range.Validate], and the panic values of [Range.Pick]
// and [Range.Candidates] for an invalid range. Match them with errors.Is.
var (
	ErrPortZero   = errors.New("portent: range includes port 0")
	ErrEmptyRange = errors.New("portent: range is empty (Lo > Hi)")
)

// Validate reports whether r is a usable range: nil, or an error wrapping
// [ErrPortZero] or [ErrEmptyRange].
func (r Range) Validate() error {
	switch {
	case r.Lo == 0:
		return fmt.Errorf("%w: %d-%d", ErrPortZero, r.Lo, r.Hi)
	case r.Lo > r.Hi:
		return fmt.Errorf("%w: %d-%d", ErrEmptyRange, r.Lo, r.Hi)
	}
	return nil
}

// size panics if r is invalid, so every exported entry point rejects misuse
// before doing any work.
func (r Range) size() uint64 {
	if err := r.Validate(); err != nil {
		panic(err)
	}
	return uint64(r.Hi) - uint64(r.Lo) + 1
}

// Pick returns the port in [Service] that input maps to. It is
// Service.Pick(input).
func Pick(input []byte) uint16 { return Service.Pick(input) }

// Candidates returns every port in [Service] in the order input fixes. It is
// Service.Candidates(input).
func Candidates(input []byte) iter.Seq[uint16] { return Service.Candidates(input) }

// Pick returns the port in r that input maps to. It is the first port of
// [Range.Candidates]. It panics if r is invalid.
func (r Range) Pick(input []byte) uint16 {
	size := r.size()
	return r.Lo + uint16(hashStream(input).uniform(size))
}

// Candidates returns every port in r exactly once, in an order fixed by
// input. The first port is [Range.Pick]'s. The sequence can be iterated more
// than once and gives the same order each time. It panics at the call, not on
// iteration, if r is invalid.
func (r Range) Candidates(input []byte) iter.Seq[uint16] {
	size := r.size()
	s := hashStream(input)
	start := s.uniform(size)
	stride := s.stride(size)
	return func(yield func(uint16) bool) {
		for i := range size {
			if !yield(r.Lo + uint16((start+i*stride)%size)) {
				return
			}
		}
	}
}

// stream is the draw sequence. next is a field so tests can supply draws
// that exercise rejection, which real hashes almost never trigger.
type stream struct{ next func() uint64 }

// hashStream returns the draw sequence for input: draw n is the first 8
// bytes, big-endian, of SHA-256(input || BE32(n)).
func hashStream(input []byte) *stream {
	buf := make([]byte, len(input)+4)
	copy(buf, input)
	var n uint32
	return &stream{next: func() uint64 {
		binary.BigEndian.PutUint32(buf[len(input):], n)
		n++
		sum := sha256.Sum256(buf)
		return binary.BigEndian.Uint64(sum[:8])
	}}
}

// uniform returns a value in [0, m) without modulo bias. -m % m in uint64
// arithmetic is 2^64 mod m; draws below it are the biased surplus.
func (s *stream) uniform(m uint64) uint64 {
	threshold := -m % m
	for {
		if v := s.next(); v >= threshold {
			return v % m
		}
	}
}

// stride returns a value in [1, size) coprime to size, or 1 for size 1.
// Coprimality is what makes the walk in Candidates visit every port.
func (s *stream) stride(size uint64) uint64 {
	if size == 1 {
		return 1
	}
	for {
		if st := 1 + s.uniform(size-1); gcd(st, size) == 1 {
			return st
		}
	}
}

func gcd(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
