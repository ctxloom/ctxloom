package isolation

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusingProbe is the NEGATIVE CONTROL. Unprivileged user namespaces cannot
// be disabled on the hosts this suite runs on, so the real probe can only ever
// answer yes here — and a capability probe that has never been seen to say no
// is a probe nobody has any reason to believe. This is what makes the
// "unsupported" path reachable at all.
func refusingProbe(reason string) func(context.Context, string) error {
	return func(context.Context, string) error { return errors.New(reason) }
}

func permittingProbe() func(context.Context, string) error {
	return func(context.Context, string) error { return nil }
}

// A probe that says NO must be BELIEVED, and Select must then walk on to the
// next DECLARED acceptance rather than using the mechanism anyway or giving
// up. This is the negative control's whole purpose: it proves the detector can
// report absence, and that reporting absence changes the outcome.
func TestSelect_AProbeThatSaysNoIsBelievedAndTheNextAcceptanceIsUsed(t *testing.T) {
	p, delivery, err := Select(context.Background(),
		ProvisioningPolicy{Accept: []Delivery{DeliveryMounted, DeliveryReplicated}},
		SharingShared,
		WithNamespaceProbe(refusingProbe("unprivileged user namespaces are disabled by policy on this host")),
		WithProvisionScratch(t.TempDir()),
	)
	require.NoError(t, err)
	assert.Equal(t, DeliveryReplicated, delivery,
		"the mount probe refused, so the declared fallback — replication — is what the caller must end up on")
	assert.Equal(t, replicationMechanism, p.Mechanism())
	assert.Equal(t, DeliveryReplicated, p.Delivery(),
		"the provisioner must report the delivery it actually gives, not the one that was asked for first")
}

// When NOTHING can serve the demand the answer is a defined terminal that
// NAMES EVERY CANDIDATE TRIED AND WHY. "Why did I end up here?" has to be
// answerable from the failure itself, not by reading the selection code.
func TestSelect_RefusesNamingEveryCandidateAndItsReason(t *testing.T) {
	_, delivery, err := Select(context.Background(),
		ProvisioningPolicy{Accept: []Delivery{DeliveryMounted, DeliveryReplicated}},
		SharingShared,
		WithNamespaceProbe(refusingProbe("userns denied by apparmor")),
		WithReplicationProbe(refusingProbe("inotify instance limit reached")),
		WithProvisionScratch(t.TempDir()),
	)
	require.Error(t, err)
	assert.Equal(t, DeliveryUnset, delivery)

	var refusal *SelectionRefusal
	require.ErrorAs(t, err, &refusal, "the terminal must be a typed refusal callers can inspect, not an opaque error")
	require.Len(t, refusal.Tried, 3, "all three discrete implementations must be tried and reported")

	msg := err.Error()
	for _, want := range []string{
		containerMountMechanism, namespaceMountMechanism, replicationMechanism,
		"not containerised", "userns denied by apparmor", "inotify instance limit reached",
		"mounted", "replicated",
	} {
		assert.Contains(t, msg, want, "the refusal must name every candidate tried and why each was rejected")
	}
}

// A candidate must SATISFY the demand. A mount delivers shared-by-identity and
// cannot deliver private material, so a private demand is REFUSED — handing
// back a shared delivery because it was the only one available is the exact
// silent substitution this design exists to forbid.
func TestSelect_RefusesRatherThanSubstitutingAWrongSharing(t *testing.T) {
	_, _, err := Select(context.Background(),
		ProvisioningPolicy{Accept: []Delivery{DeliveryMounted, DeliveryReplicated}},
		SharingPrivate,
		WithNamespaceProbe(permittingProbe()),
		WithReplicationProbe(permittingProbe()),
		WithContainerHome("/root"),
		WithProvisionScratch(t.TempDir()),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not deliver private material",
		"a mechanism that can run but delivers the wrong sharing must be rejected for THAT reason, not silently used")
	assert.Contains(t, err.Error(), "no declared provisioner can deliver private material")
}

// An engine that declared no acceptance has not made a decision, and Select
// must not make one for it.
func TestSelect_RefusesAnEmptyDeclaredAcceptance(t *testing.T) {
	_, _, err := Select(context.Background(), ProvisioningPolicy{}, SharingShared,
		WithProvisionScratch(t.TempDir()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declared no acceptance at all")
}

// SharingUnset is nobody's decision. Both guesses fail silently — a private
// credential that cannot renew, or a shared one the caller believed isolated —
// so it is refused rather than defaulted.
func TestSelect_RefusesAnUndeclaredSharing(t *testing.T) {
	_, _, err := Select(context.Background(),
		ProvisioningPolicy{Accept: []Delivery{DeliveryMounted}}, SharingUnset,
		WithProvisionScratch(t.TempDir()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no sharing was demanded")
}

// Preference order is the ENGINE'S, declared in advance, and the code only
// walks it. With a container home present the declarative implementation wins
// even though the imperative one would also have worked.
func TestSelect_HonoursDeclaredPreferenceOrder(t *testing.T) {
	p, delivery, err := Select(context.Background(),
		ProvisioningPolicy{Accept: []Delivery{DeliveryMounted, DeliveryReplicated}},
		SharingShared,
		WithContainerHome("/root"),
		WithNamespaceProbe(permittingProbe()),
		WithProvisionScratch(t.TempDir()),
	)
	require.NoError(t, err)
	assert.Equal(t, DeliveryMounted, delivery)
	assert.Equal(t, containerMountMechanism, p.Mechanism(),
		"container mounting is declared first among mount candidates when the run is containerised")
}

// Declaring only replication must not reach a mount even where mounting is
// available: acceptance is a statement about what the engine will take, and
// walking past it would make the declaration decorative.
func TestSelect_NeverUsesAnUndeclaredDelivery(t *testing.T) {
	p, delivery, err := Select(context.Background(),
		ProvisioningPolicy{Accept: []Delivery{DeliveryReplicated}},
		SharingShared,
		WithContainerHome("/root"),
		WithNamespaceProbe(permittingProbe()),
		WithProvisionScratch(t.TempDir()),
	)
	require.NoError(t, err)
	assert.Equal(t, DeliveryReplicated, delivery)
	assert.Equal(t, replicationMechanism, p.Mechanism())
}

// With NO seams injected, Select must run the REAL probes. Without this the
// negative-control tests above would pass against a default nobody ever wired
// to the live mechanism.
func TestSelect_UsesTheRealProbesWhenNoneAreInjected(t *testing.T) {
	scratch := t.TempDir()
	p, delivery, err := Select(context.Background(),
		ProvisioningPolicy{Accept: []Delivery{DeliveryMounted}}, SharingShared,
		WithProvisionScratch(scratch))
	if err != nil {
		var refusal *SelectionRefusal
		require.ErrorAs(t, err, &refusal)
		assert.Contains(t, err.Error(), "mountns",
			"the refusal must come from the real namespace probe, which is the only thing that could have rejected it here")
		t.Skipf("this host does not permit unprivileged user namespaces, which is a legitimate answer: %v", err)
	}
	assert.Equal(t, DeliveryMounted, delivery)
	assert.Equal(t, namespaceMountMechanism, p.Mechanism(),
		"no container home was given, so the only mount candidate left is the real namespace one")
}
