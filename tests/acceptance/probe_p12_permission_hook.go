// Package acceptance: P12, the NO-HOST PERMISSION HOOK — the capability
// ladder's rung for the vendor contract ctxloom's approval route stands on.
//
// UNTAGGED, like probe_p4_plan_sentinel.go beside it: every P12 cell is @live
// and paid, so the verdicts below are the only part of this rung a hermetic
// test can execute (probe_p12_permission_hook_test.go). The godog plumbing —
// gate, fixture, the one paid turn — lives in steps_p12_permission_hook.go
// behind the acceptance tag.
//
// WHAT THIS RUNG PINS. In `claude -p` with NO --permission-prompt-tool, a tool
// call that needs permission makes claude AWAIT the PermissionRequest hook and
// HONOUR its decision JSON: behavior "allow" lets the call run; behavior
// "deny" blocks it with the tool_result "Permission denied by hook" and a
// result.permission_denials entry. ctxloom routes a headless child's approvals
// through exactly that hook (no permission host is passed), so if a claude
// release stops awaiting it or stops honouring it, every gated call a child
// makes is silently auto-denied — or, worse, allowed without anyone having
// answered.
//
// WHY A LIVE CELL AND NOT THE DOCUMENTATION. The documented contract is
// https://code.claude.com/docs/en/hooks#permissionrequest, and it is prose
// that nothing checks. It has also stated the opposite: an earlier revision of
// that page said PermissionRequest does not fire when -p auto-denies, and the
// claude-code CHANGELOG for 2.1.268 records "Fixed PermissionRequest hooks not
// firing in --print mode". A contract that has flipped once is the one a pin
// bump must re-measure rather than re-read.
//
// THE CELL RUNS THE VENDOR BINARY DIRECTLY, not through ctxloom, and that is
// the point rather than a shortcut: the claim is about claude, so the fixture
// is the smallest thing that puts it to claude — a throwaway CLAUDE_CONFIG_DIR
// and HOME, a fresh git repo as cwd, a --settings file outside that repo
// registering one PermissionRequest hook on Bash, and a prompt to `touch` a
// file outside the working directory (which is what forces the ask). A cell
// routed through ctxloom would measure ctxloom's hook writing as well, and a
// red could no longer say which side moved.
//
// EVERY ASSERTION IS STRUCTURED. The verdicts read stream-json frames, the
// hook's own captured stdin, the hook's marker file and the proof file on disk
// — never the model's prose.
package acceptance

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// p12Family is this rung's name in a skip line, a failure message and the
// evidence line. One constant so they cannot disagree.
const p12Family = "permission-hook"

// p12Decision is the behavior the cell's hook answers with; it is also the
// cell's variant, so the two arms are two addressable cells.
type p12Decision string

const (
	p12Allow p12Decision = "allow"
	p12Deny  p12Decision = "deny"
)

// The vendor vocabulary the verdicts compare against: what claude names the
// event, which tool the hook is registered on, and the tool_result text claude
// puts on a call a hook denied.
const (
	p12HookEvent    = "PermissionRequest"
	p12GatedTool    = "Bash"
	p12DeniedByHook = "Permission denied by hook"
)

// p12HookDelay is how long the hook sleeps before writing its marker and
// answering. Long enough that an engine which did NOT await the hook has
// unmistakably emitted its tool_result before the marker exists.
const p12HookDelay = 5 * time.Second

// The fixture's file names inside one cell's directory.
const (
	p12ProofName     = "proof"           // what the gated `touch` creates; outside the repo
	p12MarkerName    = "hook-marker"     // written by the hook AFTER its sleep; its mtime is the answer time
	p12HookInputName = "hook-input.json" // the hook's stdin, captured verbatim
	p12HookName      = "hook.sh"
	p12SettingsName  = "settings.json" // passed with --settings; outside the repo
)

// --- the wire the fixture writes ---------------------------------------------

// p12Settings is the --settings document: one PermissionRequest matcher on the
// gated tool, running the cell's hook script.
type p12Settings struct {
	Hooks map[string][]p12HookMatcher `json:"hooks"`
}

