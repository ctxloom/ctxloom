package coordgrpc

import (
	"net"
	"sync"
)

// hostPeerListener refuses every connection whose peer is not one of this
// host's own addresses, closing it at accept — before a byte of HTTP/2 is
// read — and hands the rest to the server. It guards a listener bound on a
// LAN-visible address whose legitimate traffic is measured to originate on
// this host (present.Listen.HostPeersOnly): the kernel drops LAN packets that
// forge a local source address, so a foreign peer here is a real foreign host.
type hostPeerListener struct {
	net.Listener
	// isLocal reports whether ip is one of this host's own addresses
	// (hostnet.IsLocalAddr). An error is a refusal: when the host's addresses
	// cannot be listed, no peer can be shown to be local.
	isLocal func(ip string) (bool, error)
	// refused reports a refused peer's address; called at most once per
	// distinct address for the listener's life, so a scanner cannot flood the
	// log by reconnecting.
	refused func(remote string)

	mu   sync.Mutex
	seen map[string]bool
}

func newHostPeerListener(ln net.Listener, isLocal func(string) (bool, error), refused func(string)) *hostPeerListener {
	return &hostPeerListener{Listener: ln, isLocal: isLocal, refused: refused, seen: map[string]bool{}}
}

// Accept returns the next connection from a local peer; a foreign one is
// closed and the wait continues. It returns only the listener's own errors.
func (l *hostPeerListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip := peerIP(conn.RemoteAddr())
		if local, lerr := l.isLocal(ip); lerr == nil && local {
			return conn, nil
		}
		_ = conn.Close()
		l.noteRefused(ip)
	}
}

func (l *hostPeerListener) noteRefused(ip string) {
	l.mu.Lock()
	first := !l.seen[ip]
	l.seen[ip] = true
	l.mu.Unlock()
	if first && l.refused != nil {
		l.refused(ip)
	}
}

// peerIP is addr's IP without port or zone ("" when it has none, which no
// host address matches).
func peerIP(addr net.Addr) string {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.IP.String()
	}
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
