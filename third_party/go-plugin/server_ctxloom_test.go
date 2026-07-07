package plugin

import "testing"

// TestTCPBindHostFor_ContainerOnly0000 pins the ctxloom fork's bind-interface
// security gate: 0.0.0.0 (all interfaces) is bound ONLY for the deliberate
// container transport (PLUGIN_LISTEN_TCP=1 AND a pinned single port,
// MIN==MAX>0). A bare/ambient PLUGIN_LISTEN_TCP=1 on a host plugin server — which
// keeps go-plugin's default port RANGE (10000-25000, MIN!=MAX) — must stay on
// 127.0.0.1 so it never exposes an mTLS-less, all-interfaces listener. This is
// the unit-level analogue of the docker-gated TestContainerPolicy_TransportLoopbackTCP
// (which needs a live daemon): the container handshake env still pins MIN==MAX to
// the reserved port, so that integration path still binds 0.0.0.0 as required.
func TestTCPBindHostFor_ContainerOnly0000(t *testing.T) {
	cases := []struct {
		name      string
		listenTCP bool
		minPort   int64
		maxPort   int64
		want      string
	}{
		{"container pinned single port -> all interfaces", true, 54321, 54321, "0.0.0.0"},
		{"ambient listen-tcp, default range -> loopback only", true, 10000, 25000, "127.0.0.1"},
		{"ambient listen-tcp, zero MIN==MAX -> loopback only", true, 0, 0, "127.0.0.1"},
		{"listen-tcp off (native windows), pinned port ignored -> loopback", false, 54321, 54321, "127.0.0.1"},
		{"listen-tcp off, default range -> loopback", false, 10000, 25000, "127.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tcpBindHostFor(tc.listenTCP, tc.minPort, tc.maxPort); got != tc.want {
				t.Fatalf("tcpBindHostFor(%v, %d, %d) = %q, want %q",
					tc.listenTCP, tc.minPort, tc.maxPort, got, tc.want)
			}
		})
	}
}
