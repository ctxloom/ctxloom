package agent

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

func TestProvisioningPolicy_AcceptsADeclaredOrder(t *testing.T) {
	p := ProvisioningPolicy{Accept: []MaterialDelivery{MaterialDeliveryMounted, MaterialDeliveryReplicated}}
	require.NoError(t, p.Validate())
}

// An empty Accept is the "nobody decided" state wearing a valid-looking
// struct. It must be refused, and the refusal must name the alternative — an
// engine with nothing to provision declares the SLOT absent.
func TestProvisioningPolicy_RefusesEmptyAccept(t *testing.T) {
	err := ProvisioningPolicy{}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Accept is empty")
	assert.Contains(t, err.Error(), "absent")
}

func TestProvisioningPolicy_RefusesUnsetEntry(t *testing.T) {
	err := ProvisioningPolicy{Accept: []MaterialDelivery{MaterialDeliveryMounted, MaterialDeliveryUnset}}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Accept[1] is unset")
}

// Absence is not something to fall back TO: a policy naming it would read as
// "if no mechanism works, proceed with no material", which is an engine that
// starts logged out rather than a refusal.
func TestProvisioningPolicy_RefusesAbsentAsAFallback(t *testing.T) {
	err := ProvisioningPolicy{Accept: []MaterialDelivery{MaterialDeliveryMounted, MaterialDeliveryAbsent}}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absence is not something to fall back TO")
}

// A repeat is a second entry that can never be reached, so it is a typo for
// some other delivery rather than a preference.
func TestProvisioningPolicy_RefusesARepeatedDelivery(t *testing.T) {
	err := ProvisioningPolicy{Accept: []MaterialDelivery{MaterialDeliveryMounted, MaterialDeliveryMounted}}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repeats mounted")
}

// The slot an engine writes has three states, and the undecided one is what
// the registration gate reads. Absence carries a reason that reads back.
func TestProvisioningPolicy_DeclaredSlotDistinguishesAbsentFromUndecided(t *testing.T) {
	var undecided Declared[ProvisioningPolicy]
	assert.False(t, undecided.Decided())
	assert.Empty(t, undecided.AbsentReason())

	absent := Absent[ProvisioningPolicy]("fixture authenticates against nothing")
	assert.True(t, absent.Decided())
	_, ok := absent.Get()
	assert.False(t, ok)
	assert.Equal(t, "fixture authenticates against nothing", absent.AbsentReason())

	provided := Provide(ProvisioningPolicy{Accept: []MaterialDelivery{MaterialDeliveryMounted}})
	got, ok := provided.Get()
	require.True(t, ok)
	assert.Equal(t, []MaterialDelivery{MaterialDeliveryMounted}, got.Accept)
	assert.Empty(t, provided.AbsentReason())
}
