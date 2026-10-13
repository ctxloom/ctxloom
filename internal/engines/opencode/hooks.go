package opencode

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/kit"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// This file is opencode's HOOKS: opencode runs no external hook command, only
// in-process plugins loaded from its config dirs, so the hooks approach
// writes ONE plugin, the bridge (bridge.js, H/plugins/ctxloom.js), that fires
// ctxloom's hook commands itself: on the pre-tool point with the hook wire,
// claude's PreToolUse (claudeTools); everywhere else with ctxloom's own
// (kit.NativeHookCodec). The same plugin carries what a live TUI needs and no
// hook command can give: the session owner's wake (a unix socket,
// kit.SocketWake) and the transcript ctxloom reads back (transcript.go).
//
// Loading any plugin makes opencode wait at startup on its own npm install
// into each config dir (@opencode-ai/plugin); an offline host sees opencode's
// npm failure there. That is opencode's behaviour, tolerated (ruling Q-K).

// The bridge's file under H: opencode loads {plugin,plugins}/*.{ts,js} from
// every config dir.
const pluginRel = "plugins/ctxloom.js"

// hookEvents are the opencode hook points the bridge fires each unified event
// on. pre_shell and post_file_edit ride the tool points narrowed to their
// tool class (bridgeHooks), so they decode as the broad event.
var hookEvents = kit.HookEvents{
	{Unified: wire.HookEventPreTool, Native: "tool.execute.before"},
	{Unified: wire.HookEventPostTool, Native: "tool.execute.after"},
	{Unified: wire.HookEventSessionStart, Native: "session.created"},
	{Unified: wire.HookEventTurnStart, Native: "chat.message"},
	{Unified: wire.HookEventTurnEnd, Native: "session.idle"},
}

// narrowed are the unified events carried on a broad one's point, narrowed to
// a tool class when the hook names no matcher of its own.
var narrowed = map[string]struct {
	broad string
	class wire.ToolClass
}{
	wire.HookEventPreShell:     {wire.HookEventPreTool, wire.ToolShell},
	wire.HookEventPostFileEdit: {wire.HookEventPostTool, wire.ToolFileEdit},
}

// hookLosses are the unified events opencode gives a plugin no point for.
var hookLosses = map[string]string{
	wire.HookEventSessionEnd:    "opencode tells a plugin of no session ending",
	wire.HookEventPermissionAsk: "opencode never invokes its permission.ask plugin hook (upstream #47654)",
}

// claudeTool is one opencode tool in claude's spelling: its claude name and
// the input keys renamed to claude's (a key not listed is kept as it is).
type claudeTool struct {
	Name string            `json:"name"`
	Keys map[string]string `json:"keys,omitempty"`
}

// claudeTools normalises opencode's tool calls to claude's names for THE HOOK
// WIRE (ruling #28): ctxloom's cross-engine command-hook wire is claude's
// PreToolUse, which the bridge writes on opencode's pre-tool point with each
// tool named and its input keyed as claude's own tool is. The ids are
// opencode 1.18.35's (its shell tool keeps the id "bash" until opencode 2.0).
// apply_patch has no claude counterpart and keeps its own name and input; a
// tool not listed here (an MCP or plugin tool) passes verbatim. The bridge
// matches every hook's matcher against the normalised name.
var claudeTools = map[string]claudeTool{
	"bash":        {Name: "Bash"},
	"edit":        {Name: "Edit", Keys: map[string]string{"filePath": "file_path", "oldString": "old_string", "newString": "new_string", "replaceAll": "replace_all"}},
	"write":       {Name: "Write", Keys: map[string]string{"filePath": "file_path"}},
	"read":        {Name: "Read", Keys: map[string]string{"filePath": "file_path"}},
	"glob":        {Name: "Glob"},
	"grep":        {Name: "Grep", Keys: map[string]string{"include": "glob"}},
	"webfetch":    {Name: "WebFetch"},
	"websearch":   {Name: "WebSearch"},
	"task":        {Name: "Task"},
	"todowrite":   {Name: "TodoWrite"},
	"skill":       {Name: "Skill", Keys: map[string]string{"name": "skill"}},
	"apply_patch": {Name: "apply_patch"},
}

// classTools are the normalised names of opencode's tools in each neutral
// class.
var classTools = map[wire.ToolClass][]string{
	wire.ToolShell:    {"Bash"},
	wire.ToolFileEdit: {"Edit", "Write", "apply_patch"},
	wire.ToolSkill:    {"Skill"},
}

// toolMatcher is a class's normalised tool names as the matcher the bridge
// tests a call's normalised name with: an exact name list, as claude reads
// one.
func toolMatcher(c wire.ToolClass) (string, bool) {
	names, ok := classTools[c]
	return strings.Join(names, "|"), ok
}

// hookCodec is opencode's hook wire on every point but the pre-tool one:
// ctxloom's own, written by the bridge, with opencode's own tool ids. Context
// reaches the model on session_start (the session's system prompt),
// turn_start (a part of the prompt) and after a tool (its output); a throw
// blocks a prompt or a finished tool's result. The pre-tool point carries the
// hook wire (claudeTools) instead, so no answer here blocks a tool call.
var hookCodec = kit.NativeHookCodec{
	Engine:     EngineName,
	Events:     hookEvents,
	Skill:      invokedSkill,
	EditedPath: editedPath,
	ContextOn:  []string{wire.HookEventSessionStart, wire.HookEventTurnStart, wire.HookEventPostTool, wire.HookEventPostFileEdit},
	BlockOn:    []string{wire.HookEventTurnStart, wire.HookEventPostTool, wire.HookEventPostFileEdit},
}

