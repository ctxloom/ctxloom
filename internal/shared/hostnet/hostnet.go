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

// IsLocalAddr reports whether ip is assigned to one of this host's own network
// interfaces — whether a listener on it can bind here at all. An error means
// the host's addresses could not be listed, so the answer is unknown.
func IsLocalAddr(ip string) (bool, error) {
	want := net.ParseIP(ip)
	if want == nil {
		return false, nil
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false, err
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(want) {
			return true, nil
		}
	}
	return false, nil
}
