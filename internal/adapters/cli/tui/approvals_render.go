package tui

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/pmezard/go-difflib/difflib"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// The modal is framed and coloured unlike the engine's own prompt, and it has
// no numbered options, so it cannot be mistaken for one — nor one for it.
var (
	styleFrame = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleFocus = lipgloss.NewStyle().Reverse(true).Bold(true)
	styleWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleAdd   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleDel   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

// modalTitle states the modal's one trust property on every frame.
const modalTitle = " ctxloom approval ─ keys here never reach the engine "

// apprChrome is what the overlay knows about the modal's own arming.
type apprChrome struct {
	arming       bool // summoned, and termui has not armed it yet
	armDiscarded int  // keys termui discarded while arming
}

// render draws the approvals view as exactly h lines of w columns: a framed
// header, the list, the detail (or the open sub-view), the action row and
// the key hints. The bottom — actions and hints — is what a short screen
// keeps longest: it is how the human leaves.
func (a approvalsModel) render(w, h int, c apprChrome) []string {
	if h < 1 || w < 1 {
		return nil
	}
	inner := max(w-2, 1)
	var bottom []string
	bottom = append(bottom, a.rule(w, "├", "┤"))
	for _, l := range flowButtons(a.buttons(), inner) {
		bottom = append(bottom, frameLine(l, inner))
	}
	bottom = append(bottom, frameLine(a.hintLine(c), inner), a.rule(w, "└", "┘"))
	room := h - 1 - len(bottom)
	out := []string{a.topRule(w)}
	out = append(out, a.middle(inner, room)...)
	out = append(out, bottom...)
	if len(out) > h {
		out = out[len(out)-h:]
	}
	return out
}

// middle is the list, a rule, and the detail, in room lines.
func (a approvalsModel) middle(inner, room int) []string {
	if room <= 0 {
		return nil
	}
	listH := min(max(len(a.rows), 1), max(room/3, 1))
	out := make([]string, 0, room)
	for _, l := range a.listLines(listH, inner) {
		out = append(out, frameLine(l, inner))
	}
	detailH := room - listH - 1
	if detailH < 1 {
		return out[:min(len(out), room)]
	}
	out = append(out, a.rule(inner+2, "├", "┤"))
	detail := a.detailLines(inner - 2)
	start := min(a.scroll, max(len(detail)-detailH, 0))
	for i := 0; i < detailH; i++ {
		l := ""
		if start+i < len(detail) {
			l = " " + detail[start+i]
		}
		out = append(out, frameLine(l, inner))
	}
	return out
}

func frameLine(content string, inner int) string {
	return styleFrame.Render("│") + padCell(content, inner) + styleFrame.Render("│")
}

func (a approvalsModel) rule(w int, left, right string) string {
	return styleFrame.Render(left + strings.Repeat("─", max(w-2, 0)) + right)
}

// topRule carries the title and the pending count.
func (a approvalsModel) topRule(w int) string {
	count := " ⚑ " + strconv.Itoa(a.liveCount()) + " pending "
	fill := w - 2 - lipgloss.Width(modalTitle) - lipgloss.Width(count)
	if fill < 1 {
		return styleFrame.Render(padCell("┌"+modalTitle, w))
	}
	return styleFrame.Render("┌" + modalTitle + strings.Repeat("─", fill) + count + "┐")
}

// listLines renders the list windowed around the selection.
func (a approvalsModel) listLines(h, w int) []string {
	lines := make([]string, h)
	if len(a.rows) == 0 {
		lines[0] = "  nothing is waiting for you"
		return lines
	}
	offset := 0
	if sel := a.selectedIndex(); len(a.rows) > h && sel >= 0 {
		offset = min(max(sel-h/2, 0), len(a.rows)-h)
	}
	for i := 0; i < h && offset+i < len(a.rows); i++ {
		lines[i] = " " + a.rowLine(a.rows[offset+i], w-1)
	}
	return lines
}

func (a approvalsModel) rowLine(r apprRow, w int) string {
	mark := "  "
	if r.p.ID == a.sel {
		mark = "▸ "
	}
	who := padCell(sanitizeForDisplay(r.p.From.Harp), 16) + " " + padCell(kindLabel(r.p), 12)
	if !r.live() {
		return padCell(mark+"  ✗   "+who+" "+r.tomb, w)
	}
	line := mark + a.countdown(r.p.Deadline) + "  " + who
	if r.p.Agent != "" {
		line += " (" + sanitizeForDisplay(r.p.Agent) + ")"
	}
	if lin := lineage(r.p); lin != "" {
		line += "  " + lin
	}
	return padCell(line, w)
}

// kindLabel names what is asked: the tool, or the kind.
func kindLabel(p coord.PendingApproval) string {
	switch p.Kind {
	case coord.ApprovalQuestion:
		return "Question"
	case coord.ApprovalPlan:
		return "Plan"
	}
	return sanitizeForDisplay(p.Ask.Tool)
}

func lineage(p coord.PendingApproval) string {
	parts := make([]string, len(p.Lineage))
	for i, h := range p.Lineage {
		parts[i] = sanitizeForDisplay(h)
	}
	return strings.Join(parts, "→")
}

// countdown is the time left, in the warning colour from warnLeft down.
func (a approvalsModel) countdown(deadline time.Time) string {
	left := max(deadline.Sub(a.now()), 0)
	s := mmss(left)
	if left <= warnLeft {
		return styleWarn.Render(s)
	}
	return s
}

func mmss(d time.Duration) string {
	secs := int(d / time.Second)
	return pad2(secs/60) + ":" + pad2(secs%60)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// detailLines is the open sub-view, or the selected request.
func (a approvalsModel) detailLines(w int) []string {
	if a.sub != nil {
		return wrapAll(subRenderers[a.sub.kind](a.sub, w), w)
	}
	row, ok := a.find(a.sel)
	if !ok {
		return nil
	}
	if !row.live() {
		return []string{sanitizeForDisplay(row.p.From.Harp) + " — " + row.tomb}
	}
	return wrapAll(requestLines(row.p, a.countdown(row.p.Deadline), w), w)
}

// wrapAll hard-wraps every line to w columns: nothing a child sent is cut.
func wrapAll(lines []string, w int) []string {
	var out []string
	for _, l := range lines {
		out = append(out, wrapLine(l, max(w, 1))...)
	}
	return out
}

// requestLines is who asks, then what.
func requestLines(p coord.PendingApproval, left string, w int) []string {
	who := sanitizeForDisplay(p.From.Harp)
	if p.Agent != "" {
		who += " · agent " + sanitizeForDisplay(p.Agent)
	}
	if p.WorkDir != "" {
		who += " · " + sanitizeForDisplay(p.WorkDir)
	}
	out := []string{who}
	if lin := lineage(p); lin != "" {
		out = append(out, "lineage "+lin)
	}
	out = append(out, left+" left", "")
	switch p.Kind {
	case coord.ApprovalQuestion:
		return append(out, questionPreview(p.Ask)...)
	case coord.ApprovalPlan:
		return append(out, planDetail(p.Ask, w)...)
	}
	return append(out, toolDetail(p.Ask)...)
}

// toolDetail renders a tool call by what the tool is: the full command, a
// diff of an edit, the content of a write, an MCP call's server/tool and
// input, or the input as JSON.
func toolDetail(ask engine.PermissionAsk) []string {
	var in map[string]json.RawMessage
	_ = json.Unmarshal(ask.Input, &in)
	if f, ok := toolRenderers[ask.Tool]; ok && in != nil {
		return f(in)
	}
	head := "wants to use " + sanitizeForDisplay(ask.Tool) + ":"
	if rest, ok := strings.CutPrefix(ask.Tool, "mcp__"); ok {
		server, tool, _ := strings.Cut(rest, "__")
		head = "wants to call MCP " + sanitizeForDisplay(server) + "/" + sanitizeForDisplay(tool) + ":"
	}
	return append([]string{head}, jsonLines(ask.Input)...)
}

var toolRenderers = map[string]func(map[string]json.RawMessage) []string{
	"Bash": func(in map[string]json.RawMessage) []string {
		out := []string{"wants to run Bash:"}
		out = append(out, prefixLines("  $ ", "    ", jsonString(in, "command"))...)
		if d := jsonString(in, "description"); d != "" {
			out = append(out, prefixLines("  # ", "  # ", d)...)
		}
		return out
	},
	"Edit": func(in map[string]json.RawMessage) []string {
		path := jsonString(in, "file_path")
		return append([]string{"wants to edit " + sanitizeForDisplay(path) + ":"},
			diffLines(path, jsonString(in, "old_string"), jsonString(in, "new_string"))...)
	},
	"MultiEdit": func(in map[string]json.RawMessage) []string {
		path := jsonString(in, "file_path")
		var edits []struct {
			Old string `json:"old_string"`
			New string `json:"new_string"`
		}
		_ = json.Unmarshal(in["edits"], &edits)
		out := []string{"wants to make " + strconv.Itoa(len(edits)) + " edit(s) to " + sanitizeForDisplay(path) + ":"}
		for _, e := range edits {
			out = append(out, diffLines(path, e.Old, e.New)...)
		}
		return out
	},
	"Write": func(in map[string]json.RawMessage) []string {
		content := jsonString(in, "content")
		out := []string{"wants to write " + sanitizeForDisplay(jsonString(in, "file_path")) +
			" (" + strconv.Itoa(len(splitLines(content))) + " lines):"}
		return append(out, prefixLines("  ", "  ", content)...)
	},
}

func jsonString(in map[string]json.RawMessage, key string) string {
	var s string
	_ = json.Unmarshal(in[key], &s)
	return s
}

// prefixLines sanitizes s and puts first before its first line and rest
// before the others.
func prefixLines(first, rest, s string) []string {
	lines := strings.Split(sanitizeForDisplay(s), "\n")
	for i := range lines {
		if i == 0 {
			lines[i] = first + lines[i]
		} else {
			lines[i] = rest + lines[i]
		}
	}
	return lines
}

// jsonLines is raw JSON, indented when it parses; sanitized either way (a
// JSON string may carry a bidi or zero-width character unescaped).
func jsonLines(raw json.RawMessage) []string {
	var b bytes.Buffer
	text := string(raw)
	if json.Indent(&b, raw, "  ", "  ") == nil {
		text = "  " + b.String()
	}
	return strings.Split(sanitizeForDisplay(text), "\n")
}

// diffLines is a unified diff of an edit, sanitized line by line and then
// coloured.
func diffLines(path, before, after string) []string {
	d, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(before), B: difflib.SplitLines(after),
		FromFile: path, ToFile: path, Context: 3,
	})
	if err != nil {
		return []string{"(no diff: " + err.Error() + ")"}
	}
	var out []string
	for _, l := range strings.Split(strings.TrimRight(d, "\n"), "\n") {
		out = append(out, styleDiffLine(sanitizeForDisplay(l)))
	}
	return out
}

