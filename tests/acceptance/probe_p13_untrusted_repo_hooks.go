// Package acceptance: P13, UNTRUSTED REPO HOOKS — the capability ladder's rung
// for the vendor behaviour ctxloom's repo-trust design stands on.
//
// UNTAGGED, like probe_p12_permission_hook.go: every P13 cell is @live and
// paid, so the verdicts and the argv below are the only part of this rung a
// hermetic test can execute (probe_p13_untrusted_repo_hooks_test.go). The godog
// plumbing — gate, fixture, the one paid turn — lives in
// steps_p13_untrusted_repo_hooks.go behind the acceptance tag.
//
// WHAT THIS RUNG PINS, AND WHY ctxloom NEEDS IT. A repository can COMMIT a
// .claude/settings.json whose hooks run arbitrary commands. claude's own trust
// dialog does not stop them in `claude -p`: with no projects entry in
// CLAUDE_CONFIG_DIR/.claude.json — a repo nobody ever trusted — a committed
// PreToolUse hook and a SessionStart hook still execute. So ctxloom cannot lean
// on claude's trust flag to keep an untrusted repo's hooks out of a headless
// child; its repo-trust design (row crested-risotto) launches an untrusted
// repo's child with `--setting-sources user --strict-mcp-config`, which is
// what actually keeps project settings, and the hooks in them, from loading.
// Both halves are vendor behaviour that nothing in ctxloom's own tests can see:
//
//   - untrusted-fires: the hooks DO run in an untrusted repo under -p. If a
//     release starts honouring trust here, the premise moved and the design
//     should be re-examined (it would be over-cautious, not unsafe).
//   - setting-sources-suppresses: the same repo and turn, plus those two flags,
//     runs NO repo hook — while the echo still runs, so the hooks were
//     suppressed rather than never triggered. If this goes red, an untrusted
//     repo's hooks reach ctxloom's children.
//
// This rung is the checked copy of both claims, re-run on every pin bump.
//
// THE CELL RUNS THE VENDOR BINARY DIRECTLY, not through ctxloom, for P12's
// reason: the claim is about claude, so a red must name claude alone. The
// permission that lets `echo hi` run comes from --settings (flag scope), which
// --setting-sources does not filter, so the echo runs in both cells and only
// the repo's own settings differ in whether they load.
//
// EVERY ASSERTION IS STRUCTURED: stream-json frames and marker files on disk,
// never the model's prose.
package acceptance

import (
	"encoding/json"
	"fmt"
	"strings"
)

// p13Family is this rung's name in a skip line, a failure message and the
// evidence line.
const p13Family = "untrusted-repo-hooks"

// p13Variant is the cell's posture; the two arms are two addressable cells.
type p13Variant string

const (
	p13Fires      p13Variant = "untrusted-fires"
	p13Suppresses p13Variant = "setting-sources-suppresses"
)

// The turn both cells take: a prompt whose one Bash call the flag-scope
// settings allow, so the call runs without asking and the PreToolUse hook has
// a call to fire on.
const (
	p13Prompt           = "run echo hi"
	p13FlagSettings     = `{"permissions":{"allow":["Bash(echo hi)"]}}`
	p13EchoOutput       = "hi"
	p13RepoSettingsPath = ".claude/settings.json" // the repo's committed project settings, relative to the repo
)

// The two hook events the repo commits, and the marker each writes. Both
// markers live in the cell's directory, OUTSIDE the repo.
const (
	p13PreToolEvent      = "PreToolUse"
	p13SessionStartEvent = "SessionStart"
	p13PreToolMarker     = "pretooluse-marker"
	p13SessionMarker     = "sessionstart-marker"
)

// p13Markers is every marker the repo's hooks write, keyed by the hook event
// that writes it. The verdict walks this, so a hook added here is asserted in
// both arms without another edit.
var p13Markers = map[string]string{
	p13PreToolEvent:      p13PreToolMarker,
	p13SessionStartEvent: p13SessionMarker,
}

// p13Args is the cell's whole argv after the binary. The suppressing arm adds
// exactly the flags ctxloom's repo-trust design launches an untrusted repo
// with; nothing else differs between the arms.
func p13Args(v p13Variant) []string {
	args := []string{
		"-p", p13Prompt,
		"--settings", p13FlagSettings,
		"--output-format", "stream-json", "--verbose",
		"--model", liveClaudeModel,
	}
	if v == p13Suppresses {
		args = append(args, "--setting-sources", "user", "--strict-mcp-config")
	}
	return args
}

// p13RepoSettingsJSON renders the repo's committed .claude/settings.json: one
// hook per p13Markers event, each touching its marker under dir. The PreToolUse
// matcher is the gated tool; SessionStart takes no matcher.
func p13RepoSettingsJSON(dir string) ([]byte, error) {
	hooks := map[string][]p12HookMatcher{}
	for event, marker := range p13Markers {
		m := p12HookMatcher{Hooks: []p12HookCommand{{Type: "command", Command: "touch " + p12ShellQuote(dir+"/"+marker)}}}
		if event == p13PreToolEvent {
			m.Matcher = p12GatedTool
		}
		hooks[event] = []p12HookMatcher{m}
	}
	return json.Marshal(p12Settings{Hooks: hooks})
}

