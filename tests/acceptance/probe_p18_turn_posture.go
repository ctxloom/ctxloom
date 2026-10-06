// Package acceptance: P18, TURN POSTURE — the capability ladder's rung for
// the vendor half of a headless child's posture moving BETWEEN turns
// (conformance cells P1, D2 and P2).
//
// UNTAGGED, like probe_p12_permission_hook.go: every cell is @live and paid,
// so the verdicts and the fixture's wire below are the only part of this rung
// a hermetic test can execute (probe_p18_turn_posture_test.go). The godog
// plumbing lives in steps_p18_turn_posture.go behind the acceptance tag.
//
// WHAT THIS RUNG PINS. A child's posture is carried per turn, never by the
// session: every turn's inline --settings names its mode as defaultMode
// (turnSettings), because claude does not carry a mode across --resume. Two
// things move that mode between turns — a parent approving the plan a
// plan-first turn ended holding (runner.planApprovalIn), and an allowed ask
// carrying a mode change, which the run holds (approvals.settle) — and each
// rests on claude starting a RESUMED turn in the mode that turn's settings
// name. Each variant is two turns of one session:
//
//	plan-resumed  (P1) acceptEdits, then plan under --resume: the plan turn
//	              starts in plan and its ordered write does not land; the
//	              first turn's landed write is the control.
//	plan-approved (D2) plan, then acceptEdits under --resume with an approval
//	              prompt: the plan turn writes claude's native plan and not
//	              the file, the approved turn carries the plan out.
//	setmode-held  (P2) default, where the PermissionRequest hook answers the
//	              write's ask with production's allow + session setMode
//	              acceptEdits; then acceptEdits under --resume, where the next
//	              write lands with no ask.
//
// The mode a turn started in is claude's own report, the init frame's
// permissionMode; the files and the hook's captured asks are the behaviour
// that report must agree with. Nothing reads the model's prose.
package acceptance

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// p18Family is this rung's name in a skip line, a failure message and the
// evidence line.
const p18Family = "turn-posture"

// p18Variant is the cell's discriminator: which posture move it measures.
type p18Variant string

const (
	p18PlanResumed  p18Variant = "plan-resumed"
	p18PlanApproved p18Variant = "plan-approved"
	p18SetModeHeld  p18Variant = "setmode-held"
)

// p18Variants are the variants the rung knows, in the feature's order.
var p18Variants = []p18Variant{p18PlanResumed, p18PlanApproved, p18SetModeHeld}

// claude's spelling of the modes the cells move between.
const (
	p18Default     = "default"
	p18AcceptEdits = "acceptEdits"
	p18Plan        = "plan"
)

// p18Target is a file a turn is ordered to write into the repo, and the
// content that proves the write was the ordered one.
type p18Target struct{ Name, Content string }

var (
	p18First  = p18Target{Name: "first.txt", Content: "turn-posture-first"}
	p18Second = p18Target{Name: "second.txt", Content: "turn-posture-second"}
)

// The fixture's names: the hook's ask captures and the hook itself live in
// the cell's directory, outside the repo; claude writes plan mode's plans
// under its config home.
const (
	p18AsksDir  = "asks"
	p18HookName = "hook.sh"
	p18PlansDir = "plans"
)

// p18ApprovedPrompt is the approved turn's prompt. The plan, and so the file
// it names, is known only from the resumed session.
const p18ApprovedPrompt = "Your plan was approved. Carry it out now."

// p18TurnSpec is one turn: the mode its --settings names and its prompt.
type p18TurnSpec struct{ Mode, Prompt string }

func p18WritePrompt(repo string, t p18Target) string {
	return fmt.Sprintf("Use the Write tool to create the file %s/%s with exactly this content: %s — make that one call and nothing else.", repo, t.Name, t.Content)
}

// turns is the variant's two turns for a repo at repo.
func (v p18Variant) turns(repo string) [2]p18TurnSpec {
	switch v {
	case p18PlanResumed:
		return [2]p18TurnSpec{{p18AcceptEdits, p18WritePrompt(repo, p18First)}, {p18Plan, p18WritePrompt(repo, p18Second)}}
	case p18PlanApproved:
		plan := fmt.Sprintf("Plan how to create the file %s/%s with exactly this content: %s. Write the plan; do not create the file yet.", repo, p18First.Name, p18First.Content)
		return [2]p18TurnSpec{{p18Plan, plan}, {p18AcceptEdits, p18ApprovedPrompt}}
	default:
		return [2]p18TurnSpec{{p18Default, p18WritePrompt(repo, p18First)}, {p18AcceptEdits, p18WritePrompt(repo, p18Second)}}
	}
}

// p18Codec is production's claude approval codec: the setmode-held hook
// prints its answer and the verdict reads the captured ask through it.
func p18Codec() (engine.ApprovalCodec, error) {
	c, ok := claude.Claude{}.Approvals().Get()
	if !ok {
		return nil, errors.New(p18Family + ": claude declares no approval codec")
	}
	return c, nil
}

