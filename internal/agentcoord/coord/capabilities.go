package coord

import (
	"sort"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The capability vocabulary the handshake advertises (Hello.capabilities /
// HelloAck.capabilities). Capabilities are DISCOVERY, not permission: they say
// what an endpoint can execute so the other end can route, and they are
// per-Hello — that is, per RUN — so a resumed harp may advertise differently
// from the run before it.
const (
	// CapPeerMessaging is the mailbox surface every runner has, engine or not.
	CapPeerMessaging = "peer_messaging"
	// CapSteer through CapResume are the plane-2 control request kinds a runner
	// hosting a StructuredChat engine can execute.
	CapSteer     = "steer"
	CapQuestion  = "question"
	CapSummarize = "on_demand_summary"
	CapPause     = "pause"
	CapResume    = "resume"
	// CapTerminalDelivery says this runner has NO turn machinery: no engine
	// host, no turn sink, so nothing on its side will ever pull mail on its
	// own. It is the session owner's advertisement, and it is what tells
	// pushMail that the ordinary "unparked, so its own turn boundary owns the
	// delivery" assumption does not hold here — for this runner the push IS
	// the delivery, because the push is what fires the terminal nudge.
	CapTerminalDelivery = "terminal_delivery"
)

// ControlCapabilities are the five plane-2 control kinds, in the order the
// contract lists them.
func ControlCapabilities() []string {
	return []string{CapSteer, CapQuestion, CapSummarize, CapPause, CapResume}
}

// RunnerCapabilities is one runner's whole advertisement. The control kinds ride
// only when the runner actually hosts an engine that could execute them:
// advertising a kind nothing can run turns the send-side check into a rubber
// stamp and pushes the failure to the far end, which is the experience the check
// exists to replace.
func RunnerCapabilities(hostsEngine bool) []string {
	caps := []string{CapPeerMessaging}
	if hostsEngine {
		caps = append(caps, ControlCapabilities()...)
	} else {
		// No engine host is precisely the session owner: the run that drives a
		// terminal rather than being driven structurally. It advertises that
		// its mail must be pushed even while unparked, because there is no
		// turn boundary behind which the delivery could otherwise happen.
		caps = append(caps, CapTerminalDelivery)
	}
	return caps
}

// runCapability checks one plane-2 request kind against the target run's Hello
// advertisement before anything is queued. Its jobs are FAIL-FAST and ROUTING:
// the initiator learns at command time, with the gap and the advertisement both
// named, instead of waiting on a wire UNIMPLEMENTED or an answer that never
// arrives — and a caller with a fallback learns now that it needs one.
//
// It is deliberately NOT the guarantee. The receiver's own refusal is: the
// runner rejects kinds it did not advertise, at the boundary that actually
// protects its engine, and its advertisement is per-run — so only the far end,
// at delivery time, knows what it can currently do. Absence of an
// advertisement is never read as availability.
func (c *Coordinator) runCapability(harp, capability string) error {
	c.mu.Lock()
	ch := c.chans[harp]
	var advertised []string
	if ch != nil {
		if ch.caps[capability] {
			c.mu.Unlock()
			return nil
		}
		advertised = make([]string, 0, len(ch.caps))
		for name := range ch.caps {
			advertised = append(advertised, name)
		}
	}
	c.mu.Unlock()

	if ch == nil {
		return status.Errorf(codes.FailedPrecondition,
			"run %q has no attached run channel, so it has advertised no capabilities and %q is unavailable "+
				"(capabilities ride each run's Hello; an unattached or ended run has none)", harp, capability)
	}
	if len(advertised) == 0 {
		return status.Errorf(codes.FailedPrecondition,
			"run %q does not offer %q: it advertised no capabilities at all "+
				"(capabilities are per-run — a resumed run may advertise differently)", harp, capability)
	}
	sort.Strings(advertised)
	return status.Errorf(codes.FailedPrecondition,
		"run %q does not offer %q; it advertised: %s "+
			"(capabilities are per-run — a resumed run may advertise differently)",
		harp, capability, strings.Join(advertised, ", "))
}