// --- one cell's observation ---------------------------------------------------

// p13Outcome is everything a P13 verdict may look at.
type p13Outcome struct {
	Cell     probeCellID
	Variant  p13Variant
	Started  bool
	TimedOut bool
	Run      probeRun
	// Fired maps each hook event to whether its marker exists after the run;
	// MarkerErr is a stat failure that is NOT plain absence.
	Fired     map[string]bool
	MarkerErr error
}

// The shapes this rung adds; shapeNotAttempted is P12's.
const (
	// shapeRepoHookSilent: an untrusted repo's committed hook did not run in
	// -p. claude's trust may now gate repo hooks; the premise of ctxloom's
	// repo-trust design moved.
	shapeRepoHookSilent probeShape = "REPO-HOOK-SILENT failure"
	// shapeRepoHookLeaked: a committed repo hook ran despite --setting-sources
	// user. ctxloom's suppression of an untrusted repo's hooks does not hold.
	shapeRepoHookLeaked probeShape = "REPO-HOOK-LEAKED failure"
	// shapeEchoNotRun: the allowed echo did not run and print its output, so a
	// silent hook cannot be told apart from an untriggered one.
	shapeEchoNotRun probeShape = "ECHO-NOT-RUN failure"
)

func (o p13Outcome) verdict() probeVerdict {
	return probeVerdict{Family: p13Family, Cell: o.Cell, Channel: channelRepoHookMarker}
}

func (o p13Outcome) evidence() string {
	return fmt.Sprintf("\nfired=%v markerErr=%v\nexit=%d runErr=%v timedOut=%t\nstdout:\n%s\nstderr:\n%s",
		o.Fired, o.MarkerErr, o.Run.ExitCode, o.Run.Err, o.TimedOut, o.Run.Stdout, o.Run.Stderr)
}

// p13Assert judges one cell: the run completed, the allowed echo ran and
// printed, then the arm the cell's variant names.
func p13Assert(o p13Outcome) error {
	v := o.verdict()
	if !o.Started || o.TimedOut {
		return v.fail(shapeRunFailed, fmt.Sprintf("the claude run did not complete (started=%t timedOut=%t)", o.Started, o.TimedOut), o.evidence())
	}
	trimmed, err := v.ran(o.Run)
	if err != nil {
		return err
	}
	s, err := p12Decode(trimmed)
	if err != nil {
		return v.fail(shapeOutputFormat, err.Error(), o.evidence())
	}
	if !s.SawResult {
		return v.fail(shapeOutputFormat, "the stream carried no result frame, so the turn did not finish", o.evidence())
	}
	if len(s.GatedCalls) == 0 {
		return v.fail(shapeNotAttempted, "the model made no "+p12GatedTool+" tool_use, so the PreToolUse hook had nothing to fire on and the cell measured nothing", o.evidence())
	}
	if !p13EchoRan(s) {
		return v.fail(shapeEchoNotRun, fmt.Sprintf("no %s tool_result printed a line %q without error", p12GatedTool, p13EchoOutput), o.evidence())
	}
	if o.MarkerErr != nil {
		return v.fail(shapeRunFailed, fmt.Sprintf("a hook marker could not be checked: %v", o.MarkerErr), o.evidence())
	}

	var fired, silent []string
	for event := range p13Markers {
		if o.Fired[event] {
			fired = append(fired, event)
		} else {
			silent = append(silent, event)
		}
	}
	switch o.Variant {
	case p13Fires:
		if len(silent) > 0 {
			return v.fail(shapeRepoHookSilent, fmt.Sprintf("the untrusted repo's committed %v hook(s) did not run under -p", silent), o.evidence())
		}
		return nil
	case p13Suppresses:
		if len(fired) > 0 {
			return v.fail(shapeRepoHookLeaked, fmt.Sprintf("the repo's committed %v hook(s) ran despite --setting-sources user --strict-mcp-config", fired), o.evidence())
		}
		return nil
	}
	return fmt.Errorf("%s %s: unknown variant %q (want %q or %q)", p13Family, o.Cell, o.Variant, p13Fires, p13Suppresses)
}

// p13EchoRan reports whether any gated call's tool_result succeeded and
// printed the echo's output as a line of its own — a line, not a substring,
// because a shell's startup noise can contain those two letters.
func p13EchoRan(s p12Stream) bool {
	for _, call := range s.GatedCalls {
		res, ok := s.Results[call]
		if !ok || res.IsError {
			continue
		}
		for _, line := range strings.Split(res.Text, "\n") {
			if strings.TrimSpace(line) == p13EchoOutput {
				return true
			}
		}
	}
	return false
}
