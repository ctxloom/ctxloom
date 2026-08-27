// Package turnchange answers questions about the TURN NOW ENDING, off the
// engine's own transcript. Its original and principal question is the
// Stop-hook close-out contract's: did this turn perform a change-making
// action? LastAssistantText answers the other one — what did the turn
// SAY — for the TurnEnd next-step capture.
//
// The question it replaces is "is the working tree dirty". That proxy is
// wrong for the session shape that most needs a close-out checklist: a
// coordinator dispatches every edit into a separate git worktree, so its own
// checkout stays clean while the turn changed a great deal. The unit of
// measurement here is therefore the TURN — read out of the engine's own
// transcript — not the checkout the hook happens to run in.
//
// Nothing here parses a vendor transcript, and nothing here knows which
// engine wrote one. The caller hands in an already-selected
// vendorreader.VendorAdapter, which converts that engine's own store into the
// same agent.ChatEvent stream the live capture tee produces; this package only
// scopes that stream to the current turn and classifies it. Selecting the
// adapter is the caller's job because the choice is (engine, RECORDED
// version) -> adapter and neither key is derivable from the transcript
// itself (operations.ResolveTurnTranscript).
//
// FAILURE IS SAFE IN THE SPEAKING DIRECTION. Every failure — an unreadable
// file, a format this build cannot parse — ends as "changed". Silence is the
// defect this package exists to fix, and a spurious checklist is far cheaper
// than a missed one.
package turnchange

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// Decision is the outcome of classifying one turn.
type Decision struct {
	// Changed reports whether the turn performed a change-making action.
	Changed bool
	// Reason names the evidence for a Changed decision — the tool call that
	// made the change, or why classification could not be completed. Empty
	// when Changed is false.
	Reason string
}

// ClassifyEvents scopes evs to the current turn (CurrentTurn) and reports
// whether that turn performed a change-making action. A turn that only read,
// searched or answered is not a change.
func ClassifyEvents(evs []agent.ChatEvent) Decision {
	for _, ev := range CurrentTurn(evs) {
		e := ev.Entry
		if e == nil || e.Type != agent.EntryTypeToolUse {
			continue
		}
		if changed, reason := ToolChanges(e.ToolName, e.ToolInput); changed {
			return Decision{Changed: true, Reason: reason}
		}
	}
	return Decision{}
}

// CurrentTurn returns the tail of evs belonging to the turn now ending: every
// event from the last MAIN-THREAD user entry onwards.
//
// The user entry is the boundary rather than transcript.KindComplete, which
// mirrors agent.ChatEvent.Complete. claude-code emits one Complete per model
// RESPONSE — the vendor adapter closes a boundary when an assistant line's
// message.id changes (see vendorreader/claude's converter) — and a single
// Stop-hook turn contains one response per tool round trip. Scoping to the
// last Complete would therefore see only the final, tool-less reply and call
// every turn unchanged. A Stop hook fires once per USER-addressed turn, and a
// main-thread user entry is exactly where such a turn begins.
//
// Sidechain user entries are skipped deliberately: a subagent's own prompt is
// written as a user entry too, and treating it as a boundary would discard
// the very edits the subagent made — the evidence this package exists to find.
// With no user entry at all the whole stream is returned, keeping the
// fail-safe polarity (everything counts, rather than nothing).
func CurrentTurn(evs []agent.ChatEvent) []agent.ChatEvent {
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i].Entry
		if e != nil && e.Type == agent.EntryTypeUser && !e.Sidechain {
			return evs[i:]
		}
	}
	return evs
}

// writingTools name a tool call that writes a file directly.
var writingTools = map[string]bool{
	"edit": true, "write": true, "multiedit": true, "notebookedit": true,
	"applypatch": true, "apply_patch": true, "str_replace_editor": true,
}

// dispatchTools hand work to ANOTHER agent. They count as change-making
// because the child's edits land outside this transcript entirely — a child
// session of its own (mcp__ctxloom__agent_run) writes a separate transcript,
// and the worktree it edits is not the one this hook runs in. This is the
// coordinator case the tree-dirtiness guard could never see.
var dispatchTools = map[string]bool{
	"task": true, "agent": true, "sendmessage": true,
	"mcp__ctxloom__agent_run": true, "mcp__ctxloom__agent_send": true,
}

