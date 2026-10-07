package cli

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// skillMatesResponse decides what one post_tool event earns: the skill-mates
// line when the completed tool call ran a skill (ev.Skill, which the firing
// engine's codec answered) whose link group has mates the session has not
// invoked yet, and silence otherwise -- a call that ran no skill, a skill in
// no group, or a group fully invoked.
//
// delivered is the skill set this run materialized for the engine (the same
// set the listing came from), and prior is the session transcript up to now,
// read for the skills already invoked through the same codec.
//
// It lives here, not in an engine package, because it is typed on the bundle
// model, and it is engine-neutral: the engine's part is the codec.
func skillMatesResponse(codec engine.HookCodec, ev engine.HookEvent, delivered []*bundles.LoadedSkill, prior []agent.ChatEvent) engine.HookResponse {
	if ev.Skill == "" {
		return engine.HookResponse{}
	}
	return engine.HookResponse{Context: skillMatesContext(ev.Skill, bundles.UninvokedSkillMates(delivered, ev.Skill, skillsInvoked(codec, prior)))}
}

// skillsInvoked derives "already invoked this session" from the transcript's
// own tool_use records, each asked of the codec (InvokedSkill), so nothing has
// to be persisted to answer it.
//
// Main thread only. A subagent's invocation is written to the same transcript
// as a sidechain entry, but the owner session never saw that skill's body --
// so for the owner it is exactly as uninvoked as if it had never fired, and
// counting it would silence the line on the very miss this hook closes.
func skillsInvoked(codec engine.HookCodec, evs []agent.ChatEvent) func(string) bool {
	seen := make(map[string]bool)
	for _, ev := range evs {
		e := ev.Entry
		if e == nil || e.Type != agent.EntryTypeToolUse || e.Sidechain {
			continue
		}
		if name, ok := codec.InvokedSkill(e.ToolName, e.ToolInput); ok {
			seen[name] = true
		}
	}
	return func(name string) bool { return seen[name] }
}

// skillMatesContext renders the one line the hook injects: the completed
// skill and the mates it leaves uninvoked. Names only -- prompt wording is the
// human's voice. Empty when there are no mates, so the hook stays silent
// rather than attaching an empty statement.
func skillMatesContext(completed string, mates []string) string {
	if len(mates) == 0 {
		return ""
	}
	return "Skill " + completed + " completed; linked skills not yet invoked this session: " + strings.Join(mates, ", ")
}
