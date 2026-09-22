package agent

// InputGate is an OPTIONAL backend capability: it reads the engine's OWN
// output to decide whether a write into the engine's stdin would land in a
// text field or be eaten as a keypress by a modal. A backend implements it
// only where somebody has MEASURED that engine's terminal behaviour; the host
// discovers support by type assertion (backend.(InputGate)) and REFUSES to
// inject when it is absent.
//
// Refusing rather than assuming is the entire point. A coordinator wake writes
// a reminder frame plus a carriage return into the engine's stdin, and a CR
// delivered while a modal is open is consumed as "confirm the highlighted
// option" — so an ungated wake can answer a decision nobody made, and the
// record afterwards shows a ruling the human never gave. An engine with no
// InputGate has given no signal at all, and no signal must DELAY a wake, never
// permit one.
//
// This is deliberately separate from the core Backend interface: a required
// method would break every backend, and knowing one's own modal state is a
// capability most engines simply lack.
type InputGate interface {
	// Observe is handed every byte the engine writes to its stdout, in order.
	// It sits on the passthrough write path, so it must not block and must
	// not retain p — implementations copy anything they need to keep.
	//
	// Implementations MUST tolerate a marker split across two calls: the
	// engine's writes are chunked by the pty, not by the escape sequences it
	// happens to emit, so a sequence can straddle a boundary. Missing one
	// misses in the OPEN direction, which is the direction that forges a
	// decision.
	Observe(p []byte)

	// AcceptingText reports whether the engine is currently accepting FREE
	// TEXT rather than a single keypress — false while a modal or a selection
	// prompt is displayed.
	//
	// It MUST fail closed: when the state has not been established, report
	// false. A wake withheld is recoverable (the mail stays buffered for the
	// next Recv); a wake that answers for the human is not.
	AcceptingText() bool
}
