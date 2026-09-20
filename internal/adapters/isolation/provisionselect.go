package isolation

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Selecting a Provisioner.
//
// THIS DELIBERATELY MIRRORS SelectRuntime rather than generalising it. The two
// are structurally the same — ordered candidates, a per-candidate capability
// PROBE, an ACCEPTANCE PREDICATE a candidate must SATISFY, and a defined
// terminal when none do — and a generic over both is the obvious next move.
// It is not made here on purpose. What is at risk in an extraction is not the
// loop, it is the acceptance predicate: a generic that flattened this into
// "try in order and take what you get" would silently reintroduce the
// substitution both mechanisms exist to forbid, AND would leave SelectRuntime's
// doc explaining a rule its own code no longer enforced — a retired rule still
// being read as live. Extract once both consumers exist and the shapes are
// PROVEN to match rather than assumed to.
//
// The one place this intentionally diverges from SelectRuntime is the
// terminal. SelectRuntime returns Host{} and lets its caller decide the
// consequence, because a host run is a real, useful thing. There is no
// equivalent here: an instance home with no material in it is not a degraded
// run, it is an engine that starts logged out. So the terminal is an ERROR,
// and that error names every candidate tried and why each was rejected —
// "why did I end up on replication?" has to be answerable from the failure
// itself, not by reading this file.

// CandidateRejection records one candidate Select tried and could not use.
type CandidateRejection struct {
	// Delivery is the declared acceptance this candidate was offered against.
	Delivery Delivery
	// Mechanism names the discrete implementation.
	Mechanism string
	// Reason is why it was rejected, in the words of whatever refused it — a
	// probe's errno, or the acceptance predicate's own verdict.
	Reason string
}

// SelectionRefusal is the defined terminal: no declared candidate could serve
// the demand. It is a refusal NAMING THE REASON, never a downgrade — a
// SharingShared demand on a platform that cannot honour it must not quietly
// become something else.
type SelectionRefusal struct {
	// Want is the sharing that was demanded.
	Want Sharing
	// Accept is the policy that was walked.
	Accept []Delivery
	// Tried is every candidate, in the order they were tried, with why each
	// was rejected. This is the whole point of the type.
	Tried []CandidateRejection
}

// Error names the demand and every candidate that failed it.
func (r *SelectionRefusal) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "no declared provisioner can deliver %s material", r.Want)
	if len(r.Accept) == 0 {
		b.WriteString(": the engine declared no acceptance at all, so there was nothing to try; an engine must state, in preference order, which deliveries it will take")
		return b.String()
	}
	accepted := make([]string, 0, len(r.Accept))
	for _, d := range r.Accept {
		accepted = append(accepted, d.String())
	}
	fmt.Fprintf(&b, " (declared acceptance, best first: %s)", strings.Join(accepted, ", "))
	if len(r.Tried) == 0 {
		b.WriteString("; no candidate was even constructed, which means the declared acceptance named nothing this build implements")
		return b.String()
	}
	b.WriteString("; tried:")
	for _, t := range r.Tried {
		fmt.Fprintf(&b, "\n  - %s (%s): %s", t.Mechanism, t.Delivery, t.Reason)
	}
	return b.String()
}

// provisionConfig is what Select was told about this run, plus the seams that
// let a test force an answer the host cannot be made to give.
type provisionConfig struct {
	// namespaceProbe answers whether this host permits an unprivileged
	// user+mount namespace with a file bind. THE SEAM IS THE POINT: the real
	// probe calls clone(2), and no host available to this project's tests
	// forbids it, so without an injectable answer the "unsupported" path is
	// untestable — and a capability probe that can only ever answer YES gets
	// believed, which is strictly worse than having none.
	namespaceProbe func(ctx context.Context, scratch string) error
	// replicationProbe answers whether a watcher-and-write-back replicator can
	// run here. Injectable for the same reason: proving the DEFINED TERMINAL
	// requires making every candidate fail, and one that cannot be made to
	// fail cannot appear in that test.
	replicationProbe func(ctx context.Context, scratch string) error
	// namespaceBindsPerformed is the caller DECLARING that something in this
	// run will perform the imperative binds a namespace mount emits — wrapping
	// the engine's own exec through mountns.Command — before the engine
	// starts. It is the exact twin of containerHome's question: not "can this
	// host do it?" (that is the probe) but "is there a performer in this run
	// at all?". Default false, and the default is the honest one: a caller
	// that emits NamespaceBinds nobody performs leaves empty mount targets in
	// the instance home and starts the engine logged out behind them, which is
	// worse than the refusal.
	namespaceBindsPerformed bool
	// containerHome is the path an engine's home has INSIDE the container, and
	// its presence is what says this run is containerised at all. Empty means
	// there is no container to emit mount descriptors for, which is a
	// rejection with a reason, not an error.
	containerHome string
	// scratch is where a probe may build throwaway files. Never the real
	// material.
	scratch string
}

// ProvisionOption adjusts Select. Options exist so a caller with nothing to
// say makes the plain call.
type ProvisionOption func(*provisionConfig)

// WithNamespaceProbe replaces the host-namespace capability probe. Its real
// purpose is the NEGATIVE CONTROL — see provisionConfig.namespaceProbe — and a
// production caller has no reason to pass it.
func WithNamespaceProbe(probe func(ctx context.Context, scratch string) error) ProvisionOption {
	return func(c *provisionConfig) { c.namespaceProbe = probe }
}

