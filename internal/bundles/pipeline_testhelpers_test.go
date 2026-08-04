package bundles

// Test-only pipeline constructors. Gating and form selection are process-stage
// policy, so a test that wants either builds the stage that carries it; these
// two spell the two shapes so the intent of each call site is visible.

// ungated wraps a reader in a pipeline that does NOT gate — the
// management/listing shape, and the right one for a test that is exercising
// resolution rather than trust.
func ungated(l *Loader, preferDistilled bool) *Pipeline {
	return NewPipeline(l, nil, preferDistilled)
}

// gatedPipe wraps a reader in a pipeline that decides with gate — the exposure
// shape.
func gatedPipe(l *Loader, gate ContentGate, preferDistilled bool) *Pipeline {
	return NewPipeline(l, gate, preferDistilled)
}
