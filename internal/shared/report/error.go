package report

import "fmt"

// Remediable is an error that names its own fix. Raise sites return one so
// the fix travels with the failure instead of being spliced into its text;
// the renderer (pkg/clifmt's RemedyOf, which reads the same method set
// structurally) pulls it back out of any %w chain.
type Remediable interface {
	error
	Remedy() string
}

// Error is the base remediable error. A specific error embeds it or
// implements Remedy() itself. The field is Fix rather than Remedy only
// because Go forbids a field and a method of one name: Remedy() is the
// concept, Fix its storage.
//
// Value receivers, so both an embedded T{Error: …} and a *T satisfy
// Remediable.
type Error struct {
	Msg string // what broke; "" when Err already says it
	Fix string // the one-line remedy
	Err error  // wrapped cause; errors.Is/As traverse it
}

// Error is Msg, Err's text, or "Msg: Err" when both are set.
func (e Error) Error() string {
	switch {
	case e.Err == nil:
		return e.Msg
	case e.Msg == "":
		return e.Err.Error()
	default:
		return e.Msg + ": " + e.Err.Error()
	}
}

// Remedy returns Fix.
func (e Error) Remedy() string { return e.Fix }

// Unwrap returns Err, so a sentinel wrapped under a remedy stays errors.Is-able.
func (e Error) Unwrap() error { return e.Err }

// Errorf is Error{Fix: fix, Err: fmt.Errorf(format, args...)}; a %w verb in
// format keeps its target reachable.
func Errorf(fix, format string, args ...any) error {
	return Error{Fix: fix, Err: fmt.Errorf(format, args...)}
}
