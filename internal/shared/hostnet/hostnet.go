// Package hostnet answers questions about this host's own network that more
// than one adapter asks: the coordinator listens on an answer, and the
// container runtime hands the same answer to the container that dials it.
package hostnet

import "net"

// PrimaryOutboundIP resolves the host's primary outbound interface IP with a
// connected UDP socket (no packets are sent), or "" when the host has no
// default route to pick a source address with.
func PrimaryOutboundIP() string {
	conn, err := net.Dial("udp", "203.0.113.1:9") // TEST-NET-3: never routed
	if err != nil {
		return ""
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return ""
}