func styleDiffLine(l string) string {
	switch {
	case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
		return l
	case strings.HasPrefix(l, "+"):
		return styleAdd.Render(l)
	case strings.HasPrefix(l, "-"):
		return styleDel.Render(l)
	}
	return l
}

// questionPreview shows the questions read-only; Answer… opens them.
func questionPreview(ask engine.PermissionAsk) []string {
	out := []string{"asks " + strconv.Itoa(len(ask.Questions)) + " question(s):"}
	for _, q := range ask.Questions {
		out = append(out, questionHead(q))
		for _, o := range q.Options {
			out = append(out, "    "+optionText(o))
		}
	}
	return out
}

func questionHead(q engine.Question) string {
	head := "  " + sanitizeForDisplay(q.Text)
	if q.Header != "" {
		head = "  [" + sanitizeForDisplay(q.Header) + "] " + sanitizeForDisplay(q.Text)
	}
	if q.MultiSelect {
		head += " (choose any)"
	}
	return head
}

func optionText(o engine.QuestionOption) string {
	t := sanitizeForDisplay(o.Label)
	if o.Description != "" {
		t += " — " + sanitizeForDisplay(o.Description)
	}
	return t
}

func planDetail(ask engine.PermissionAsk, w int) []string {
	if ask.Plan == nil {
		return []string{"presents a plan (empty)"}
	}
	out := []string{"presents a plan · " + sanitizeForDisplay(ask.Plan.Path), ""}
	return append(out, styleMarkdownLines(ask.Plan.Markdown, w)...)
}

