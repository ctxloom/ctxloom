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
// ctxloom's hook commands itself with ctxloom's own hook wire
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

// toolIDs are opencode's tool ids in each neutral class.
var toolIDs = map[wire.ToolClass][]string{
	wire.ToolShell:    {"bash"},
	wire.ToolFileEdit: {"edit", "write", "apply_patch"},
	wire.ToolSkill:    {"skill"},
}

// toolMatcher is a class's tool ids as the regexp the bridge tests a tool
// call's id with.
func toolMatcher(c wire.ToolClass) (string, bool) {
	ids, ok := toolIDs[c]
	return "^(" + strings.Join(ids, "|") + ")$", ok
}

// ToolClass is the neutral class an opencode tool id belongs to.
func ToolClass(tool string) (wire.ToolClass, bool) {
	for c, ids := range toolIDs {
		if slices.Contains(ids, tool) {
			return c, true
		}
	}
	return "", false
}

// hookCodec is opencode's hook wire: ctxloom's own, written by the bridge.
// Context reaches the model on session_start (the session's system prompt),
// turn_start (a part of the prompt) and after a tool (its output); a throw
// blocks a tool call, a prompt, or a finished tool's result.
var hookCodec = kit.NativeHookCodec{
	Engine:     EngineName,
	Events:     hookEvents,
	Skill:      invokedSkill,
	EditedPath: editedPath,
	ContextOn:  []string{wire.HookEventSessionStart, wire.HookEventTurnStart, wire.HookEventPostTool, wire.HookEventPostFileEdit},
	BlockOn:    []string{wire.HookEventPreTool, wire.HookEventPreShell, wire.HookEventTurnStart, wire.HookEventPostTool, wire.HookEventPostFileEdit},
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
// opencode hook point, the wake socket's variable, and where transcripts go
// (a directory beneath a base variable's value).
type bridgeConfig struct {
	Hooks      map[string][]bridgeHook `json:"hooks"`
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
	for event, n := range narrowed {
		hs := slices.Clone(hooks.Event(event))
		for i := range hs {
			if hs[i].Matcher == "" && hs[i].Tool == "" {
				hs[i].Tool = n.class
			}
		}
		hooks.SetEvent(event, hs)
	}
	bound, err := agent.BindHooks(hooks, string(EngineName), toolMatcher)
	if err != nil {
		return nil, err
	}
	cfg := bridgeConfig{Hooks: map[string][]bridgeHook{}, WakeEnv: wake.EnvVar}
	cfg.Transcript.Env, cfg.Transcript.Rel = transcriptBase, transcriptRel
	points := hookEvents.Map()
	for _, event := range wire.HookEvents() {
		unified := event
		if n, ok := narrowed[event]; ok {
			unified = n.broad
		}
		if point, ok := points[unified]; ok {
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
