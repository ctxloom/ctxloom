package coord

// ContextUsage is how much of its model's context window a harp's
// conversation fills, as its engine reported at the end of its latest turn:
// Used tokens out of Window (zero: not reported).
type ContextUsage struct {
	Used   uint64
	Window uint64
}

// RecordContextUsage notes the context usage the run on ch reported — the
// RunChannel heartbeat a runner sends as each turn completes. It is a gauge,
// not state: no fact journals it, so a restarted coordinator shows none for a
// harp until that harp's next turn. Keyed by harp, the conversation the
// usage measures, so a harp driven one run per turn keeps one entry.
func (c *Coordinator) RecordContextUsage(ch *RunChannel, u ContextUsage) {
	harp := ch.id.Harp
	if harp == "" || (u.Used == 0 && u.Window == 0) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.contextUse == nil {
		c.contextUse = make(map[string]ContextUsage)
	}
	c.contextUse[harp] = u
}

// contextUsage is harp's last reported context usage (zero: none).
func (c *Coordinator) contextUsage(harp string) ContextUsage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.contextUse[harp]
}