type p12HookMatcher struct {
	Matcher string           `json:"matcher"`
	Hooks   []p12HookCommand `json:"hooks"`
}

type p12HookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// p12HookOutput is the decision JSON the hook prints:
// {"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}.
type p12HookOutput struct {
	HookSpecificOutput p12HookSpecific `json:"hookSpecificOutput"`
}

type p12HookSpecific struct {
	HookEventName string          `json:"hookEventName"`
	Decision      p12HookDecision `json:"decision"`
}

type p12HookDecision struct {
	Behavior p12Decision `json:"behavior"`
}

// p12SettingsJSON renders the --settings document for a hook at hookPath.
func p12SettingsJSON(hookPath string) ([]byte, error) {
	return json.Marshal(p12Settings{Hooks: map[string][]p12HookMatcher{
		p12HookEvent: {{Matcher: p12GatedTool, Hooks: []p12HookCommand{{Type: "command", Command: hookPath}}}},
	}})
}

// p12HookScript renders the hook: capture stdin, sleep, stamp the marker, then
// print the decision. The marker is written AFTER the sleep and immediately
// before the answer, so its mtime is the moment the decision became available
// — the allow verdict requires the gated call's tool_result to come after it.
func p12HookScript(dir string, d p12Decision) (string, error) {
	out, err := json.Marshal(p12HookOutput{HookSpecificOutput: p12HookSpecific{
		HookEventName: p12HookEvent,
		Decision:      p12HookDecision{Behavior: d},
	}})
	if err != nil {
		return "", err
	}
	q := func(name string) string { return p12ShellQuote(dir + "/" + name) }
	return fmt.Sprintf("#!/bin/sh\ncat > %s\nsleep %d\n: > %s\nprintf '%%s\\n' %s\n",
		q(p12HookInputName), int(p12HookDelay/time.Second), q(p12MarkerName), p12ShellQuote(string(out))), nil
}

// p12ShellQuote single-quotes s for /bin/sh.
func p12ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// p12Prompt orders the one gated call. The target is outside the working
// directory, which is what makes claude ask rather than run it.
func p12Prompt(proofPath string) string {
	return "Use the Bash tool to run exactly this command, once, and nothing else: touch " + proofPath
}

// --- the wire the engine writes ----------------------------------------------

// p12Frame is the subset of a stream-json line the verdicts read.
type p12Frame struct {
	Type              string          `json:"type"`
	Timestamp         string          `json:"timestamp"`
	Message           json.RawMessage `json:"message"`
	PermissionDenials []p12Denial     `json:"permission_denials"`
}

type p12Denial struct {
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
}

type p12Message struct {
	Content json.RawMessage `json:"content"`
}

type p12Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// p12HookInput is the subset of the hook's stdin the verdict reads.
type p12HookInput struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
}

// p12ToolResult is one tool_result block with the time of the frame carrying it.
type p12ToolResult struct {
	Text    string
	IsError bool
	At      time.Time
}

// p12Stream is a decoded run: the gated tool's calls in order, every
// tool_result by id, and the result frame's denials.
type p12Stream struct {
	GatedCalls []string
	Results    map[string]p12ToolResult
	SawResult  bool
	Denials    []p12Denial
}

// p12Decode reads stream-json. A line that is not a JSON object is an error:
// with --output-format stream-json every stdout line is a frame.
func p12Decode(stdout string) (p12Stream, error) {
	s := p12Stream{Results: map[string]p12ToolResult{}}
	sc := bufio.NewScanner(strings.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var f p12Frame
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			return s, fmt.Errorf("stdout line %d is not a stream-json frame: %w", n, err)
		}
		switch f.Type {
		case "result":
			s.SawResult = true
			s.Denials = f.PermissionDenials
		case "assistant", "user":
			var m p12Message
			var blocks []p12Block
			if json.Unmarshal(f.Message, &m) != nil || json.Unmarshal(m.Content, &blocks) != nil {
				continue // a string-content message carries no tool blocks
			}
			for _, b := range blocks {
				switch {
				case b.Type == "tool_use" && b.Name == p12GatedTool:
					s.GatedCalls = append(s.GatedCalls, b.ID)
				case b.Type == "tool_result":
					at, _ := time.Parse(time.RFC3339Nano, f.Timestamp)
					s.Results[b.ToolUseID] = p12ToolResult{Text: p12BlockText(b.Content), IsError: b.IsError, At: at}
				}
			}
		}
	}
	return s, sc.Err()
}