// toolInput is the fields of opencode's tool inputs the codec reads.
type toolInput struct {
	Name     string `json:"name"`
	FilePath string `json:"filePath"`
}

// invokedSkill: opencode's skill tool names the skill in its input.
func invokedSkill(tool string, input []byte) (string, bool) {
	var in toolInput
	if tool != "skill" || json.Unmarshal(input, &in) != nil || in.Name == "" {
		return "", false
	}
	return in.Name, true
}

// editedPath: edit and write name their file; apply_patch carries it inside
// the patch text, unread.
func editedPath(tool string, input []byte) string {
	var in toolInput
	if (tool != "edit" && tool != "write") || json.Unmarshal(input, &in) != nil {
		return ""
	}
	return in.FilePath
}

//go:embed bridge.js
var bridgeTemplate string

// configMarker is where the bridge's config is spliced into the template.
const configMarker = "/*CTXLOOM_CONFIG*/{}"

// bridgeConfig is what the bridge is generated with: the hook commands by
// opencode hook point, the tool table the hook wire is normalised by
// (claudeTools), the wake socket's variable, and where transcripts go
// (a directory beneath a base variable's value).
type bridgeConfig struct {
	Hooks      map[string][]bridgeHook `json:"hooks"`
	Tools      map[string]claudeTool   `json:"tools"`
	WakeEnv    string                  `json:"wakeEnv"`
	Transcript struct {
		Env string `json:"env"`
		Rel string `json:"rel"`
	} `json:"transcript"`
}

type bridgeHook struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Matcher string   `json:"matcher,omitempty"`
	Timeout int      `json:"timeout,omitempty"`
}

// bridgePlugin renders the bridge for a unified hook set (and the hooks the
// label declares by opencode hook point, ext): every command hook bound to
// opencode (agent.BindHooks), filed under the point its event fires on; a
// lost event's hooks are dropped (the plugin fires nothing for it).
func bridgePlugin(hooks wire.UnifiedHooks, ext wire.BackendHooks) ([]byte, error) {
	bound, err := agent.BindHooks(narrowToClass(hooks), string(EngineName), toolMatcher)
	if err != nil {
		return nil, err
	}
	cfg := bridgeConfig{Hooks: map[string][]bridgeHook{}, Tools: claudeTools, WakeEnv: wake.EnvVar}
	cfg.Transcript.Env, cfg.Transcript.Rel = transcriptBase, transcriptRel
	points := hookEvents.Map()
	for _, event := range wire.HookEvents() {
		if point, ok := points[broadEvent(event)]; ok {
			cfg.Hooks[point] = appendCommands(cfg.Hooks[point], bound.Event(event))
		}
	}
	for point, hs := range ext {
		if hookEvents.Unified(point) != point { // one of the bridge's points
			cfg.Hooks[point] = appendCommands(cfg.Hooks[point], hs)
		}
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return []byte(strings.Replace(bridgeTemplate, configMarker, string(b), 1)), nil
}

// narrowToClass narrows each pre_shell and post_file_edit hook that names no
// matcher of its own to its tool class (BindHooks then spells the class as
// opencode's matcher), on a copy: the caller's set is not mutated.
func narrowToClass(hooks wire.UnifiedHooks) wire.UnifiedHooks {
	for event, n := range narrowed {
		hs := slices.Clone(hooks.Event(event))
		for i := range hs {
			if hs[i].Matcher == "" && hs[i].Tool == "" {
				hs[i].Tool = n.class
			}
		}
		hooks.SetEvent(event, hs)
	}
	return hooks
}

// broadEvent is the event whose point a unified event fires on.
func broadEvent(event string) string {
	if n, ok := narrowed[event]; ok {
		return n.broad
	}
	return event
}

// appendCommands adds the command hooks of hs (a prompt or agent hook has no
// opencode form).
func appendCommands(dst []bridgeHook, hs []wire.Hook) []bridgeHook {
	for _, h := range hs {
		if h.Type != "" && h.Type != "command" {
			continue
		}
		dst = append(dst, bridgeHook{Command: h.Command, Args: h.Args, Matcher: h.Matcher, Timeout: h.Timeout})
	}
	return dst
}

// hooksApproach writes the bridge as H/plugins/ctxloom.js, a whole file it
// owns, announced on OPENCODE_CONFIG_DIR. It is written even with no hook to
// fire: the wake and the transcript ride it.
type hooksApproach struct{ kit.Approach }

func (a *hooksApproach) DeliverHooks(start present.Start, root present.RootKind, in engine.HooksInputs, fs afero.Fs) (present.Delivered, error) {
	src, err := bridgePlugin(in.Hooks, in.Ext[string(EngineName)])
	if err != nil {
		return present.Delivered{}, err
	}
	r, err := a.Rooted(start, root, pluginRel, "")
	if err != nil {
		return present.Delivered{}, err
	}
	p := announce(r).Build()
	fs = agent.GetFS(fs)
	if err := fs.MkdirAll(filepath.Dir(p.HostPath), 0o755); err != nil {
		return present.Delivered{}, fmt.Errorf("%s/%s: create the plugin dir: %w", EngineName, a.ApproachName, err)
	}
	if err := safefs.WriteFileKeepMode(fs, p.HostPath, src, filepath.Base(pluginRel)); err != nil {
		return present.Delivered{}, fmt.Errorf("%s/%s: write the bridge plugin: %w", EngineName, a.ApproachName, err)
	}
	return present.Delivered{Presented: p, Files: []string{p.HostPath}}, nil
}

var _ engine.HooksApproach = (*hooksApproach)(nil)

