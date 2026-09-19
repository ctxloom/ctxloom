package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	claudeengine "github.com/ctxloom/ctxloom/internal/engines/claude/engine"
	"github.com/ctxloom/ctxloom/internal/lm/engine"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// composed is every descriptor the composition root installs — the shipped
// engine and the test doubles together. The doubles are NOT excluded: tests
// run the same provisioning path, so a double that declared nothing would be
// an undeclared hole in exactly the place this gate exists to close.
func composed() []engine.Descriptor {
	return append(MockDescriptors(), claudeengine.Descriptor())
}

// TestProvisioning_EveryComposedEngineDeclares is the settling assertion:
// every engine states what it accepts, or states that it has nothing to
// provision AND WHY. There is no third state that reaches registration.
func TestProvisioning_EveryComposedEngineDeclares(t *testing.T) {
	descs := composed()
	require.NotEmpty(t, descs)
	for _, d := range descs {
		t.Run(d.Name, func(t *testing.T) {
			require.True(t, d.Provisioning.Decided(),
				"%s declared no provisioning policy; Provide one or declare it Absent with the reason", d.Name)
			if policy, ok := d.Provisioning.Get(); ok {
				assert.NoError(t, policy.Validate())
				return
			}
			// A declared absence whose reason is empty is the omission the
			// Declared type exists to refuse; assert the reason READS BACK,
			// because that clause is what a report shows a user asking where
			// their credential went.
			assert.NotEmpty(t, d.Provisioning.AbsentReason(),
				"%s declares absence with no reason", d.Name)
		})
	}
}

// Every test double declares ABSENCE, not an empty policy: they authenticate
// against nothing, and "has nothing to provision, because X" must stay
// distinguishable from "nobody filled this in".
func TestProvisioning_TestDoublesDeclareAbsenceWithAReason(t *testing.T) {
	for _, d := range MockDescriptors() {
		t.Run(d.Name, func(t *testing.T) {
			require.True(t, d.Provisioning.Decided())
			_, provided := d.Provisioning.Get()
			assert.False(t, provided, "%s has no credential material, so it declares absence rather than a policy", d.Name)
			assert.Contains(t, d.Provisioning.AbsentReason(), d.Name,
				"the reason must name the engine it is about")
			assert.Contains(t, d.Provisioning.AbsentReason(), "authenticates against nothing")
		})
	}
}

// THE GATE. A descriptor complete in every other respect but silent about
// provisioning must not register, and the refusal must name the engine and
// the slot so the composition root's message says what to fix.
func TestRegister_UndeclaredProvisioningIsRefusedByName(t *testing.T) {
	d := enginefixture.Descriptor("u057-no-provisioning")
	d.Provisioning = agent.Declared[agent.ProvisioningPolicy]{}

	err := Register(d)

	require.Error(t, err, "an undeclared provisioning policy must not register")
	assert.Contains(t, err.Error(), "u057-no-provisioning")
	assert.Contains(t, err.Error(), "Provisioning is undeclared")
	assert.Contains(t, err.Error(), "Provide it or declare it Absent with the reason")
	assert.False(t, Exists("u057-no-provisioning"), "a refused descriptor must not be installed")
}

// An engine that DID fill the slot in but with an empty policy is refused
// too, at registration rather than at launch: an empty Accept is the
// undecided state wearing a struct that looks filled in.
func TestRegister_EmptyAcceptIsRefused(t *testing.T) {
	d := enginefixture.Descriptor("u057-empty-accept")
	d.Provisioning = agent.Provide(agent.ProvisioningPolicy{})

	err := Register(d)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "u057-empty-accept")
	assert.Contains(t, err.Error(), "Accept is empty")
	assert.False(t, Exists("u057-empty-accept"))
}