// shellTools run an arbitrary command, so their verdict comes from the
// command string rather than the tool name.
var shellTools = map[string]bool{"bash": true, "shell": true, "run_command": true}

// ToolChanges reports whether one tool call changes anything, and names the
// evidence when it does.
//
// The tool namespace and the shell get OPPOSITE polarities, deliberately. Tool
// names are enumerable and stable, so an unrecognized one is treated as
// read-only — the alternative fires the checklist on every turn, which is the
// nagging the guard exists to prevent. A shell command is the escape hatch
// every other tool can be spelled through, so there only a command proven
// read-only counts as read-only (see BashCommandChanges).
func ToolChanges(toolName string, input json.RawMessage) (bool, string) {
	name := strings.ToLower(strings.TrimSpace(toolName))
	switch {
	case writingTools[name]:
		return true, toolName
	case dispatchTools[name]:
		return true, toolName + " (dispatched work to another agent)"
	case !shellTools[name]:
		return false, ""
	}

	if len(input) == 0 {
		return false, ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		// A shell call whose input cannot be read is a shell call that might
		// have changed anything at all.
		return true, toolName + " (unreadable tool input)"
	}
	if in.Command == "" {
		return false, ""
	}
	if BashCommandChanges(in.Command) {
		return true, toolName + ": " + truncate(in.Command, 80)
	}
	return false, ""
}

// BashCommandChanges reports whether a shell command mutates anything.
//
// It answers by proving the command read-only, not by proving it mutating: a
// command is read-only only when every substitution and every pipeline segment
// runs a known read-only program with no writing redirection. Anything this
// function does not recognize is a change, so a tool or flag it has never
// heard of costs a spurious checklist rather than a missed one.
func BashCommandChanges(command string) bool {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return false
	}

	// Command substitutions carry a whole command of their own; judge each on
	// its own terms, then remove it so the outer pass sees only the shell it
	// is actually running.
	inner, rest := extractSubstitutions(cmd)
	for _, sub := range inner {
		if BashCommandChanges(sub) {
			return true
		}
	}

	stripped := stripHarmlessRedirections(rest)
	if strings.Contains(stripped, ">") {
		// What survives the strip is a redirection with a real destination.
		return true
	}

	for _, seg := range splitSegments(stripped) {
		if segmentChanges(seg) {
			return true
		}
	}
	return false
}

// segmentChanges judges one pipeline segment (no separators left in it).
func segmentChanges(seg string) bool {
	fields := strings.Fields(seg)
	for len(fields) > 0 {
		head := fields[0]
		switch {
		case skippableWords[head], isAssignment(head):
			fields = fields[1:]
			continue
		case neutralWords[head]:
			return false
		}
		break
	}
	if len(fields) == 0 {
		return false
	}

	name := baseName(fields[0])
	args := fields[1:]
	switch name {
	case "git":
		return !readOnlyGitSubcommand(args)
	case "sed":
		return hasFlagPrefix(args, "-i")
	case "find":
		return hasAnyWord(args, "-delete", "-exec", "-execdir", "-ok", "-fprint")
	}
	return !readOnlyCommands[name]
}

// readOnlyCommands are programs that only observe. Membership is the ONLY way
// a segment is judged read-only, so this list stays conservative: a program
// that writes under some flag combination does not belong here.
var readOnlyCommands = map[string]bool{
	"awk": true, "base64": true, "basename": true, "bat": true, "cat": true,
	"cd": true, "cksum": true, "column": true, "comm": true, "cmp": true,
	"cut": true, "date": true, "df": true, "diff": true, "dirname": true,
	"du": true, "echo": true, "egrep": true, "env": true, "expr": true,
	"false": true, "fd": true, "fgrep": true, "file": true, "free": true,
	"grep": true, "groups": true, "head": true, "hostname": true, "id": true,
	"jq": true, "less": true, "ls": true, "md5sum": true, "more": true,
	"nl": true, "nproc": true, "od": true, "paste": true, "printenv": true,
	"printf": true, "ps": true, "pwd": true, "readlink": true, "realpath": true,
	"rev": true, "rg": true, "seq": true, "sha1sum": true, "sha256sum": true,
	"sleep": true, "sort": true, "stat": true, "strings": true, "tac": true,
	"tail": true, "test": true, "tr": true, "tree": true, "true": true,
	"tty": true, "type": true, "uname": true, "uniq": true, "uptime": true,
	"wc": true, "which": true, "whoami": true, "xxd": true, "yq": true,
}

