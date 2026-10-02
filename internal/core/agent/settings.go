package agent

// SettingsReader reports what ctxloom has wired into an agent's settings
// files. It is the SETTINGS facet of an agent, deliberately separate from the
// launch facet (Backend). It only reads: every write to those files is a
// claim in the ownership record (delivery.Static), so there is one writer,
// and this is how a status report asks what it left there.
type SettingsReader interface {
	// Status reports which managed artifacts are currently wired in.
	Status(projectDir string) (SettingsStatus, error)
}

// SettingsStatus reports which managed artifacts an agent has wired into its
// settings files.
type SettingsStatus struct {
	SettingsExists bool // the agent's settings file is present
	HooksPresent   bool // at least one managed hook is configured
	StatusLine     bool // a managed statusline is configured
	MCPPresent     bool // at least one managed MCP server is configured
}

// Wired reports whether any managed artifact is present. SettingsExists is
// deliberately NOT a disjunct: a settings file ctxloom merely found is not a
// file ctxloom wired, so a removal that leaves the user's own settings.json in
// place still reports false.
//
// All nine of its call sites being in _test.go does not make it dead: it is a
// derived predicate whose consumer is a TEST SUITE — and the most important
// consumer is
// internal/engines/conformance, this repo's cross-agent contract check, whose
// post-removal assertion is precisely "nothing managed remains". Inlining the
// three-term OR into nine call sites would cost more lines than it saves, would
// restate the SettingsExists omission nowhere, and would have to be edited at
// all nine sites the day a fifth managed artifact is added. Pinned by
// TestSettingsStatus_Wired.
func (s SettingsStatus) Wired() bool {
	return s.HooksPresent || s.StatusLine || s.MCPPresent
}
