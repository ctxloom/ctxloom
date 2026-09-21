package wire

// MergeHooksConfig merges src into dest. It is the nil-handling wrapper the
// host assembly, the profile resolver and the engine-side lifecycle all build
// on; the merge rule itself is HooksConfig.Append. A nil src is a legitimate
// no-op (nothing to merge). A nil DEST is not: it is the caller's own hook
// set, so with none there is nowhere for src to go and this signature has no
// way to refuse. The drop still happens — it cannot not — and the SIZE of
// what went missing is returned so the caller can say so on its own
// diagnostic channel rather than leave the session running with none of its
// configured hooks and nothing said.
//
// The same hook never runs twice in one event, whatever declared it:
// HooksConfig.Append dedupes on the hook's whole executable content scoped
// to the event. Do not reintroduce a plain concatenation here or beside it —
// a diamond (child parents [b, c], both parenting d) applied d's hook once
// per path before that landed.
func MergeHooksConfig(dest *HooksConfig, src *HooksConfig) (dropped int) {
	if src == nil {
		return 0
	}
	if dest == nil {
		return src.Count()
	}
	dest.Append(*src)
	return 0
}

// Count totals every hook the config carries — the unified lifecycles plus
// every engine-specific list — so a merge that cannot happen can report the
// SIZE of what it dropped rather than a bare "some hooks".
func (h *HooksConfig) Count() int {
	if h == nil {
		return 0
	}
	n := len(h.Unified.PreTool) + len(h.Unified.PostTool) +
		len(h.Unified.SessionStart) + len(h.Unified.SessionEnd) +
		len(h.Unified.PreShell) + len(h.Unified.PostFileEdit) + len(h.Unified.TurnEnd)
	for _, backend := range h.Ext {
		for _, hooks := range backend {
			n += len(hooks)
		}
	}
	return n
}
