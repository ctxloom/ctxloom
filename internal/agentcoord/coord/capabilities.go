package coord

// The capability vocabulary the handshake advertises (Hello.capabilities /
// HelloAck.capabilities). Capabilities are DISCOVERY, not permission: they say
// what an endpoint can execute so the other end can route, and they are
// per-Hello — that is, per RUN — so a resumed harp may advertise differently
// from the run before it.
const (
	// CapPeerMessaging is the messaging surface every runner has, engine or
	// not.
	CapPeerMessaging = "peer_messaging"
	// CapTerminalDelivery says this runner has NO turn machinery: no engine
	// host, no turn sink, so nothing on its side will ever pull mail on its
	// own. It is the session owner's advertisement: its mail is delivered by
	// the terminal nudge (terminalinject.go), not by a turn boundary.
	CapTerminalDelivery = "terminal_delivery"
)

// RunnerCapabilities is one runner's whole advertisement.
func RunnerCapabilities(hostsEngine bool) []string {
	caps := []string{CapPeerMessaging}
	if !hostsEngine {
		// No engine host is precisely the session owner: the run that drives a
		// terminal rather than being driven structurally.
		caps = append(caps, CapTerminalDelivery)
	}
	return caps
}
