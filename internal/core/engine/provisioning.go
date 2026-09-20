package engine

import (
	"errors"
	"fmt"
)

// Session-home MATERIAL provisioning, as an ENGINE DECLARES it.
//
// A session home is a private per-session engine home, and something has to
// put the material the engine needs INTO it. There is more than one way to
// do that and they are NOT interchangeable — a mount shares one inode, a
// replicator keeps two files in step with a window between them, and a copy
// cannot renew at all. Which of those an engine will accept is a fact ABOUT
// THE ENGINE, so the engine declares it (CredentialSeed.Accept) and the
// isolation machinery only walks what it declared.

// MaterialDelivery is the GUARANTEE a provisioning mechanism provides. It is
// the engine-facing half of the vocabulary: an engine states which
// guarantees it will accept, never which implementation runs.
//
// Shared-by-identity and shared-by-replication are both "shared" and they
// FAIL DIFFERENTLY, so they are separate values. One value covering both
// would hide exactly the difference an auth-failure investigation turns on.
type MaterialDelivery int

const (
	// MaterialDeliveryUnset is the zero value: nobody declared anything.
	MaterialDeliveryUnset MaterialDelivery = iota
	// MaterialDeliveryMounted: the instance and the host share ONE inode.
	MaterialDeliveryMounted
	// MaterialDeliveryReplicated: two files kept in step by a watcher, with
	// a window between them.
	MaterialDeliveryReplicated
	// MaterialDeliveryAbsent: nothing was placed. A RESULT a provisioner may
	// report, never a preference an engine may state.
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

// Decided reports whether d names a real delivery (or the absent result),
// as opposed to the zero value nobody wrote.
func (d MaterialDelivery) Decided() bool {
	switch d {
	case MaterialDeliveryMounted, MaterialDeliveryReplicated, MaterialDeliveryAbsent:
		return true
	default:
		return false
	}
}

// validateAccept refuses an acceptance order the isolation machinery could
// not walk: empty, an unset entry, absence named as a fallback, a repeat.
func validateAccept(accept []MaterialDelivery) error {
	if len(accept) == 0 {
		return errors.New("CredentialSeed: Accept is empty; declare the deliveries this engine takes, best first")
	}
	seen := map[MaterialDelivery]bool{}
	for i, d := range accept {
		if !d.Decided() {
			return fmt.Errorf("CredentialSeed: Accept[%d] is %s; an undeclared delivery must not be walked as if it named a mechanism", i, d)
		}
		if d == MaterialDeliveryAbsent {
			return errors.New("CredentialSeed: Accept names absent; absence is not something to fall back TO — an engine with no material to provision declares no seed")
		}
		if seen[d] {
			return fmt.Errorf("CredentialSeed: Accept repeats %s; a preference order that names one delivery twice has a second entry that can never be reached", d)
		}
		seen[d] = true
	}
	return nil
}
