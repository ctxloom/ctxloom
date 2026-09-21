package coord

import (
	"errors"
	"fmt"
)

// ErrForbidden marks a refusal of the CALLER — not this session, not its
// child, not a credential that may do this — on a path that has no sentinel
// of its own. The wire adapter answers it as a permission refusal.
var ErrForbidden = errors.New("coord: forbidden")

// ErrNotFound marks a lookup that named nothing this coordinator holds.
var ErrNotFound = errors.New("coord: not found")

// refused pairs a sentinel with the exact text a refusal carries: errors.Is
// routes on the sentinel, Error() is the message alone (no "sentinel: "
// prefix), so a caller reads the same words whether it reached the refusal
// in-process or over the wire.
type refused struct {
	sentinel error
	msg      string
}

func (e refused) Error() string        { return e.msg }
func (e refused) Is(target error) bool { return target == e.sentinel }

// refusal builds a refused error carrying sentinel and the formatted text.
func refusal(sentinel error, format string, a ...any) error {
	return refused{sentinel: sentinel, msg: fmt.Sprintf(format, a...)}
}
