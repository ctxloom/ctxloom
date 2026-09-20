package coord

// The capability vocabulary the handshake advertises (Hello.capabilities /
// HelloAck.capabilities). Capabilities are DISCOVERY, not permission: they say
// what an endpoint can execute so the other end can route, and they are
// per-Hello — that is, per RUN — so a resumed harp may advertise differently
// from the run before it.
//
// "peer_messaging" is a RETIRED name and must not be reissued: mail rides
// files (the spool), every runner has that surface, and the string gated
// nothing — a receiver that reads it from an older peer must ignore it.
const (
	// CapTerminalDelivery says this runner has NO turn machinery: no engine
	// host, no turn sink, so nothing on its side will ever pull mail on its
	// own. It is the session owner's advertisement: its mail is delivered by
	// the terminal nudge (terminalinject.go), not by a turn boundary.
	CapTerminalDelivery = "terminal_delivery"
)

// RunnerCapabilities is one runner's whole advertisement.
func RunnerCapabilities(hostsEngine bool) []string {
	if hostsEngine {
		// An engine host is driven structurally; its turn boundary owns
		// delivery, and the mailbox surface needs no advertising.
		return nil
	}
	// No engine host is precisely the session owner: the run that drives a
	// terminal rather than being driven structurally.
	return []string{CapTerminalDelivery}
}