// readOnlyGitSubcommands are the git verbs that only report. Every other verb
// — including ones this build has never seen — is treated as mutating.
var readOnlyGitSubcommands = map[string]bool{
	"blame": true, "cat-file": true, "count-objects": true, "describe": true,
	"diff": true, "grep": true, "log": true, "ls-files": true, "ls-remote": true,
	"ls-tree": true, "merge-base": true, "name-rev": true, "rev-list": true,
	"rev-parse": true, "shortlog": true, "show": true, "status": true,
	"symbolic-ref": true, "var": true, "verify-commit": true, "whatchanged": true,
}

// skippableWords precede the real command without being one.
var skippableWords = map[string]bool{
	"sudo": true, "nohup": true, "command": true, "builtin": true,
	"exec": true, "time": true, "then": true, "do": true, "else": true,
	"!": true, "(": true, "{": true, "[[": true,
}

// neutralWords open or close a shell construct and run nothing themselves.
var neutralWords = map[string]bool{
	"for": true, "while": true, "if": true, "until": true, "case": true,
	"esac": true, "done": true, "fi": true, "elif": true, "in": true,
	"}": true, ")": true, "]]": true, "select": true,
}

// isAssignment reports whether tok is a bare NAME=VALUE prefix. Substitutions
// have already been lifted out by the time this runs, so the value left here
// cannot hide a command.
func isAssignment(tok string) bool {
	eq := strings.Index(tok, "=")
	if eq <= 0 {
		return false
	}
	for i, r := range tok[:eq] {
		isAlpha := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if isAlpha || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func readOnlyGitSubcommand(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		return readOnlyGitSubcommands[a]
	}
	// Bare `git` (or only flags) prints usage.
	return true
}

func hasFlagPrefix(args []string, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

func hasAnyWord(args []string, words ...string) bool {
	for _, a := range args {
		for _, w := range words {
			if a == w {
				return true
			}
		}
	}
	return false
}

// baseName drops a leading path from a command word (/usr/bin/rm -> rm).
func baseName(word string) string {
	if i := strings.LastIndexByte(word, '/'); i >= 0 {
		return word[i+1:]
	}
	return word
}

// splitSegments cuts a command into pipeline/list segments on the shell
// separators that start a new command word.
func splitSegments(cmd string) []string {
	return strings.FieldsFunc(cmd, func(r rune) bool {
		return r == ';' || r == '|' || r == '&' || r == '\n'
	})
}

// stripHarmlessRedirections removes the redirections that write no file: fd
// duplications (2>&1, >&2) and anything sent to /dev/null. A '>' surviving
// this has a real destination.
func stripHarmlessRedirections(cmd string) string {
	out := make([]byte, 0, len(cmd))
	for i := 0; i < len(cmd); {
		if n := matchHarmlessRedirection(cmd[i:]); n > 0 {
			i += n
			continue
		}
		out = append(out, cmd[i])
		i++
	}
	return string(out)
}

// matchHarmlessRedirection returns the length of a harmless redirection at the
// start of s, or 0. Shapes: [n]>[>] &n | - | /dev/null, and &>[>] /dev/null.
func matchHarmlessRedirection(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 && strings.HasPrefix(s, "&") {
		i++
	}
	if i >= len(s) || s[i] != '>' {
		return 0
	}
	i++
	if i < len(s) && s[i] == '>' {
		i++
	}
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	rest := s[i:]
	switch {
	case strings.HasPrefix(rest, "&"):
		j := 1
		for j < len(rest) && (rest[j] >= '0' && rest[j] <= '9' || rest[j] == '-') {
			j++
		}
		if j == 1 {
			return 0
		}
		return i + j
	case strings.HasPrefix(rest, "/dev/null"):
		return i + len("/dev/null")
	}
	return 0
}

// extractSubstitutions lifts every $(...) and `...` body out of cmd, returning
// the bodies and the command with them removed.
func extractSubstitutions(cmd string) (inner []string, rest string) {
	var out strings.Builder
	for i := 0; i < len(cmd); {
		switch {
		case strings.HasPrefix(cmd[i:], "$("):
			body, n := scanBalanced(cmd[i+2:])
			inner = append(inner, body)
			i += 2 + n
		case cmd[i] == '`':
			end := strings.IndexByte(cmd[i+1:], '`')
			if end < 0 {
				out.WriteByte(cmd[i])
				i++
				continue
			}
			inner = append(inner, cmd[i+1:i+1+end])
			i += end + 2
		default:
			out.WriteByte(cmd[i])
			i++
		}
	}
	return inner, out.String()
}

// scanBalanced reads up to the matching ')' and returns the body plus how many
// bytes of s were consumed (including the ')'), or all of s when unbalanced.
func scanBalanced(s string) (string, int) {
	depth := 1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[:i], i + 1
			}
		}
	}
	return s, len(s)
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// collector is the in-memory transcript.Recorder the vendor adapter converts
// into. Nothing is written to disk: this is a read-only question asked at
// Stop-hook latency, and the harp's own canonical transcript is written by
// the capture tee, not by a guard.
type collector struct{ events []agent.ChatEvent }

