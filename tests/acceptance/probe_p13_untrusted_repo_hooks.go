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
//   - trusted-frontmatter-fires: the positive control for the repo's committed
//     SKILL and AGENT, whose frontmatter can declare hooks and mcpServers of
//     its own. claude honours agent frontmatter only from a folder it trusts,
//     so this arm trusts the repo and shows the fixture's agent hooks and MCP
//     server really execute; without it, the suppressing arm's silence could
//     be a fixture claude never runs.
//
// The flags keep skills and agents out by not LOADING them: with the project
// source off, the repo's skill and agent are absent from the init frame, so
// there is nothing for the model to invoke and no frontmatter to honour. The
// init frame is the mechanical half of that claim, the markers the other.
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
	"slices"
	"strings"
)

// p13Family is this rung's name in a skip line, a failure message and the
// evidence line.
const p13Family = "untrusted-repo-hooks"

// p13Variant is the cell's posture; the two arms are two addressable cells.
type p13Variant string

const (
	p13Fires              p13Variant = "untrusted-fires"
	p13Suppresses         p13Variant = "setting-sources-suppresses"
	p13TrustedFrontmatter p13Variant = "trusted-frontmatter-fires"
)

// The turn every cell takes: invoke the repo's skill, then one Bash call the
// flag-scope settings allow (so the PreToolUse hooks have a call to fire on),
// then the repo's agent in the foreground (so its frontmatter runs before the
// turn ends). Where the skill and agent are not loaded, the model cannot
// invoke them and the echo still runs.
const (
	p13Prompt           = "Do these three steps in order. 1: invoke the " + p13RepoSkill + " skill. 2: run echo hi with Bash. 3: use the " + p13RepoAgent + " subagent in the foreground (not in the background)."
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

// The repo's committed skill and agent, and the marker each frontmatter
// surface writes, again in the cell's directory outside the repo.
const (
	p13RepoSkill       = "p13-skill"
	p13RepoAgent       = "p13-agent"
	p13RepoSkillPath   = ".claude/skills/" + p13RepoSkill + "/SKILL.md"
	p13RepoAgentPath   = ".claude/agents/" + p13RepoAgent + ".md"
	p13SkillHooks      = "skill hooks"
	p13AgentHooks      = "agent hooks"
	p13AgentMCPServers = "agent mcpServers"
)

// p13FrontmatterMarkers is every marker the repo's skill and agent frontmatter
// writes, keyed by the surface that writes it. No marker may appear under the
// flags.
var p13FrontmatterMarkers = map[string]string{
	p13SkillHooks:      "skill-hook-marker",
	p13AgentHooks:      "agent-hook-marker",
	p13AgentMCPServers: "agent-mcp-marker",
}

// p13ControlSurfaces are the frontmatter surfaces the trusted control requires
// to fire. Skill hooks are not among them: claude 2.1.286 did not run a
// skill's frontmatter PreToolUse hook under -p even in a trusted repo, so the
// skill's absence from the init frame is what the suppressing arm judges it
// by, and its marker only has to stay absent.
var p13ControlSurfaces = []string{p13AgentHooks, p13AgentMCPServers}

// p13Markers is every marker the repo's hooks write, keyed by the hook event
// that writes it. The verdict walks this, so a hook added here is asserted in
// both arms without another edit.
var p13Markers = map[string]string{
	p13PreToolEvent:      p13PreToolMarker,
	p13SessionStartEvent: p13SessionMarker,
}

// p13Args is the cell's whole argv after the binary. The suppressing arm adds
// exactly the flags ctxloom's repo-trust design launches an untrusted repo
// with; nothing else differs between the arms. The trusted control's trust is
// in its config dir (p13TrustJSON), not its argv.
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

// p13SkillMD renders the repo's committed SKILL.md: a PreToolUse hook on the
// gated tool touching its marker under dir. Frontmatter values are JSON, which
// is YAML, so a path needs no YAML quoting of its own.
func p13SkillMD(dir string) ([]byte, error) {
	hooks, err := json.Marshal(map[string][]p12HookMatcher{p13PreToolEvent: {p13Touch(dir, p13SkillHooks, p12GatedTool)}})
	if err != nil {
		return nil, err
	}
	return p13Frontmatter(p13RepoSkill, "The p13 probe skill. Use it whenever the user asks for the p13 skill.",
		"hooks: "+string(hooks), "Run the bash command `echo hi`."), nil
}

// p13AgentMD renders the repo's committed agent: a Stop hook (claude runs it
// as the subagent's SubagentStop) and an inline stdio MCP server whose command
// is itself the marker's touch, so the server need not speak MCP to leave a
// trace.
func p13AgentMD(dir string) ([]byte, error) {
	hooks, err := json.Marshal(map[string][]p12HookMatcher{"Stop": {p13Touch(dir, p13AgentHooks, "")}})
	if err != nil {
		return nil, err
	}
	mcp, err := json.Marshal([]map[string]any{{"p13mcp": map[string]any{
		"type": "stdio", "command": "sh", "args": []string{"-c", "touch " + p12ShellQuote(dir+"/"+p13FrontmatterMarkers[p13AgentMCPServers])},
	}}})
	if err != nil {
		return nil, err
	}
	return p13Frontmatter(p13RepoAgent, "The p13 probe subagent. Use it whenever the user asks for the p13 agent.",
		"mcpServers: "+string(mcp)+"\nhooks: "+string(hooks), "Reply with the single word ok."), nil
}

func p13Touch(dir, surface, matcher string) p12HookMatcher {
	return p12HookMatcher{Matcher: matcher, Hooks: []p12HookCommand{{Type: "command", Command: "touch " + p12ShellQuote(dir+"/"+p13FrontmatterMarkers[surface])}}}
}

func p13Frontmatter(name, description, fields, body string) []byte {
	return []byte("---\nname: " + name + "\ndescription: " + description + "\n" + fields + "\n---\n" + body + "\n")
}

// p13TrustJSON renders the trusted control's CLAUDE_CONFIG_DIR/.claude.json:
// the repo's projects entry with the trust dialog accepted, which is what
// claude reads as a trusted folder.
func p13TrustJSON(repo string) ([]byte, error) {
	return json.Marshal(map[string]any{"projects": map[string]any{repo: map[string]bool{"hasTrustDialogAccepted": true}}})
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
	// Frontmatter does the same for each p13FrontmatterMarkers surface;
	// MarkerErr is a stat failure that is NOT plain absence.
	Fired       map[string]bool
	Frontmatter map[string]bool
	MarkerErr   error
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
	// shapeRepoSurfaceUnlisted: without the flags, the init frame did not list
	// the repo's committed skill or agent. claude no longer loads them from a
	// repo, or the fixture is not one it recognises; either way the arm
	// measured nothing about them.
	shapeRepoSurfaceUnlisted probeShape = "REPO-SURFACE-UNLISTED failure"
	// shapeRepoSurfaceLoaded: under --setting-sources user the init frame
	// still listed the repo's skill or agent, so an untrusted repo's skills and
	// agents are invocable in ctxloom's children.
	shapeRepoSurfaceLoaded probeShape = "REPO-SURFACE-LOADED failure"
	// shapeFrontmatterSilent: in a trusted repo the agent's frontmatter hook or
	// MCP server left no marker, so the fixture does not exercise frontmatter
	// and the suppressing arm's silence proves nothing.
	shapeFrontmatterSilent probeShape = "FRONTMATTER-SILENT failure"
	// shapeEchoNotRun: the allowed echo did not run and print its output, so a
	// silent hook cannot be told apart from an untriggered one.
	shapeEchoNotRun probeShape = "ECHO-NOT-RUN failure"
)

func (o p13Outcome) verdict() probeVerdict {
	return probeVerdict{Family: p13Family, Cell: o.Cell, Channel: channelRepoHookMarker}
}

func (o p13Outcome) evidence() string {
	return fmt.Sprintf("\nfired=%v frontmatter=%v markerErr=%v\nexit=%d runErr=%v timedOut=%t\nstdout:\n%s\nstderr:\n%s",
		o.Fired, o.Frontmatter, o.MarkerErr, o.Run.ExitCode, o.Run.Err, o.TimedOut, o.Run.Stdout, o.Run.Stderr)
}

// p13Assert judges one cell: the run completed, the allowed echo ran and
// printed, then the arm the cell's variant names.
func p13Assert(o p13Outcome) error {
	v := o.verdict()
	s, err := decodeGatedRun(v, gatedRun{
		Started: o.Started, TimedOut: o.TimedOut, Run: o.Run, Evidence: o.evidence(),
		NoResult: "the stream carried no result frame, so the turn did not finish",
		NoCall:   "the model made no " + p12GatedTool + " tool_use, so the PreToolUse hook had nothing to fire on and the cell measured nothing",
	})
	if err != nil {
		return err
	}
	if !p13EchoRan(s) {
		return v.fail(shapeEchoNotRun, fmt.Sprintf("no %s tool_result printed a line %q without error", p12GatedTool, p13EchoOutput), o.evidence())
	}
	if o.MarkerErr != nil {
		return v.fail(shapeRunFailed, fmt.Sprintf("a hook marker could not be checked: %v", o.MarkerErr), o.evidence())
	}
	return o.judgeVariant(v, s)
}

// judgeVariant is the arm the cell's variant names.
func (o p13Outcome) judgeVariant(v probeVerdict, s p12Stream) error {
	switch o.Variant {
	case p13Fires:
		return o.judgeFires(v, s)
	case p13Suppresses:
		return o.judgeSuppresses(v, s)
	case p13TrustedFrontmatter:
		return o.judgeTrustedControl(v, s)
	}
	return fmt.Errorf("%s %s: unknown variant %q (want %q, %q or %q)", p13Family, o.Cell, o.Variant, p13Fires, p13Suppresses, p13TrustedFrontmatter)
}

// judgeFires: every committed settings hook fired, and the repo's skill and
// agent were loaded. Frontmatter markers are not judged here: claude's own
// folder trust decides them, and this arm's repo is untrusted.
func (o p13Outcome) judgeFires(v probeVerdict, s p12Stream) error {
	if _, silent := p13Split(p13Markers, o.Fired); len(silent) > 0 {
		return v.fail(shapeRepoHookSilent, fmt.Sprintf("the untrusted repo's committed %v hook(s) did not run under -p", silent), o.evidence())
	}
	if _, unlisted := p13Listing(s); len(unlisted) > 0 {
		return v.fail(shapeRepoSurfaceUnlisted, fmt.Sprintf("the init frame did not list the repo's %v", unlisted), o.evidence())
	}
	return nil
}

// judgeSuppresses: no settings hook and no frontmatter surface left a marker,
// and the repo's skill and agent were not loaded at all.
func (o p13Outcome) judgeSuppresses(v probeVerdict, s p12Stream) error {
	fired, _ := p13Split(p13Markers, o.Fired)
	surfaced, _ := p13Split(p13FrontmatterMarkers, o.Frontmatter)
	if leaked := append(fired, surfaced...); len(leaked) > 0 {
		return v.fail(shapeRepoHookLeaked, fmt.Sprintf("the repo's committed %v ran despite --setting-sources user --strict-mcp-config", leaked), o.evidence())
	}
	if listed, _ := p13Listing(s); len(listed) > 0 {
		return v.fail(shapeRepoSurfaceLoaded, fmt.Sprintf("the init frame listed the repo's %v despite --setting-sources user", listed), o.evidence())
	}
	return nil
}

// judgeTrustedControl: in a trusted repo the skill and agent were loaded and
// every control surface left its marker.
func (o p13Outcome) judgeTrustedControl(v probeVerdict, s p12Stream) error {
	if _, unlisted := p13Listing(s); len(unlisted) > 0 {
		return v.fail(shapeRepoSurfaceUnlisted, fmt.Sprintf("the init frame did not list the trusted repo's %v", unlisted), o.evidence())
	}
	var silent []string
	for _, surface := range p13ControlSurfaces {
		if !o.Frontmatter[surface] {
			silent = append(silent, surface)
		}
	}
	if len(silent) > 0 {
		return v.fail(shapeFrontmatterSilent, fmt.Sprintf("the trusted repo's agent %v left no marker", silent), o.evidence())
	}
	return nil
}

// p13Split partitions markers' keys into those observed and those not, sorted
// so a failure message is stable.
func p13Split(markers map[string]string, observed map[string]bool) (seen, unseen []string) {
	for key := range markers {
		if observed[key] {
			seen = append(seen, key)
		} else {
			unseen = append(unseen, key)
		}
	}
	slices.Sort(seen)
	slices.Sort(unseen)
	return seen, unseen
}

// p13Listing partitions the repo's skill and agent into those the init frame
// listed and those it did not.
func p13Listing(s p12Stream) (listed, unlisted []string) {
	for _, l := range []struct {
		kind, name string
		loaded     []string
	}{{"skill", p13RepoSkill, s.Skills}, {"agent", p13RepoAgent, s.Agents}} {
		if slices.Contains(l.loaded, l.name) {
			listed = append(listed, l.kind+" "+l.name)
		} else {
			unlisted = append(unlisted, l.kind+" "+l.name)
		}
	}
	return listed, unlisted
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
