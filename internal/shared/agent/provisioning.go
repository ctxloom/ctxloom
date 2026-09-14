package agent

import (
	"errors"
	"fmt"
)

// Instance-home MATERIAL provisioning, as an ENGINE DECLARES it.
//
// An instance home is a private per-session engine home, and something has to
// put the material the engine needs INTO it. There is more than one way to do
// that and they are NOT interchangeable — a mount shares one inode, a
// replicator keeps two files in step with a window between them, and a copy
// cannot renew at all. Which of those an engine will accept is a fact ABOUT
// THE ENGINE, so the engine declares it and the isolation machinery only
// walks what it declared.
//
// These types live here, beside EngineHome and CredentialSeed, for the reason
// stated in enginefacts.go: an engine package must be able to author its own
// facts without linking the isolation machinery. internal/lm/isolation aliases
// them, so there is one enum and not a copy on each side of that boundary.

// MaterialDelivery is the GUARANTEE a provisioning mechanism provides. It is the
// engine-facing half of the vocabulary: an engine states which guarantees it
// will accept, never which implementation runs.
//
// It is MaterialDelivery and not Delivery because this package already has a
// Delivery: the interface a loadout surface implements to write itself. The
// two are unrelated and the collision is the evidence — this one is about
// MATERIAL reaching an instance home, and the name says so.
//
// Shared-by-identity and shared-by-replication are both "shared" and they FAIL
// DIFFERENTLY, so they are separate values. One value covering both would hide
// exactly the difference an auth-failure investigation turns on.
type MaterialDelivery int

const (
	// MaterialDeliveryUnset is nobody's decision. It is the zero value precisely so
	// that an omission is loud rather than plausible: a policy that named it
	// is refused rather than read as some default.
	MaterialDeliveryUnset MaterialDelivery = iota
	// MaterialDeliveryMounted is shared BY IDENTITY: the instance and the host see one
	// inode, so there is nothing to synchronise and no window in which they
	// disagree.
	MaterialDeliveryMounted
	// MaterialDeliveryReplicated is shared BY REPLICATION: two files kept in step by a
	// watcher under a cross-process lock. Eventual, with a rotation window —
	// if one instance consumes a single-use refresh token before replication
	// has delivered the successor, another instance presents the spent one and
	// THE SERVER rejects it. Strictly better than material that provably
	// cannot renew, and strictly worse than a mount.
	MaterialDeliveryReplicated
	// MaterialDeliveryAbsent is the engine keeping no material that needs provisioning
	// at all. It is NOT a value an engine puts in Accept — an engine with
	// nothing to provision declares the whole policy slot Absent, with the
	// reason — and it exists here so the machinery can report that state.
	MaterialDeliveryAbsent
)

// String renders the delivery for a diagnostic.
func (d MaterialDelivery) String() string {
	switch d {
	case MaterialDeliveryMounted:
		return "mounted"
	case MaterialDeliveryReplicated:
		return "replicated"
	case MaterialDeliveryAbsent:
		return "absent"
	case MaterialDeliveryUnset:
		return "unset"
	default:
		return fmt.Sprintf("MaterialDelivery(%d)", int(d))
	}
}

// Decided reports whether d is a member of the enum other than Unset — the
// question a registration gate asks of each entry in a declared policy.
func (d MaterialDelivery) Decided() bool {
	switch d {
	case MaterialDeliveryMounted, MaterialDeliveryReplicated, MaterialDeliveryAbsent:
		return true
	default:
		return false
	}
}

// ProvisioningPolicy is what an ENGINE DECLARES it will accept, in preference
// order. Declaring it at the engine is what makes a fallback reviewable rather
// than silent: the descriptor states the chain and the selector only walks it,
// which is the difference between a degrade that was agreed and one that
// merely happened.
type ProvisioningPolicy struct {
	// Accept is the deliveries this engine will take, best first. An EMPTY
	// Accept is refused rather than defaulted: an engine that never said what
	// it would accept has not declared a policy, and picking one for it is the
	// silent decision this whole type exists to prevent. An engine with
	// nothing to provision declares the SLOT absent with a reason instead —
	// an empty Accept cannot say why it is empty.
	Accept []MaterialDelivery
}

// Validate refuses a policy the selector could act on only by guessing.
func (p ProvisioningPolicy) Validate() error {
	if len(p.Accept) == 0 {
		return errors.New("ProvisioningPolicy: Accept is empty; declare the deliveries this engine takes, best first, or declare the slot absent with the reason it has nothing to provision")
	}
	seen := map[MaterialDelivery]bool{}
	for i, d := range p.Accept {
		if !d.Decided() {
			return fmt.Errorf("ProvisioningPolicy: Accept[%d] is %s; an undeclared delivery must not be walked as if it named a mechanism", i, d)
		}
		if d == MaterialDeliveryAbsent {
			return errors.New("ProvisioningPolicy: Accept names absent; absence is not something to fall back TO — an engine with no material to provision declares the whole slot Absent, with the reason")
		}
		if seen[d] {
			return fmt.Errorf("ProvisioningPolicy: Accept repeats %s; a preference order that names one delivery twice has a second entry that can never be reached", d)
		}
		seen[d] = true
	}
	return nil
}