// subRenderers draw each sub-view's body.
var subRenderers = map[subKind]func(s *subState, w int) []string{
	subScope: scopeLines,
	subDeny: func(s *subState, _ int) []string {
		return textLines("Deny "+sanitizeForDisplay(s.harp)+"'s request", "note (optional, sent to the model)", s.text)
	},
	subAnswer:  answerLines,
	subApprove: approveLines,
	subReject: func(s *subState, _ int) []string {
		return textLines("Reject "+sanitizeForDisplay(s.harp)+"'s plan", "feedback (required: the model revises the plan from it)", s.text)
	},
	subDenyAll: denyAllLines,
	subGrants:  grantsLines,
	subRevoke: func(s *subState, _ int) []string {
		return []string{"Revoke " + sanitizeForDisplay(s.grant.Rule) + " from " + sanitizeForDisplay(s.harp) + "?",
			"It stops applying from the child's next turn; a call already allowed in this turn is not undone."}
	},
}

// textLines is a sub-view with one text field. What was typed is child-free
// but may hold a paste, so it is sanitized for display too.
func textLines(title, label, text string) []string {
	return []string{title, "", label + ":", "> " + sanitizeForDisplay(text) + "_"}
}

func cursorMark(on bool) string {
	if on {
		return "▸ "
	}
	return "  "
}