// p12BlockText flattens a tool_result's content: a bare string, or text blocks.
func p12BlockText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []p12Block
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// --- one cell's observation ---------------------------------------------------

// p12Outcome is everything a P12 verdict may look at.
type p12Outcome struct {
	Cell     probeCellID
	Decision p12Decision
	Started  bool
	TimedOut bool
	Run      probeRun
	// HookInput is the hook's captured stdin; HookInputErr is why it could not
	// be read (the hook never ran).
	HookInput    []byte
	HookInputErr error
	// MarkerAt is the marker's mtime; MarkerErr is why it is absent (the hook
	// never finished its sleep).
	MarkerAt  time.Time
	MarkerErr error
	// ProofExists is whether the gated call's file is on disk after the run;
	// ProofErr is a stat failure that is NOT plain absence.
	ProofExists bool
	ProofErr    error
}

// The shapes this rung adds. The channel's own shape (APPROVAL-DELIVERY) is
// not used: every failure here names which half of the contract broke.
const (
	// shapeNotAttempted: the model never made the gated call, so the cell
	// measured nothing about the hook.
	shapeNotAttempted probeShape = "NOT-ATTEMPTED failure"
	// shapeHookNotFired: the gated call was made but the PermissionRequest
	// hook did not run (or did not finish).
	shapeHookNotFired probeShape = "HOOK-NOT-FIRED failure"
	// shapeNotAwaited: the hook ran, but the engine settled the call before
	// the hook had answered.
	shapeNotAwaited probeShape = "NOT-AWAITED failure"
	// shapeDecisionIgnored: the hook answered and the engine did something
	// other than what the answer said.
	shapeDecisionIgnored probeShape = "DECISION-IGNORED failure"
)

func (o p12Outcome) verdict() probeVerdict {
	return probeVerdict{Family: p12Family, Cell: o.Cell, Channel: channelGatedAction}
}

func (o p12Outcome) evidence() string {
	return fmt.Sprintf("\nproof exists=%t err=%v\nhook input (err=%v):\n%s\nmarker at=%s err=%v\nexit=%d runErr=%v timedOut=%t\nstdout:\n%s\nstderr:\n%s",
		o.ProofExists, o.ProofErr, o.HookInputErr, o.HookInput, o.MarkerAt.Format(time.RFC3339Nano), o.MarkerErr,
		o.Run.ExitCode, o.Run.Err, o.TimedOut, o.Run.Stdout, o.Run.Stderr)
}

// p12Assert judges one cell. The common half (the run happened, the gated call
// was made, the hook fired on it and answered) runs first; then the arm the
// cell's decision names.
func p12Assert(o p12Outcome) error {
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
		return v.fail(shapeOutputFormat, "the stream carried no result frame, so permission_denials cannot be read", o.evidence())
	}
	if len(s.GatedCalls) == 0 {
		return v.fail(shapeNotAttempted, "the model made no "+p12GatedTool+" tool_use, so nothing asked for permission and the cell measured nothing", o.evidence())
	}
	call := s.GatedCalls[0]
	res, ok := s.Results[call]
	if !ok {
		return v.fail(shapeOutputFormat, fmt.Sprintf("no tool_result for the gated call %s", call), o.evidence())
	}
	if err := o.hookFired(v); err != nil {
		return err
	}

	switch o.Decision {
	case p12Allow:
		return o.assertAllow(v, s, call, res)
	case p12Deny:
		return o.assertDeny(v, s, call, res)
	}
	return fmt.Errorf("%s %s: unknown decision %q (want %q or %q)", p12Family, o.Cell, o.Decision, p12Allow, p12Deny)
}

