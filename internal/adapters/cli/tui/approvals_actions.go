package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// apprAction is one button on the action row. run is only ever called on the
// request the action was latched to, after activate re-checked it.
type apprAction struct {
	label string
	run   func(a approvalsModel, p coord.PendingApproval) (approvalsModel, tea.Cmd)
}

// actions is the selected row's action row; index 0 is the neutral Later.
// A tombstone offers nothing else.
func (a approvalsModel) actions() []apprAction {
	acts := []apprAction{{label: "Later"}}
	row, ok := a.find(a.sel)
	if !ok || !row.live() {
		return acts
	}
	acts = append(acts, kindActions[row.p.Kind]...)
	return append(acts,
		apprAction{label: "Deny all from " + sanitizeForDisplay(row.p.From.Harp) + "…", run: openDenyAll},
		apprAction{label: "Grants…", run: func(a approvalsModel, p coord.PendingApproval) (approvalsModel, tea.Cmd) {
			a.sub = openGrants(a.src, p.From.Harp)
			return a, nil
		}},
	)
}

// kindActions are each kind's own actions, between Later and the per-child
// ones.
var kindActions = map[coord.ApprovalKind][]apprAction{
	coord.ApprovalTool: {
		{label: "Allow once", run: allowOnce},
		{label: "Allow for session…", run: opener(subScope)},
		{label: "Deny…", run: opener(subDeny)},
	},
	coord.ApprovalQuestion: {
		{label: "Answer…", run: opener(subAnswer)},
		{label: "Decline…", run: opener(subDeny)},
	},
	coord.ApprovalPlan: {
		{label: "Approve…", run: opener(subApprove)},
		{label: "Reject…", run: opener(subReject)},
	},
}

func allowOnce(a approvalsModel, p coord.PendingApproval) (approvalsModel, tea.Cmd) {
	a.focus, a.latch = 0, ""
	return a, answerCmd(a.src, "allowed "+sanitizeForDisplay(p.Ask.Tool)+" once for "+sanitizeForDisplay(p.From.Harp), []coord.ApprovalID{p.ID}, coord.ApprovalDecision{Allow: true})
}

func opener(kind subKind) func(approvalsModel, coord.PendingApproval) (approvalsModel, tea.Cmd) {
	return func(a approvalsModel, p coord.PendingApproval) (approvalsModel, tea.Cmd) {
		a.sub = newSub(kind, p)
		return a, nil
	}
}

// openDenyAll latches the child's pending requests as they are NOW: one that
// arrives while the confirmation is up is not in the set it confirms.
func openDenyAll(a approvalsModel, p coord.PendingApproval) (approvalsModel, tea.Cmd) {
	s := newSub(subDenyAll, p)
	for _, r := range a.rows {
		if r.live() && r.p.From.Harp == p.From.Harp {
			s.ids = append(s.ids, r.p.ID)
		}
	}
	a.sub = s
	return a, nil
}

func openGrants(src coord.ApprovalSource, harp string) *subState {
	return &subState{kind: subGrants, harp: harp, grants: src.Grants(harp)}
}

// subKind names a sub-view.
type subKind int

const (
	subScope subKind = iota + 1
	subDeny
	subAnswer
	subApprove
	subReject
	subDenyAll
	subGrants
	subRevoke
)

// aimed reports that the sub-view acts on one request, so it closes when
// that request resolves. Deny-all holds its own latched set; grants belong
// to the child, not to a request.
func (k subKind) aimed() bool { return k <= subReject }

// subState is an open sub-view. focus 0 is Back, 1 the primary button.
type subState struct {
	kind   subKind
	target coord.ApprovalID
	p      coord.PendingApproval
	harp   string
	ids    []coord.ApprovalID // deny-all's latched set
	text   string             // note / feedback
	cursor int
	focus  int

	scopes   []scopeOption
	postures []string
	picks    []map[int]bool // per question: the chosen option indexes
	others   []string       // per question: the Other text
	grants   []coord.Grant
	grant    coord.Grant // revoke's target
}