func scopeLines(s *subState, _ int) []string {
	out := []string{"Allow for session — " + sanitizeForDisplay(s.harp), ""}
	if len(s.scopes) == 0 {
		return append(out, "The engine offered no session rule for this call.")
	}
	out = append(out, "Rule to grant:")
	for i, o := range s.scopes {
		out = append(out, cursorMark(i == s.cursor)+scopeLabel(o))
	}
	return append(out, "",
		"grants: "+scopeLabel(s.scopes[s.cursor]),
		"for "+sanitizeForDisplay(s.harp)+"'s run; revocable in Grants; never saved to a settings file")
}

func scopeLabel(o scopeOption) string {
	if o.rule != "" {
		return sanitizeForDisplay(o.rule)
	}
	m, _ := o.mode.Get()
	return "mode " + m.String() + " for the rest of this run"
}

func answerLines(s *subState, _ int) []string {
	out := []string{"Answer " + sanitizeForDisplay(s.harp) + "'s question(s) — space chooses, type in Other", ""}
	cur, _ := s.entry()
	for qi, q := range s.p.Ask.Questions {
		out = append(out, questionHead(q))
		for oi, o := range q.Options {
			on := answerEntry{q: qi, opt: oi} == cur
			out = append(out, "  "+cursorMark(on)+choiceMark(q.MultiSelect, s.picks[qi][oi])+" "+optionText(o))
		}
		on := answerEntry{q: qi, other: true} == cur
		out = append(out, "  "+cursorMark(on)+"Other: "+sanitizeForDisplay(s.others[qi])+"_")
	}
	return out
}

func choiceMark(multi, on bool) string {
	switch {
	case multi && on:
		return "[x]"
	case multi:
		return "[ ]"
	case on:
		return "(•)"
	}
	return "( )"
}