// hookAnswer is what the cell's PermissionRequest hook prints: production's
// allow carrying a session accept-edits change for setmode-held; nothing for
// a plan variant, so any ask there is refused (P12's silent cell).
func (v p18Variant) hookAnswer() ([]byte, error) {
	if v != p18SetModeHeld {
		return nil, nil
	}
	c, err := p18Codec()
	if err != nil {
		return nil, err
	}
	return c.EncodeAnswer(p12HookEvent, engine.PermissionAsk{}, engine.PermissionAnswer{Allow: true, SetMode: engine.Provide(p18AcceptEdits)})
}

// p18PostureJSON is a turn's inline --settings: only its mode, as
// turnSettings names it.
func p18PostureJSON(mode string) string {
	return `{"permissions":{"defaultMode":"` + mode + `"}}`
}

// p18Args is one turn's argv after the binary; resume is the session the
// turn continues, "" for the first.
func p18Args(t p18TurnSpec, resume string) []string {
	args := []string{"-p", t.Prompt, "--setting-sources", "user", "--settings", p18PostureJSON(t.Mode),
		"--output-format", "stream-json", "--verbose", "--model", liveClaudeModel}
	if resume != "" {
		args = append(args, "--resume", resume)
	}
	return args
}

// p18HomeSettings is the config home's settings.json: one PermissionRequest
// hook with no matcher, registered as production's approval hook is.
type p18HomeSettings struct {
	Hooks map[string][]p12HookMatcher `json:"hooks"`
}

func p18HomeSettingsJSON(hookPath string) ([]byte, error) {
	return json.Marshal(p18HomeSettings{Hooks: map[string][]p12HookMatcher{
		p12HookEvent: {{Hooks: []p12HookCommand{{Type: "command", Command: hookPath}}}},
	}})
}

// p18HookScript renders the hook: capture each ask's stdin to its own file
// under dir/asks, then print answer (nothing when answer is empty).
func p18HookScript(dir string, answer []byte) string {
	script := fmt.Sprintf("#!/bin/sh\ncat > \"$(mktemp %s/ask.XXXXXX)\"\n", p12ShellQuote(dir+"/"+p18AsksDir))
	if len(answer) == 0 {
		return script
	}
	return script + fmt.Sprintf("printf '%%s\\n' %s\n", p12ShellQuote(string(answer)))
}

// p18TurnRun is one turn's observation.
type p18TurnRun struct {
	Started, TimedOut bool
	Run               probeRun
	// Asks is the hook's stdin for each ask this turn raised.
	Asks [][]byte
	// Landed maps a target's name to whether, after this turn, it is on disk
	// carrying its content.
	Landed map[string]bool
	// Plans counts the native plan files in the config home after this turn.
	Plans int
}

// p18Outcome is everything a P18 verdict may look at.
type p18Outcome struct {
	Cell    probeCellID
	Variant p18Variant
	Turns   [2]p18TurnRun
}

// The shapes this rung adds; shapeNotAttempted, shapeHookNotFired and
// shapeDecisionIgnored are P12's.
const (
	// shapePostureNotApplied: a turn started in another mode than the one its
	// --settings named.
	shapePostureNotApplied probeShape = "POSTURE-NOT-APPLIED failure"
	// shapeNotResumed: the second turn ran on another session than the first.
	shapeNotResumed probeShape = "NOT-RESUMED failure"
	// shapePostureNotHeld: a turn reported the mode and behaved otherwise.
	shapePostureNotHeld probeShape = "POSTURE-NOT-HELD failure"
	// shapeNoNativePlan: a plan turn left no plan in the config home, so a
	// plan-first run's report has no plan to name.
	shapeNoNativePlan probeShape = "NO-NATIVE-PLAN failure"
	// shapeApprovedWorkMissing: the approved turn did not carry the plan out.
	shapeApprovedWorkMissing probeShape = "APPROVED-WORK-MISSING failure"
	// shapeNoSetModeOffer: an edit's ask offered no accept-edits mode change,
	// so the approvals surface has none to offer the human.
	shapeNoSetModeOffer probeShape = "NO-SETMODE-OFFER failure"
)

func (o p18Outcome) verdict() probeVerdict {
	return probeVerdict{Family: p18Family, Cell: o.Cell, Channel: channelTurnPosture}
}

// summary is the cell's one-line evidence.
func (o p18Outcome) summary() string {
	var parts []string
	for i, t := range o.Turns {
		s, _ := p12Decode(t.Run.Stdout)
		parts = append(parts, fmt.Sprintf("turn%d{mode=%q session=%q exit=%d timedOut=%t landed=%v asks=%d plans=%d}",
			i+1, s.PermissionMode, s.SessionID, t.Run.ExitCode, t.TimedOut, t.Landed, len(t.Asks), t.Plans))
	}
	return strings.Join(parts, " ")
}

func (o p18Outcome) evidence() string {
	var b strings.Builder
	b.WriteString("\n" + o.summary())
	for i, t := range o.Turns {
		fmt.Fprintf(&b, "\n--- turn %d runErr=%v\nasks:\n%s\nstdout:\n%s\nstderr:\n%s", i+1, t.Run.Err, t.Asks, t.Run.Stdout, t.Run.Stderr)
	}
	return b.String()
}