func newSub(kind subKind, p coord.PendingApproval) *subState {
	s := &subState{kind: kind, target: p.ID, p: p, harp: p.From.Harp}
	switch kind {
	case subScope:
		s.scopes = scopeOptions(p)
	case subApprove:
		s.postures = slices.Clone(p.Transitions)
	case subAnswer:
		s.picks = make([]map[int]bool, len(p.Ask.Questions))
		for i := range s.picks {
			s.picks[i] = map[int]bool{}
		}
		s.others = make([]string, len(p.Ask.Questions))
	}
	return s
}

// scopeOption is one allow-for-session choice: a session rule the engine
// suggested, or the engine's suggested mode change.
type scopeOption struct {
	rule string
	mode engine.Declared[string]
}

// scopeOptions are the engine's own session suggestions, and its suggested
// mode change when the engine offers that transition from the asker's
// posture (the request's Transitions). Every rule is granted for the session
// only — the decision carries rules, never a destination.
func scopeOptions(p coord.PendingApproval) []scopeOption {
	var out []scopeOption
	for _, r := range p.Ask.Suggestions {
		out = append(out, scopeOption{rule: r})
	}
	if m, ok := p.Ask.SuggestsSetMode.Get(); ok && slices.Contains(p.Transitions, m) {
		out = append(out, scopeOption{mode: engine.Provide(m)})
	}
	return out
}

// subSpec is a sub-view's behaviour: its primary button and when it may be
// pressed, whether it takes typed text, how long its list is, and what the
// primary does.
type subSpec struct {
	primary string
	text    bool
	listLen func(s *subState) int
	ready   func(s *subState) (bool, string)
	confirm func(a approvalsModel, s *subState) (approvalsModel, tea.Cmd)
}

func always(*subState) (bool, string) { return true, "" }
func noList(*subState) int            { return 0 }

var subSpecs = map[subKind]subSpec{
	subScope: {primary: "Grant", listLen: func(s *subState) int { return len(s.scopes) },
		ready: func(s *subState) (bool, string) {
			return len(s.scopes) > 0, "the engine offered no session rule for this call"
		},
		confirm: confirmScope},
	subDeny: {primary: "Deny", text: true, listLen: noList, ready: always, confirm: confirmDeny},
	subAnswer: {primary: "Submit answers", text: true, listLen: func(s *subState) int { return len(answerEntries(s.p)) },
		ready: func(s *subState) (bool, string) {
			return answersComplete(s), "answer every question first"
		},
		confirm: confirmAnswers},
	subApprove: {primary: "Approve", text: true, listLen: func(s *subState) int { return len(s.postures) },
		ready: func(s *subState) (bool, string) {
			return len(s.postures) > 0, "this agent's engine offers no posture to continue in"
		},
		confirm: confirmApprove},
	subReject: {primary: "Reject", text: true, listLen: noList,
		ready: func(s *subState) (bool, string) {
			return strings.TrimSpace(s.text) != "", "feedback is required: the model revises the plan from it"
		},
		confirm: confirmReject},
	subDenyAll: {primary: "Deny all", listLen: noList, ready: always, confirm: confirmDenyAll},
	subGrants: {primary: "Revoke…", listLen: func(s *subState) int { return len(s.grants) },
		ready: func(s *subState) (bool, string) { return len(s.grants) > 0, "no session grants to revoke" },
		confirm: func(a approvalsModel, s *subState) (approvalsModel, tea.Cmd) {
			a.sub = &subState{kind: subRevoke, harp: s.harp, grant: s.grants[s.cursor]}
			return a, nil
		}},
	subRevoke: {primary: "Revoke", listLen: noList, ready: always, confirm: confirmRevoke},
}

// decided closes the sub-view after a decision; the resync that follows the
// answer moves the selection on.
func (a approvalsModel) decided() approvalsModel {
	a.sub, a.focus, a.latch = nil, 0, ""
	return a
}