// WithReplicationProbe replaces the replication capability probe, for the same
// negative-control reason as WithNamespaceProbe.
func WithReplicationProbe(probe func(ctx context.Context, scratch string) error) ProvisionOption {
	return func(c *provisionConfig) { c.replicationProbe = probe }
}

// WithNamespaceBindsPerformed declares that this run will hand the Result's
// NamespaceBinds to a performer before the engine is exec'd. Without it the
// host-namespace-mount candidate is rejected with that as its reason — see
// provisionConfig.namespaceBindsPerformed for why silence is the safe default.
func WithNamespaceBindsPerformed() ProvisionOption {
	return func(c *provisionConfig) { c.namespaceBindsPerformed = true }
}

// WithContainerHome declares that this run is containerised and where the
// engine's home lives inside that container. Without it the container-mount
// candidate has no target path to emit and is rejected.
func WithContainerHome(home string) ProvisionOption {
	return func(c *provisionConfig) { c.containerHome = home }
}

// WithProvisionScratch sets the directory probes build throwaway files under.
func WithProvisionScratch(dir string) ProvisionOption {
	return func(c *provisionConfig) { c.scratch = dir }
}

// candidate is one constructible implementation: the delivery it claims, and a
// constructor that PROBES and either returns a usable Provisioner or says why
// not.
type candidate struct {
	mechanism string
	construct func(context.Context, *provisionConfig) (Provisioner, error)
}

// candidatesFor lists, for one declared Delivery, every discrete
// implementation that claims it, in preference order.
//
// DeliveryMounted has TWO candidates and they stay two. Container mounting and
// host-namespace mounting share a kernel mechanism and nothing else: one is
// DECLARATIVE (we emit descriptors, the container runtime performs them and
// reports its own failures at container start), the other IMPERATIVE (we
// perform mount(2) ourselves, inside a namespace we created, and failure is an
// errno in our own shim before the engine is exec'd). Their capability
// questions are not the same question either — container mounting is available
// whenever the runtime is; namespace mounting depends on unprivileged user
// namespaces, which are kernel- and policy-dependent and absent on macOS
// entirely — so neither can stand in for the other's answer. Collapsing them
// into one implementation with a mode flag would turn every one of those
// differences into a branch.
func candidatesFor(d Delivery) []candidate {
	switch d {
	case DeliveryMounted:
		return []candidate{
			{mechanism: containerMountMechanism, construct: newContainerMountProvisioner},
			{mechanism: namespaceMountMechanism, construct: newNamespaceMountProvisioner},
		}
	case DeliveryReplicated:
		return []candidate{
			{mechanism: replicationMechanism, construct: newReplicationProvisioner},
		}
	default:
		return nil
	}
}

// Select constructs the first declared candidate whose CONSTRUCTOR-TIME PROBE
// succeeds and which SATISFIES want, and returns it together with the delivery
// it actually provides.
//
// Probing in the constructor is deliberate: an unusable mechanism fails when
// it is built, not at first use, so a broken mechanism cannot be discovered
// halfway through a run with a live engine attached to it.
//
// A candidate must SATISFY the demand, exactly as in SelectRuntime. A
// SharingShared demand is never met by handing back something private, and a
// SharingPrivate demand is never met by handing back something shared — those
// are the two substitutions this whole design exists to forbid. When nothing
// satisfies it, the answer is *SelectionRefusal naming every candidate tried,
// never a near-miss.
// accept is what the ENGINE DECLARED it will take, in preference order
// (engine.CredentialSeed.Accept): the seed an engine writes and the order
// this walks are the same value, not two that agree.
func Select(ctx context.Context, accept []Delivery, want Sharing, opts ...ProvisionOption) (Provisioner, Delivery, error) {
	cfg := &provisionConfig{
		namespaceProbe:   defaultNamespaceProbe,
		replicationProbe: defaultReplicationProbe,
		scratch:          os.TempDir(),
	}
	for _, opt := range opts {
		opt(cfg)
	}
	refusal := &SelectionRefusal{Want: want, Accept: accept}
	if want == SharingUnset {
		// Not a candidate problem — nobody stated a demand, so there is no
		// predicate to satisfy and every candidate would "pass" vacuously.
		refusal.Tried = append(refusal.Tried, CandidateRejection{
			Mechanism: "(none)",
			Reason:    "no sharing was demanded; SharingUnset is nobody's decision and is refused rather than guessed at, because both guesses fail silently",
		})
		return nil, DeliveryUnset, refusal
	}
	for _, d := range accept {
		for _, c := range candidatesFor(d) {
			p, err := c.construct(ctx, cfg)
			if err != nil {
				refusal.Tried = append(refusal.Tried, CandidateRejection{
					Delivery: d, Mechanism: c.mechanism, Reason: err.Error(),
				})
				continue
			}
			if !p.Can(want) {
				refusal.Tried = append(refusal.Tried, CandidateRejection{
					Delivery: d, Mechanism: c.mechanism,
					Reason: fmt.Sprintf("this host can run it, but it does not deliver %s material", want),
				})
				continue
			}
			return p, p.Delivery(), nil
		}
	}
	return nil, DeliveryUnset, refusal
}
