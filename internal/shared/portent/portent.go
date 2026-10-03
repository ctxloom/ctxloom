package portent

import (
	"errors"
	"iter"
)

// Range is an inclusive range of port numbers, Lo..Hi.
type Range struct{ Lo, Hi uint16 }

var (
	Privileged = Range{1, 1023}
	Registered = Range{1024, 49151}
	Dynamic    = Range{49152, 65535}
	Service    = Range{1024, 32767}
)

var (
	ErrPortZero   = errors.New("portent: range includes port 0")
	ErrEmptyRange = errors.New("portent: range is empty (Lo > Hi)")
)

func (r Range) Validate() error { return nil }

type stream struct{ next func() uint64 }

func hashStream(input []byte) *stream { return &stream{next: func() uint64 { return 0 }} }

func (s *stream) uniform(m uint64) uint64 { return 0 }

func (s *stream) stride(size uint64) uint64 { return 1 }

func Pick(input []byte, r Range) uint16 { return 0 }

func Candidates(input []byte, r Range) iter.Seq[uint16] {
	return func(func(uint16) bool) {}
}