func confirmScope(a approvalsModel, s *subState) (approvalsModel, tea.Cmd) {
	o := s.scopes[s.cursor]
	d := coord.ApprovalDecision{Allow: true}
	what := "allowed for the session"
	if o.rule != "" {
		d.SessionRules = []string{o.rule}
		what = "granted " + sanitizeForDisplay(o.rule) + " to " + sanitizeForDisplay(s.harp) + " for this run"
	} else {
		d.SetMode = o.mode
	}
	return a.decided(), answerCmd(a.src, what, []coord.ApprovalID{s.target}, d)
}

func confirmDeny(a approvalsModel, s *subState) (approvalsModel, tea.Cmd) {
	d := coord.ApprovalDecision{Message: strings.TrimSpace(s.text)}
	return a.decided(), answerCmd(a.src, "denied "+sanitizeForDisplay(s.harp)+"'s request", []coord.ApprovalID{s.target}, d)
}

func confirmAnswers(a approvalsModel, s *subState) (approvalsModel, tea.Cmd) {
	d := coord.ApprovalDecision{Allow: true, Answers: questionAnswers(s)}
	return a.decided(), answerCmd(a.src, "answered "+sanitizeForDisplay(s.harp), []coord.ApprovalID{s.target}, d)
}

func confirmApprove(a approvalsModel, s *subState) (approvalsModel, tea.Cmd) {
	m := s.postures[s.cursor]
	d := coord.ApprovalDecision{Allow: true, SetMode: engine.Provide(m), Message: strings.TrimSpace(s.text)}
	return a.decided(), answerCmd(a.src, "approved "+sanitizeForDisplay(s.harp)+"'s plan ("+sanitizeForDisplay(m)+")", []coord.ApprovalID{s.target}, d)
}

func confirmReject(a approvalsModel, s *subState) (approvalsModel, tea.Cmd) {
	d := coord.ApprovalDecision{Message: strings.TrimSpace(s.text)}
	return a.decided(), answerCmd(a.src, "rejected "+sanitizeForDisplay(s.harp)+"'s plan", []coord.ApprovalID{s.target}, d)
}

// denyAllMessage is what each denied request tells its model.
const denyAllMessage = "denied: the human denied every pending request from this agent"

func confirmDenyAll(a approvalsModel, s *subState) (approvalsModel, tea.Cmd) {
	d := coord.ApprovalDecision{Message: denyAllMessage}
	return a.decided(), answerCmd(a.src, "denied every pending request from "+sanitizeForDisplay(s.harp), s.ids, d)
}

func confirmRevoke(a approvalsModel, s *subState) (approvalsModel, tea.Cmd) {
	src, harp, g := a.src, s.harp, s.grant
	return a, func() tea.Msg {
		return revokeResultMsg{harp: harp, rule: g.Rule, err: src.Revoke(harp, g.ID)}
	}
}

// back leaves a sub-view without deciding: revoke's confirmation returns to
// the grants list, anything else to the main view on the neutral action.
func (a approvalsModel) back() approvalsModel {
	if a.sub.kind == subRevoke {
		a.sub = openGrants(a.src, a.sub.harp)
		return a
	}
	a.sub, a.focus, a.latch = nil, 0, ""
	return a
}

// subNav are a sub-view's keys that never decide. Printable keys are not
// here: in a sub-view they type into its text field.
var subNav = map[string]func(s *subState){
	"left":      func(s *subState) { s.focus = 1 - s.focus },
	"right":     func(s *subState) { s.focus = 1 - s.focus },
	"tab":       func(s *subState) { s.focus = 1 - s.focus },
	"shift+tab": func(s *subState) { s.focus = 1 - s.focus },
	"up":        func(s *subState) { s.moveCursor(-1) },
	"down":      func(s *subState) { s.moveCursor(1) },
	"backspace": func(s *subState) { s.backspace() },
}

// subKey handles a key in a sub-view. The sub-view is copied before it is
// changed: models are values, and an earlier one must not see this key.
func (a approvalsModel) subKey(msg tea.KeyPressMsg) (approvalsModel, tea.Cmd) {
	s := *a.sub
	a.sub = &s
	k := msg.String()
	if f, ok := subNav[k]; ok {
		f(&s)
		return a, nil
	}
	switch {
	case k == "esc" || (k == "enter" && s.focus == 0):
		return a.back(), nil
	case k == "enter":
		return a.confirmSub(&s)
	case k == "space":
		s.typeText(" ")
	case msg.Text != "":
		s.typeText(msg.Text)
	}
	return a, nil
}

