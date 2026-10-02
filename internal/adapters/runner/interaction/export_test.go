package interaction

// WithServeGate returns e with gate run at the head of its serve goroutine,
// before http.Server.Serve registers the listener, so a test can hold the
// endpoint in the window where Shutdown alone does not own the listener.
func WithServeGate(e Endpoint, gate func()) Endpoint {
	e.serveGate = gate
	return e
}
