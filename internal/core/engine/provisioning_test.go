package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The zero value must not be usable, and must not be reachable by leaving a
// field blank — the whole reason the enum starts at Unset.
func TestMaterialDelivery_ZeroValueIsUndecided(t *testing.T) {
	var zero MaterialDelivery
	assert.Equal(t, MaterialDeliveryUnset, zero)
	assert.False(t, zero.Decided(), "the zero value must not read as a decision")
	assert.Equal(t, "unset", zero.String())
}

func TestMaterialDelivery_EveryNamedValueIsDecided(t *testing.T) {
	for _, d := range []MaterialDelivery{MaterialDeliveryMounted, MaterialDeliveryReplicated, MaterialDeliveryAbsent} {
		assert.True(t, d.Decided(), "%s must be a decision", d)
		assert.NotEqual(t, "unset", d.String())
	}
}

// An out-of-range value renders as its number rather than silently reading as
// one of the members — the diagnostic must not lie about what it found.
func TestMaterialDelivery_UnknownValueRendersItsNumber(t *testing.T) {
	assert.Equal(t, "MaterialDelivery(9)", MaterialDelivery(9).String())
	assert.False(t, MaterialDelivery(9).Decided())
}

func TestValidateAccept_AcceptsADeclaredOrder(t *testing.T) {
	require.NoError(t, validateAccept([]MaterialDelivery{MaterialDeliveryMounted, MaterialDeliveryReplicated}))
}

// An empty Accept is the "nobody decided" state wearing a valid-looking
// slice. It is refused: an engine with nothing to provision declares no seed.
func TestValidateAccept_RefusesEmpty(t *testing.T) {
	err := validateAccept(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Accept is empty")
}

func TestValidateAccept_RefusesUnsetEntry(t *testing.T) {
	err := validateAccept([]MaterialDelivery{MaterialDeliveryMounted, MaterialDeliveryUnset})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Accept[1] is unset")
}

// Absence is not something to fall back TO: an order naming it would read as
// "if no mechanism works, proceed with no material", which is an engine that
// starts logged out rather than a refusal.
func TestValidateAccept_RefusesAbsentAsAFallback(t *testing.T) {
	err := validateAccept([]MaterialDelivery{MaterialDeliveryMounted, MaterialDeliveryAbsent})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absence is not something to fall back TO")
}

// A repeat is a second entry that can never be reached, so it is a typo for
// some other delivery rather than a preference.
func TestValidateAccept_RefusesARepeatedDelivery(t *testing.T) {
	err := validateAccept([]MaterialDelivery{MaterialDeliveryMounted, MaterialDeliveryMounted})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repeats mounted")
}

// The slot an engine writes has three states, and the undecided one is what
// Validate refuses. Absence carries a reason that reads back.
func TestDeclared_CredentialSeed_DistinguishesAbsentFromUndecided(t *testing.T) {
	var undecided Declared[CredentialSeed]
	assert.False(t, undecided.Decided())
	assert.Empty(t, undecided.AbsentReason())

	absent := Absent[CredentialSeed]("fixture authenticates against nothing")
	assert.True(t, absent.Decided())
	_, ok := absent.Get()
	assert.False(t, ok)
	assert.Equal(t, "fixture authenticates against nothing", absent.AbsentReason())

	provided := Provide(CredentialSeed{Accept: []MaterialDelivery{MaterialDeliveryMounted}})
	got, ok := provided.Get()
	require.True(t, ok)
	assert.Equal(t, []MaterialDelivery{MaterialDeliveryMounted}, got.Accept)
	assert.Empty(t, provided.AbsentReason())
}
