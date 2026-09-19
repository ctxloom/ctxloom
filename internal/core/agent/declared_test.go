package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeclared_ZeroValue_IsUndecided pins the whole reason the type exists:
// a slot nobody wrote to is neither present nor absent, so a registration
// gate can refuse it instead of reading it as "absent".
func TestDeclared_ZeroValue_IsUndecided(t *testing.T) {
	var d Declared[int]
	assert.False(t, d.Decided())
	_, ok := d.Get()
	assert.False(t, ok)
	assert.Empty(t, d.AbsentReason())
}

func TestProvide_ValueIsPresentAndDecided(t *testing.T) {
	d := Provide(42)
	require.True(t, d.Decided())
	v, ok := d.Get()
	assert.True(t, ok)
	assert.Equal(t, 42, v)
	assert.Empty(t, d.AbsentReason())
}

func TestAbsent_ReasonIsCarriedAndValueIsNotPresent(t *testing.T) {
	d := Absent[func() string]("this engine has no binary to ask")
	require.True(t, d.Decided())
	v, ok := d.Get()
	assert.False(t, ok)
	assert.Nil(t, v)
	assert.Equal(t, "this engine has no binary to ask", d.AbsentReason())
}

// TestAbsent_EmptyReasonPanics: an absence with no stated reason is an
// omission wearing a declaration's clothes — the exact thing the type refuses
// to represent. Refused at the constructor so it cannot be built at all.
func TestAbsent_EmptyReasonPanics(t *testing.T) {
	assert.PanicsWithValue(t, ErrAbsentWithoutReason, func() { Absent[int]("") })
}

// TestProvide_NilFuncIsStillPresent: presence is what the author said, not
// what the value happens to be. A nil func provided is a bug for the
// registration gate's nil checks, not for this type, which must not guess.
func TestProvide_NilFuncIsStillPresent(t *testing.T) {
	d := Provide[func()](nil)
	assert.True(t, d.Decided())
	_, ok := d.Get()
	assert.True(t, ok)
}