// confirmSub presses the primary button: refused, and said why, until the
// sub-view is complete.
func (a approvalsModel) confirmSub(s *subState) (approvalsModel, tea.Cmd) {
	spec := subSpecs[s.kind]
	if ok, why := spec.ready(s); !ok {
		a.note, a.errMsg = "", why
		return a, nil
	}
	return spec.confirm(a, s)
}

// moveCursor moves within the sub-view's list. Like a selection change in
// the main view, it puts focus back on Back: the primary acts only on what
// was under the cursor when focus reached it.
func (s *subState) moveCursor(d int) {
	n := subSpecs[s.kind].listLen(s)
	if n == 0 {
		return
	}
	s.cursor = min(max(s.cursor+d, 0), n-1)
	s.focus = 0
}

// typeText is typed or pasted text. In the answer view it goes to the Other
// field under the cursor, and a space on an option toggles it instead.
func (s *subState) typeText(t string) {
	if s.kind == subAnswer {
		s.answerText(t)
		return
	}
	if subSpecs[s.kind].text {
		s.text += t
	}
}

func (s *subState) backspace() {
	if s.kind == subAnswer {
		if e, ok := s.entry(); ok && e.other {
			s.others[e.q] = dropLastRune(s.others[e.q])
		}
		return
	}
	s.text = dropLastRune(s.text)
}

func dropLastRune(t string) string {
	if r := []rune(t); len(r) > 0 {
		return string(r[:len(r)-1])
	}
	return t
}

// answerEntry is one line of the answer view: a question's option, or its
// Other field.
type answerEntry struct {
	q, opt int
	other  bool
}

func answerEntries(p coord.PendingApproval) []answerEntry {
	var out []answerEntry
	for qi, q := range p.Ask.Questions {
		for oi := range q.Options {
			out = append(out, answerEntry{q: qi, opt: oi})
		}
		out = append(out, answerEntry{q: qi, other: true})
	}
	return out
}

func (s *subState) entry() (answerEntry, bool) {
	es := answerEntries(s.p)
	if s.cursor < 0 || s.cursor >= len(es) {
		return answerEntry{}, false
	}
	return es[s.cursor], true
}

// answerText types into the Other field under the cursor (which, for a
// single-choice question, replaces a chosen option), or toggles the option
// under it on a space.
func (s *subState) answerText(t string) {
	e, ok := s.entry()
	if !ok {
		return
	}
	if e.other {
		s.others[e.q] += t
		if !s.p.Ask.Questions[e.q].MultiSelect {
			s.picks[e.q] = map[int]bool{}
		}
		return
	}
	if t != " " {
		return
	}
	if s.p.Ask.Questions[e.q].MultiSelect {
		s.picks[e.q][e.opt] = !s.picks[e.q][e.opt]
		return
	}
	s.picks[e.q] = map[int]bool{e.opt: true}
	s.others[e.q] = ""
}

// answersComplete: every question has a chosen option or Other text.
func answersComplete(s *subState) bool {
	for qi := range s.p.Ask.Questions {
		if !hasPick(s.picks[qi]) && strings.TrimSpace(s.others[qi]) == "" {
			return false
		}
	}
	return true
}

func hasPick(m map[int]bool) bool {
	for _, on := range m {
		if on {
			return true
		}
	}
	return false
}

// questionAnswers are the answers as the engine asked: the question text and
// option labels verbatim (what is shown is sanitized; what is answered is
// what the engine sent).
func questionAnswers(s *subState) []engine.QuestionAnswer {
	out := make([]engine.QuestionAnswer, len(s.p.Ask.Questions))
	for qi, q := range s.p.Ask.Questions {
		a := engine.QuestionAnswer{Question: q.Text, Other: strings.TrimSpace(s.others[qi])}
		for oi, o := range q.Options {
			if s.picks[qi][oi] {
				a.Labels = append(a.Labels, o.Label)
			}
		}
		out[qi] = a
	}
	return out
}