// p18Assert judges the cell: both turns ran, on one session, each in the mode
// its settings named; then the variant's own arm.
func p18Assert(o p18Outcome) error {
	v, ev := o.verdict(), o.evidence()
	specs := o.Variant.turns("")
	var session string
	for i, t := range o.Turns {
		s, err := p18Turn(v, i+1, t, specs[i].Mode, ev)
		if err != nil {
			return err
		}
		if i == 0 {
			session = s.SessionID
		} else if s.SessionID != session {
			return v.fail(shapeNotResumed, fmt.Sprintf("turn 2 ran on session %q, not turn 1's %q", s.SessionID, session), ev)
		}
	}
	switch o.Variant {
	case p18PlanResumed:
		return p18AssertPlanResumed(v, o, ev)
	case p18PlanApproved:
		return p18AssertPlanApproved(v, o, ev)
	case p18SetModeHeld:
		return p18AssertSetModeHeld(v, o, ev)
	}
	return fmt.Errorf("%s: unknown variant %q", p18Family, o.Variant)
}

// p18Turn is the half every turn shares: it completed, its stdout decodes,
// it reached a result frame, and its init frame names a session and the
// mode its settings named.
func p18Turn(v probeVerdict, n int, t p18TurnRun, mode, ev string) (p12Stream, error) {
	if !t.Started || t.TimedOut {
		return p12Stream{}, v.fail(shapeRunFailed, fmt.Sprintf("turn %d did not complete (started=%t timedOut=%t)", n, t.Started, t.TimedOut), ev)
	}
	trimmed, err := v.ran(t.Run)
	if err != nil {
		return p12Stream{}, err
	}
	s, err := p12Decode(trimmed)
	if err != nil {
		return s, v.fail(shapeOutputFormat, fmt.Sprintf("turn %d: %v", n, err), ev)
	}
	if !s.SawResult || s.SessionID == "" {
		return s, v.fail(shapeOutputFormat, fmt.Sprintf("turn %d's stream lacks a result frame or an init frame naming its session", n), ev)
	}
	if s.PermissionMode != mode {
		return s, v.fail(shapePostureNotApplied, fmt.Sprintf("turn %d's settings named defaultMode %q and its init frame reports %q", n, mode, s.PermissionMode), ev)
	}
	return s, nil
}

func p18AssertPlanResumed(v probeVerdict, o p18Outcome, ev string) error {
	if !o.Turns[0].Landed[p18First.Name] {
		return v.fail(shapeNotAttempted, "the acceptEdits control turn did not write "+p18First.Name+", so the plan turn's unwritten file would mean nothing", ev)
	}
	if o.Turns[1].Landed[p18Second.Name] {
		return v.fail(shapePostureNotHeld, "the resumed plan turn wrote "+p18Second.Name+" although it reported plan mode", ev)
	}
	return nil
}

func p18AssertPlanApproved(v probeVerdict, o p18Outcome, ev string) error {
	if o.Turns[0].Landed[p18First.Name] {
		return v.fail(shapePostureNotHeld, "the plan turn created "+p18First.Name+" instead of planning it", ev)
	}
	if o.Turns[0].Plans == 0 {
		return v.fail(shapeNoNativePlan, "the plan turn left no plan under the config home's "+p18PlansDir+"/", ev)
	}
	if !o.Turns[1].Landed[p18First.Name] {
		return v.fail(shapeApprovedWorkMissing, "the approved acceptEdits turn did not create "+p18First.Name, ev)
	}
	return nil
}

func p18AssertSetModeHeld(v probeVerdict, o p18Outcome, ev string) error {
	first, held := o.Turns[0], o.Turns[1]
	if len(first.Asks) == 0 {
		return v.fail(shapeHookNotFired, "the default turn's write raised no PermissionRequest, so no mode change was ever answered", ev)
	}
	c, err := p18Codec()
	if err != nil {
		return err
	}
	ask, err := c.DecodeAsk(p12HookEvent, first.Asks[0])
	if err != nil {
		return v.fail(shapeOutputFormat, fmt.Sprintf("production's codec refuses the captured ask: %v", err), ev)
	}
	if m, ok := ask.SuggestsSetMode.Get(); !ok || m != p18AcceptEdits {
		return v.fail(shapeNoSetModeOffer, fmt.Sprintf("the %s ask offered no %s mode change (production decoded %q, %t)", ask.Tool, p18AcceptEdits, m, ok), ev)
	}
	if !first.Landed[p18First.Name] {
		return v.fail(shapeDecisionIgnored, "the hook allowed the write and "+p18First.Name+" is not on disk", ev)
	}
	if len(held.Asks) > 0 {
		return v.fail(shapePostureNotHeld, fmt.Sprintf("the acceptEdits turn after the allow raised %d ask(s)", len(held.Asks)), ev)
	}
	if !held.Landed[p18Second.Name] {
		return v.fail(shapePostureNotHeld, "the acceptEdits turn after the allow did not write "+p18Second.Name, ev)
	}
	return nil
}