// hookFired requires the hook's stdin to name the event and the gated tool,
// and the marker to exist — the hook ran on THIS call and reached its answer.
func (o p12Outcome) hookFired(v probeVerdict) error {
	if o.HookInputErr != nil {
		return v.fail(shapeHookNotFired, fmt.Sprintf("the %s hook never captured its input: %v", p12HookEvent, o.HookInputErr), o.evidence())
	}
	var in p12HookInput
	if err := json.Unmarshal(o.HookInput, &in); err != nil {
		return v.fail(shapeHookNotFired, fmt.Sprintf("the hook's captured input is not JSON: %v", err), o.evidence())
	}
	if in.HookEventName != p12HookEvent || in.ToolName != p12GatedTool {
		return v.fail(shapeHookNotFired, fmt.Sprintf("the hook ran for event %q tool %q, want %q on %q", in.HookEventName, in.ToolName, p12HookEvent, p12GatedTool), o.evidence())
	}
	if o.MarkerErr != nil {
		return v.fail(shapeHookNotFired, fmt.Sprintf("the hook started but never wrote its marker after the %s sleep: %v", p12HookDelay, o.MarkerErr), o.evidence())
	}
	return nil
}

// assertAllow: the call ran (no error, no denial, the file exists) and its
// tool_result came after the hook's answer.
func (o p12Outcome) assertAllow(v probeVerdict, s p12Stream, call string, res p12ToolResult) error {
	if res.At.IsZero() {
		return v.fail(shapeOutputFormat, "the gated call's tool_result frame carries no parseable timestamp, so awaiting cannot be judged", o.evidence())
	}
	// The frame timestamp has millisecond precision; the marker's mtime does not.
	if res.At.Before(o.MarkerAt.Truncate(time.Millisecond)) {
		return v.fail(shapeNotAwaited, fmt.Sprintf("the gated call's tool_result (%s) predates the hook's answer (%s): the engine settled the call without waiting for the hook",
			res.At.Format(time.RFC3339Nano), o.MarkerAt.Format(time.RFC3339Nano)), o.evidence())
	}
	if res.IsError || p12Denied(s.Denials, call) {
		return v.fail(shapeDecisionIgnored, fmt.Sprintf("the hook answered %q but the call was refused (is_error=%t, tool_result %q, denials %v)", p12Allow, res.IsError, res.Text, s.Denials), o.evidence())
	}
	if o.ProofErr != nil {
		return v.fail(shapeRunFailed, fmt.Sprintf("the proof file could not be checked: %v", o.ProofErr), o.evidence())
	}
	if !o.ProofExists {
		return v.fail(shapeDecisionIgnored, fmt.Sprintf("the hook answered %q and the call reported success, but the file it creates is absent", p12Allow), o.evidence())
	}
	return nil
}

// assertDeny: the call was blocked with the hook-denial tool_result, reported
// in permission_denials, and the file was never created.
func (o p12Outcome) assertDeny(v probeVerdict, s p12Stream, call string, res p12ToolResult) error {
	if !res.IsError || res.Text != p12DeniedByHook {
		return v.fail(shapeDecisionIgnored, fmt.Sprintf("the hook answered %q but the gated call's tool_result is %q (is_error=%t), want %q (is_error=true)", p12Deny, res.Text, res.IsError, p12DeniedByHook), o.evidence())
	}
	if !p12Denied(s.Denials, call) {
		return v.fail(shapeDecisionIgnored, fmt.Sprintf("the result frame's permission_denials %v has no %s entry for the gated call %s", s.Denials, p12GatedTool, call), o.evidence())
	}
	if o.ProofErr != nil {
		return v.fail(shapeRunFailed, fmt.Sprintf("the proof file could not be checked: %v", o.ProofErr), o.evidence())
	}
	if o.ProofExists {
		return v.fail(shapeDecisionIgnored, fmt.Sprintf("the hook answered %q but the file the gated call creates exists", p12Deny), o.evidence())
	}
	return nil
}

func p12Denied(denials []p12Denial, call string) bool {
	for _, d := range denials {
		if d.ToolName == p12GatedTool && d.ToolUseID == call {
			return true
		}
	}
	return false
}

// errP12NotRun marks an outcome whose fixture never reached the run.
var errP12NotRun = errors.New("permission-hook: the cell's fixture was not prepared")
