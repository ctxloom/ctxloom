// Package acceptance: P17, INLINE SETTINGS — the capability ladder's rung for
// whether an inline-JSON --settings document applies ALONGSIDE the user
// settings in claude's config home rather than instead of them (conformance
// cell S1).
//
// UNTAGGED, like probe_p12_permission_hook.go: the cell is @live and paid, so
// the verdict and the fixture's wire below are the only part of this rung a
// hermetic test can execute (probe_p17_inline_settings_test.go). The godog
// plumbing lives in steps_p17_inline_settings.go behind the acceptance tag.
//
// WHAT THIS RUNG PINS. A child's settings come from two places at once:
// ctxloom's hooks live in the session home, claude's USER source; the turn's
// posture (permission rules, defaultMode) arrives as an inline JSON document
// on --settings (streamJSONDriver.argv, turnSettings). The design needs both
// to apply on the same turn. If the inline document REPLACED the user
// settings, every child would lose ctxloom's hooks — its approval hook among
// them — the moment it was given a posture; if claude ignored inline JSON,
// the posture would never reach the child.
//
// The cell puts the marker hooks P13 uses and one allow rule in the config
// home's settings.json, a second allow rule (and a defaultMode, as the
// posture document carries) inline, and asks for two commands each allowed
// by exactly one source, under --setting-sources user as an untrusted child
// runs. Green means both rules applied and every home hook fired.
package acceptance

import (
	"encoding/json"
	"fmt"
)

// p17Family is this rung's name in a skip line, a failure message and the
// evidence line.
const p17Family = "inline-settings"

// The two commands and the rule that allows each. Neither source allows the
// other's command, so each output is evidence for exactly one source.
const (
	p17InlineOutput = "hi"
	p17HomeOutput   = "bye"
	p17InlineAllow  = "Bash(echo " + p17InlineOutput + ")"
	p17HomeAllow    = "Bash(echo " + p17HomeOutput + ")"
	p17Prompt       = "Use the Bash tool twice, as two separate calls. First run exactly: echo " + p17InlineOutput + " — then run exactly: echo " + p17HomeOutput
	// p17InlineSettings is the --settings argument: JSON, not a path, shaped
	// like the per-turn posture document.
	p17InlineSettings = `{"permissions":{"defaultMode":"default","allow":["` + p17InlineAllow + `"]}}`
	// p17HomeSettingsName is the user settings file inside CLAUDE_CONFIG_DIR.
	p17HomeSettingsName = "settings.json"
)

// p17Settings is the settings shape both sources share.
type p17Settings struct {
	Permissions p17Permissions              `json:"permissions"`
	Hooks       map[string][]p12HookMatcher `json:"hooks,omitempty"`
}

type p17Permissions struct {
	DefaultMode string   `json:"defaultMode,omitempty"`
	Allow       []string `json:"allow"`
}

// p17HomeSettingsJSON renders the config home's settings.json: P13's marker
// hooks, touching their markers under dir, and the home's allow rule.
func p17HomeSettingsJSON(dir string) ([]byte, error) {
	return json.Marshal(p17Settings{Permissions: p17Permissions{Allow: []string{p17HomeAllow}}, Hooks: p13MarkerHooks(dir)})
}

// p17Args is the cell's argv after the binary.
func p17Args() []string {
	return []string{"-p", p17Prompt, "--setting-sources", "user", "--settings", p17InlineSettings,
		"--output-format", "stream-json", "--verbose", "--model", liveClaudeModel}
}

// p17Outcome is everything a P17 verdict may look at.
type p17Outcome struct {
	Cell     probeCellID
	Started  bool
	TimedOut bool
	Run      probeRun
	// Fired maps each p13Markers event to whether its marker exists after the
	// run; MarkerErr is a stat failure that is NOT plain absence.
	Fired     map[string]bool
	MarkerErr error
}

// The shapes this rung adds; shapeNotAttempted is P12's.
const (
	// shapeInlineSettingsIgnored: the inline JSON's allow rule did not apply.
	shapeInlineSettingsIgnored probeShape = "INLINE-SETTINGS-IGNORED failure"
	// shapeHomeSettingsDropped: with the inline document present, a hook or
	// rule from the config home's user settings did not apply.
	shapeHomeSettingsDropped probeShape = "HOME-SETTINGS-DROPPED failure"
)

func (o p17Outcome) verdict() probeVerdict {
	return probeVerdict{Family: p17Family, Cell: o.Cell, Channel: channelSettingsSources}
}

func (o p17Outcome) evidence() string {
	return fmt.Sprintf("\nfired=%v markerErr=%v\nexit=%d runErr=%v timedOut=%t\nstdout:\n%s\nstderr:\n%s",
		o.Fired, o.MarkerErr, o.Run.ExitCode, o.Run.Err, o.TimedOut, o.Run.Stdout, o.Run.Stderr)
}

// p17Assert judges the cell.
func p17Assert(o p17Outcome) error {
	v := o.verdict()
	s, err := decodeGatedRun(v, gatedRun{
		Started: o.Started, TimedOut: o.TimedOut, Run: o.Run, Evidence: o.evidence(),
		NoResult: "the stream carried no result frame, so the turn did not finish",
		NoCall:   "the model made no " + p12GatedTool + " tool_use, so no rule was consulted and the cell measured nothing",
	})
	if err != nil {
		return err
	}
	if o.MarkerErr != nil {
		return v.fail(shapeRunFailed, fmt.Sprintf("a hook marker could not be checked: %v", o.MarkerErr), o.evidence())
	}
	if !gatedCallPrinted(s, p17InlineOutput) {
		return v.fail(shapeInlineSettingsIgnored, fmt.Sprintf("no %s call printed %q, which only the inline --settings rule %s allows", p12GatedTool, p17InlineOutput, p17InlineAllow), o.evidence())
	}
	if !gatedCallPrinted(s, p17HomeOutput) {
		return v.fail(shapeHomeSettingsDropped, fmt.Sprintf("no %s call printed %q, which only the config home's rule %s allows", p12GatedTool, p17HomeOutput, p17HomeAllow), o.evidence())
	}
	if _, silent := p13Split(p13Markers, o.Fired); len(silent) > 0 {
		return v.fail(shapeHomeSettingsDropped, fmt.Sprintf("the config home's %v hook(s) did not fire alongside the inline --settings", silent), o.evidence())
	}
	return nil
}