func approveLines(s *subState, _ int) []string {
	out := []string{"Approve " + sanitizeForDisplay(s.harp) + "'s plan — then continue in:", ""}
	if len(s.postures) == 0 {
		out = append(out, "no posture is within this agent's ceiling ("+s.p.Ceiling.String()+")")
	}
	for i, m := range s.postures {
		out = append(out, cursorMark(i == s.cursor)+m.String())
	}
	return append(out, "", "note (optional):", "> "+sanitizeForDisplay(s.text)+"_")
}

func denyAllLines(s *subState, _ int) []string {
	return []string{"Deny all " + strconv.Itoa(len(s.ids)) + " pending request(s) from " + sanitizeForDisplay(s.harp) + "?",
		"Each is denied; a request that arrives after this screen opened is not included."}
}

func grantsLines(s *subState, _ int) []string {
	out := []string{"Session grants held by " + sanitizeForDisplay(s.harp), ""}
	if len(s.grants) == 0 {
		return append(out, "none")
	}
	for i, g := range s.grants {
		out = append(out, cursorMark(i == s.cursor)+sanitizeForDisplay(g.Rule)+"  (since "+g.At.Format("15:04")+")")
	}
	return out
}

// button is one rendered action.
type button struct {
	label   string
	focused bool
	off     bool
}

// buttons is the action row: the sub-view's Back and primary, or the main
// actions.
func (a approvalsModel) buttons() []button {
	if a.sub != nil {
		ready, _ := subSpecs[a.sub.kind].ready(a.sub)
		return []button{
			{label: "Back", focused: a.sub.focus == 0},
			{label: subSpecs[a.sub.kind].primary, focused: a.sub.focus == 1, off: !ready},
		}
	}
	acts := a.actions()
	out := make([]button, len(acts))
	for i, act := range acts {
		out[i] = button{label: act.label, focused: i == a.focus}
	}
	return out
}

// flowButtons lays the buttons out left to right, wrapping rather than
// cutting one off: an action nobody can see is an action nobody can check.
func flowButtons(bs []button, w int) []string {
	var lines []string
	cur, curW := "", 0
	for _, b := range bs {
		text := "  " + b.label + "  "
		if b.focused {
			text = "[ " + b.label + " ]"
		}
		bw := lipgloss.Width(text) + 1
		if curW > 0 && curW+bw > w {
			lines = append(lines, cur)
			cur, curW = "", 0
		}
		cur += " " + styleButton(b).Render(text)
		curW += bw
	}
	return append(lines, cur)
}

func styleButton(b button) lipgloss.Style {
	switch {
	case b.focused:
		return styleFocus
	case b.off:
		return styleDim
	}
	return lipgloss.NewStyle()
}

// hintLine leads with the last outcome, then the arming state, then the keys.
func (a approvalsModel) hintLine(c apprChrome) string {
	var lead []string
	switch {
	case a.errMsg != "":
		lead = append(lead, a.errMsg)
	case a.note != "":
		lead = append(lead, a.note)
	}
	lead = append(lead, a.armingNote(c)...)
	keys := "↑↓ request · ←→/Tab action · Enter confirm · PgUp/PgDn scroll · Esc later"
	if a.sub != nil {
		keys = "←→/Tab button · ↑↓ choose · type to edit · Enter confirm · Esc back"
	}
	return " " + strings.Join(append(lead, keys), "  ─ ")
}

func (a approvalsModel) armingNote(c apprChrome) []string {
	switch {
	case c.arming:
		return []string{"arming — keys are ignored for a moment"}
	case a.now().Before(a.inertUntil):
		return []string{"re-arming — keys are ignored for a moment"}
	}
	discarded := c.armDiscarded + a.inertDiscarded
	if discarded > 0 {
		return []string{strconv.Itoa(discarded) + " key(s) ignored while arming"}
	}
	return nil
}