func (c *collector) Record(ev agent.ChatEvent) error {
	c.events = append(c.events, ev)
	return nil
}

func (c *collector) Close() error { return nil }

// ReadTranscript reads the vendor transcript at src through adapter — the
// same reader that produces ctxloom's canonical transcript — and returns its
// events.
//
// src is adapter's own locator, not necessarily a file path: a
// JSONL-per-session engine takes the transcript file, kiro takes its
// "<db-path>#<conversation-id>" composite (vendorreader.VendorAdapter).
//
// Exported so a second caller cannot arrive at the same bytes by a second
// route. A hand-rolled JSONL scan is the obvious shortcut for anyone who only
// wants "the last assistant line", and it is how a build ends up with two
// disagreeing notions of what a transcript entry is: the vendor format is the
// adapter's problem, and it already solves it for every engine ctxloom reads.
func ReadTranscript(ctx context.Context, adapter vendorreader.VendorAdapter, src string) ([]agent.ChatEvent, error) {
	c := &collector{}
	if err := adapter.Convert(ctx, c, src); err != nil {
		return nil, err
	}
	return c.events, nil
}

// ClassifyTranscript reads the vendor transcript at src through adapter and
// classifies its current turn.
//
// On failure it returns BOTH a changed Decision and the error, so a caller
// that only inspects the Decision still fails in the speaking direction.
func ClassifyTranscript(ctx context.Context, adapter vendorreader.VendorAdapter, src string) (Decision, error) {
	evs, err := ReadTranscript(ctx, adapter, src)
	if err != nil {
		return Decision{Changed: true, Reason: "transcript could not be read: " + err.Error()}, err
	}
	return ClassifyEvents(evs), nil
}

// LastAssistantText returns the text of the LAST main-thread assistant message
// in the turn now ending, or "" when the turn produced none.
//
// Scoped to CurrentTurn, so a turn that says nothing yields nothing rather
// than an answer recovered from an earlier turn — the caller overwrites
// per-turn storage with this, and a stale message re-presented as the current
// one is worse than no message at all.
//
// Sidechain entries are excluded: a subagent's closing words are its report to
// this agent, not this agent's statement of what IT intends to do next.
func LastAssistantText(evs []agent.ChatEvent) string {
	turn := CurrentTurn(evs)
	for i := len(turn) - 1; i >= 0; i-- {
		e := turn[i].Entry
		if e == nil || e.Type != agent.EntryTypeAssistant || e.Sidechain {
			continue
		}
		if text := strings.TrimSpace(e.Content); text != "" {
			return text
		}
	}
	return ""
}
