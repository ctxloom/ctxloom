package agent

// MCPNameArbiter is ctxloom's ONE decision about a managed-vs-user MCP
// server-name contest, in a single place every engine registry writer routes
// through.
//
// The contest: ctxloom derives a managed server name this round (from config,
// a bundle, a plugin, or its own well-known entry) and the engine's registry
// ALREADY holds an entry of that name that ctxloom did not write. The ruling
// is that ctxloom refuses the name — LOUDLY, one warning naming the file and
// the name — and writes every other managed server anyway: a single contest
// must not block the rest of the reconcile, and claiming the name would
// compound a silent overwrite into a silent deletion the next time the name
// leaves the managed set and the removal pass drops it.
//
// This is a WRITER-layer arbiter: it settles ctxloom-vs-user inside one
// engine's registry file. The different contest of two ctxloom source refs
// claiming one name is settled a layer up, when bundle MCP is resolved.
//
// Why it is a type and not a function: the ruling is stateful. Each contested
// name warns at most once per reconcile, and the names that WIN are exactly
// the set the writer records in its managed-content ledger, in claim order.
//
// The zero value is usable; every field is optional.
type MCPNameArbiter struct {
	// Present reports whether name is already in the registry. The writer
	// must have dropped its previously-managed names (its ledger, plus the
	// well-known ctxloom name) BEFORE the first Claim, so that anything
	// Present still reports is by construction an entry ctxloom never wrote.
	// A nil Present arbitrates nothing and every name wins — the posture for
	// a transient overlay that does no ledger reconcile.
	Present func(name string) bool
	// HandDeleted names entries the ledger claims but the registry no longer
	// holds: a human removed them since the last write. Claiming one is a
	// deliberate resurrection and warns, but still wins. Optional.
	HandDeleted map[string]bool
	// Label names the registry file in warnings (e.g. "opencode.json").
	Label string
	// Warn is the diagnostics sink; nil uses the package Warn. It never
	// fails the write.
	Warn func(format string, args ...any)

	claimed []string
	won     map[string]bool
	warned  map[string]bool
}

// Claim reports whether the writer may write name into the registry, and
// records the outcome. Repeating a name is safe and does not re-warn: a name
// that already won wins again (writers whose managed set is a slice can
// legitimately carry the same name twice, and the later value overwrites the
// earlier one exactly as an unarbitrated map assignment would), and a name
// that already lost loses again.
func (a *MCPNameArbiter) Claim(name string) bool {
	if a.won == nil {
		a.won = make(map[string]bool)
		a.warned = make(map[string]bool)
	}
	if a.won[name] {
		return true
	}
	if a.Present != nil && a.Present(name) {
		if !a.warned[name] {
			a.warned[name] = true
			a.warn("refusing to overwrite MCP server %q in %s: a hand-authored entry already uses this name and ctxloom did not create it; rename it in your config or bundle, or rename/remove the existing entry, to let ctxloom manage %q", name, a.Label, name)
		}
		return false
	}
	if a.HandDeleted[name] {
		a.warn("recreating MCP server %q in %s: it was removed by hand since the last write, but config, a bundle, or a plugin still declares it; remove it from there instead if you want ctxloom to stop managing it", name, a.Label)
	}
	a.won[name] = true
	a.claimed = append(a.claimed, name)
	return true
}

// Claimed returns, in first-claim order and without duplicates, the names
// Claim allowed — exactly the set the writer records in its ledger. A name
// the arbiter refused is deliberately absent: recording it would make the
// next reconcile delete a user's entry.
func (a *MCPNameArbiter) Claimed() []string { return a.claimed }

func (a *MCPNameArbiter) warn(format string, args ...any) {
	if a.Warn != nil {
		a.Warn(format, args...)
		return
	}
	Warn(format, args...)
}
