package engine

// Declared is an optional engine capability whose ABSENCE is a stated value,
// not an omission. A slot of this type is in one of three states:
//
//   - PROVIDED (Provide): the engine carries the capability, and the value is
//     what it carries.
//   - ABSENT (Absent): the engine does not carry it, and the reason says why —
//     the clause a report shows a user who asks where a surface went.
//   - UNDECIDED (the zero value): nobody said either. A registration gate
//     refuses this state, so a descriptor that forgot a slot cannot register
//     as one that declared it absent.
//
// The rule it encodes is the one noHooksReason already states in prose: a
// capability written nowhere is indistinguishable from one nobody asked for,
// so the absence has to be DECLARED to be reportable. Making presence and its
// reason ONE slot, rather than a value field beside a reason field, is what
// keeps "both omitted" from reading as a decision.
type Declared[T any] struct {
	value   T
	absent  string
	decided bool
}

// ErrAbsentWithoutReason is the panic value Absent raises for an empty
// reason: an absence with nothing to report is the omission this type exists
// to refuse, and it is refused at construction so it cannot exist at all.
const ErrAbsentWithoutReason = "engine: Absent requires a reason; an unexplained absence is an omission"

// Provide declares the capability present with value v. Presence is what the
// author states, not a property of v — a nil func provided is present, and it
// is the registration gate's job (not this type's) to reject it.
func Provide[T any](v T) Declared[T] {
	return Declared[T]{value: v, decided: true}
}

// Absent declares the capability missing, for the stated reason. Panics on
// an empty reason (see ErrAbsentWithoutReason).
func Absent[T any](reason string) Declared[T] {
	if reason == "" {
		panic(ErrAbsentWithoutReason)
	}
	return Declared[T]{absent: reason, decided: true}
}

// Get returns the provided value and true, or the zero value and false when
// the slot is absent or undecided.
func (d Declared[T]) Get() (T, bool) {
	if !d.decided || d.absent != "" {
		var zero T
		return zero, false
	}
	return d.value, true
}

// AbsentReason returns the declared reason for absence, or "" when the slot
// is provided or undecided.
func (d Declared[T]) AbsentReason() string { return d.absent }

// Decided reports whether the slot was written by Provide or Absent at all.
// It is what a registration gate reads to refuse an undeclared slot.
func (d Declared[T]) Decided() bool { return d.decided }
