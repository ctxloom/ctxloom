package isolation

// Instance-home MATERIAL provisioning.
//
// An instance home is a private per-session engine home. Something has to put
// the material the engine needs INTO it, and the credential is only the
// loudest example — arbitrary user-supplied files are its peers, which is why
// this is a material provisioner and not a credential one.
//
// WHY IT IS POLYMORPHIC. There is more than one way to put a file in an
// instance home, and they are not interchangeable:
//
//	mount              shared by IDENTITY      one inode, no window
//	watcher + sync     shared by REPLICATION   eventual, locked, has a window
//	copy               NOT SHARED              cannot renew at all
//
// The copy is what credentialseed.go does today, and it is the reason this
// exists: a copied credential has its single-use refresh token stripped, so it
// works until the access token expires and then that instance is stuck. It is
// not a weaker sharing mode, it is a different product with a fuse on it. This
// file does NOT delete that path — that is a later, ordered change — it builds
// the replacement alongside it.
//
// THE ROTATION WINDOW, stated here because it will bite someone. With a
// single-use refresh token: instance A refreshes, consuming T1 and receiving
// T2. If instance B refreshes before replication has delivered T2, B presents
// T1 and THE SERVER rejects it. No local locking closes that window — it is
// inherent to replication. A mount has no such window because there is one
// file and one refresh path. Replication is still far better than a credential
// that provably cannot renew, which is what makes it the right answer where
// mounting is impossible; it is NOT equal to mounting, and DeliveryReplicated
// is what keeps that difference visible to whoever debugs an auth failure a
// year from now.

// Sharing is what the DECLARER ASKS FOR. It says nothing about how the request
// is met — that is Delivery.
type Sharing int

const (
	// SharingUnset is nobody's decision. It is the zero value precisely so it
	// cannot be reached by leaving a field blank: a provisioner refuses it
	// rather than guessing, because both possible guesses are wrong in a way
	// that is silent (a private credential that cannot renew, or a shared one
	// the caller believed was isolated).
	SharingUnset Sharing = iota
	// SharingShared: the instance's writes REACH THE HOST. A token the engine
	// refreshes inside the instance rotates the host's copy too, which is the
	// only arrangement under which a long run can keep authenticating.
	SharingShared
	// SharingPrivate: the instance's writes STAY LOCAL. Correct for material
	// deliberately isolated from the real thing ("give the agent my settings,
	// don't let it touch the real ones"), and wrong for anything that must
	// renew.
	SharingPrivate
)

// String names the sharing for a diagnostic.
func (s Sharing) String() string {
	switch s {
	case SharingShared:
		return "shared"
	case SharingPrivate:
		return "private"
	default:
		return "unset"
	}
}

// Delivery is what the caller ACTUALLY GOT.
//
// It is a separate type from Sharing, not a restatement of it, because
// shared-by-identity and shared-by-replication are BOTH "shared" and they FAIL
// DIFFERENTLY — see the rotation window in this file's header. One enum value
// cannot cover both without hiding exactly the difference a failure
// investigation turns on.
type Delivery int

const (
	// DeliveryUnset is a Result nobody filled in. Like SharingUnset it is the
	// zero value so that an omission is loud rather than plausible.
	DeliveryUnset Delivery = iota
	// DeliveryMounted is shared BY IDENTITY: the instance and the host see one
	// inode, so there is nothing to synchronise and no window in which they
	// disagree. Both discrete mount implementations report this — a caller
	// cares that it got identity, not which kernel caller established it.
	DeliveryMounted
	// DeliveryReplicated is shared BY REPLICATION: two files kept in step by a
	// watcher, under a cross-process lock. Eventual, and with the rotation
	// window this file's header describes.
	DeliveryReplicated
	// DeliveryAbsent is a declared absence: the engine keeps no material that
	// needs provisioning at all, with a reason. Distinct from DeliveryUnset,
	// which is nobody having said anything.
	DeliveryAbsent
)

// String names the delivery for a diagnostic.
func (d Delivery) String() string {
	switch d {
	case DeliveryMounted:
		return "mounted"
	case DeliveryReplicated:
		return "replicated"
	case DeliveryAbsent:
		return "absent"
	default:
		return "unset"
	}
}

// Material is one thing that must appear inside an instance home.
type Material struct {
	// Host is the absolute host path holding the real material.
	Host string
	// DestRel is where it lands inside the instance home, in slash form.
	DestRel string
	// Sharing is what the declarer asks for. SharingUnset is refused.
	Sharing Sharing
	// ReadOnly asks that the instance not be able to write it at all. A
	// credential cannot be read-only — refreshing IS a write — so this is for
	// material the engine only consumes.
	ReadOnly bool
}

// Result is what a Provisioner did, reported back so its consumers can act on
// the delivery they actually got rather than the one they asked for.
type Result struct {
	// Delivery is how the material was placed. Never DeliveryUnset on a
	// successful Provision.
	Delivery Delivery
	// Mechanism names the discrete implementation that ran ("container-mount",
	// "namespace-mount", "replication"). Delivery answers "what guarantees do
	// I have?"; Mechanism answers "which code do I go read?" — and they are
	// deliberately not the same question, because two implementations share
	// DeliveryMounted.
	Mechanism string
	// ContainerMounts are DECLARATIVE: bind descriptors a container runtime is
	// asked to perform, which it may reject at container start. Populated only
	// by the container-mount implementation.
	ContainerMounts []Mount
	// NamespaceBinds are IMPERATIVE: binds this process performs itself, with
	// mount(2), inside a namespace it created. Populated only by the
	// namespace-mount implementation. They are a different field from
	// ContainerMounts rather than a shared one with a mode flag, because
	// handing either list to the other performer is a bug that must not
	// typecheck as ordinary.
	NamespaceBinds []NamespaceBind
	// stop tears down anything the provisioner left running (a replicator's
	// watchers and goroutine). nil when there is nothing to stop, which is the
	// case for both mount implementations — and is exactly the property that
	// makes them the better answer where they are available.
	stop func() error
}

// Close releases whatever the provisioning left running. Safe to call on a
// zero Result and safe to call twice.
func (r Result) Close() error {
	if r.stop == nil {
		return nil
	}
	return r.stop()
}

// Provisioner puts declared material into an instance home. Which one you get
// is Select's answer, never a call site's choice.
type Provisioner interface {
	// Mechanism names this implementation for diagnostics and for Result.
	Mechanism() string
	// Delivery is the guarantee this implementation provides.
	Delivery() Delivery
	// Can answers BEFORE it is asked to do anything, so a bad combination is
	// refused at CONFIG-WRITE time rather than discovered at launch. It is the
	// shape HasContainerAuth already has, and it is what makes Select's
	// acceptance predicate possible.
	Can(Sharing) bool
	// Provision places every material in instanceHome and reports what it did.
	// It fails rather than degrading: a material this implementation cannot
	// deliver as asked is an error naming the material and the reason, never a
	// quiet substitution of a weaker delivery.
	Provision(instanceHome string, materials []Material) (Result, error)
}
