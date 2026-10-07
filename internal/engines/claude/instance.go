package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/kit"
)

// This file is the INSTANCE half of the port for claude: one kind bound to
// one session. Exec is the ONLY place claude's argv is composed — the
// interactive launch, the print (one-shot) launch and the structured drive
// all read it; buildArgs and the structured driver are projections onto it.
// The argv it emits parses against Definition.CLI (the conformance suite's
// anti-drift test) and is byte-identical to the launch golden
// (TestInstance_Exec_MatchesTheLaunchGolden).

// Instance is where REQUIREDNESS is refused, loudly: claude cannot run a
// session without a context surface to carry the system prompt.
func (c Claude) Instance(s engine.Session) (engine.Instance, error) {
	if c.Context == nil {
		return nil, engine.ErrUnsupported{Engine: c.Name, Capability: "context"}
	}
	pos, err := postureOf(s.Permission, s.Mode == engine.Interactive)
	if err != nil {
		return nil, err
	}
	return &instance{c: c, s: s, pos: pos}, nil
}

// Home: CLAUDE_CONFIG_DIR relocates claude's config into a session home,
// which gets its own .claude.json through claudeInstanceConfig (the account
// identity and the onboarding answers, carried across by name and nothing
// else). No credential is placed there: how a run authenticates is the
// agent's declared mode, resolved by claudeAuth.Credentials.
func (c Claude) Home() engine.HomeSpec {
	return engine.HomeSpec{
		Vars:           []engine.HomeVar{{Name: ConfigDirEnv, Subdir: HomeLeaf}},
		Auth:           engine.Provide[engine.Auth](claudeAuth{engine: string(c.Name)}),
		InstanceConfig: claudeInstanceConfig{},
		// claude writes each conversation (and its per-project memory) under
		// projects/ in its config dir — measured by the P14 probe cells, which
		// also proved it writes through a symlinked projects/.
		TranscriptStoreRel: TranscriptsDirName,
	}
}

// Container: no official image (ghcr.io/anthropics/claude-code appears in
// docs but does not resolve publicly), so the composed install fragment,
// which fetches the most recent claude, is the build source.
func (c Claude) Container() (engine.ContainerSpec, error) {
	return engine.ContainerSpec{
		Install:         installFragment,
		ValidateCommand: "claude --version",
		OverlayDirs:     []string{ConfigDirName},
		InPlaceFiles:    []string{MCPFileName},
	}, nil
}

// Transcripts are the readers the constructor was handed (WithTranscripts):
// the conversion of claude's own store is a transcript adapter, composed
// beside this kind at the root, never imported by it.
func (c Claude) Transcripts() []engine.TranscriptReader { return c.transcripts }

// TranscriptSession is a transcript's file stem: claude writes each session
// to <encoded-project>/<session-id>.jsonl.
func (c Claude) TranscriptSession(p string) (string, error) {
	base := path.Base(filepath.ToSlash(p))
	id, ok := strings.CutSuffix(base, transcriptExt)
	if !ok || id == "" {
		return "", fmt.Errorf("%w: %s", engine.ErrForeignTranscript, p)
	}
	return id, nil
}

// transcriptExt is the extension of every claude transcript.
const transcriptExt = ".jsonl"

// Hooks is claude's hook codec.
func (c Claude) Hooks() engine.HookCodec { return hookCodec{} }

// Wake is claude's cross-session messaging endpoint, bound in the session
// relay claude spawns (see messagingWake).
func (c Claude) Wake() engine.Declared[engine.WakeSpec] {
	return engine.Provide[engine.WakeSpec](messagingWake{})
}

// nativeHookEvents is claude's native event for each unified one. Two
// unified events (pre_shell, post_file_edit) are claude's PreToolUse and
// PostToolUse narrowed by a matcher, so decoding a native event yields the
// broad unified event, never the narrowed one.
var nativeHookEvents = []struct{ unified, native string }{
	{"pre_tool", hookEventPreToolUse},
	{"post_tool", "PostToolUse"},
	{"session_start", HookEventSessionStart},
	{"session_end", "SessionEnd"},
	{"turn_end", "Stop"},
	{"turn_start", HookEventUserPromptSubmit},
}

// hookEventMap is the unified→native event table as Exports carries it:
// the events claude fires, by the name it registers each under.
func hookEventMap() map[string]string {
	out := make(map[string]string, len(nativeHookEvents))
	for _, e := range nativeHookEvents {
		out[e.unified] = e.native
	}
	return out
}

// hookCodec decodes the JSON claude writes to a hook's stdin: the native
// session id and transcript path every payload carries, and the event —
// the payload's own hook_event_name when present, else the event the hook
// registration named.
type hookCodec struct{}

func (hookCodec) Decode(event string, payload []byte) (engine.HookEvent, error) {
	var p HookPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return engine.HookEvent{}, fmt.Errorf("claude hook payload: %w", err)
	}
	native := p.HookEventName
	if native == "" {
		native = event
	}
	out := engine.HookEvent{Event: native, NativeSession: p.SessionID, Transcript: p.TranscriptPath}
	for _, e := range nativeHookEvents {
		if e.native == native || e.unified == native {
			out.Event = e.unified
			break
		}
	}
	return out, nil
}

// instance is one claude kind bound to one session. key is the native
// session to continue, set by Resume.
type instance struct {
	c   Claude
	s   engine.Session
	pos posture
	key string
}

// Exec composes the process a launch of this session runs. The order is
// observable and pinned by the launch golden: the label's own args, the
// permission posture (with the plan grant over the session's MCP servers),
// the model, the session name (interactive only: print and structured runs
// are throwaway or named by their driver), --print for a non-interactive
// run, then every presentation's argv channel in delivery order (the
// out-of-cwd surface flags, the minimal posture), the resumed native key,
// and — interactive only — the prompt behind an explicit "--" terminator,
// because several claude flags are VARIADIC and whichever lands last before
// a positional swallows it. A print run carries its prompt on stdin
// (StdinPrompt) so a large task cannot exceed the OS argv limit.
//
// Env holds ONLY engine-native variables: the relocated home var(s) and
// whatever a presentation announced on the env channel. The runner lays
// identity and passthrough on top.
func (i *instance) Exec(presented []present.Presentation) (engine.Exec, error) {
	binary := i.s.Label.Binary
	if binary == "" {
		binary = "claude"
	}
	interactive := i.s.Mode == engine.Interactive
	args, err := i.execArgs(presented)
	if err != nil {
		return engine.Exec{}, err
	}
	ex := engine.Exec{Binary: binary, Args: args, Env: i.execEnv(presented), WorkDir: i.s.WorkDir, Interactive: interactive}
	i.attachPrompt(&ex)
	return ex, nil
}

// errSettingsTwice refuses an argv naming --settings twice: claude keeps
// only the LAST one given (measured on 2.1.285: the first document is
// dropped whole, not merged), so a second one would silently discard what
// the first carries.
var errSettingsTwice = errors.New("claude: the launch would name --settings twice, and claude keeps only the last one — a presentation already names a settings file; deliver settings to the session home instead")

// errTurnSettingsPresented refuses a structured turn whose exec already
// names --settings: a flag source is one --setting-sources does not filter,
// so a named file would load whatever it holds into the turn whatever the
// repository's verdict. The turn's own posture document is its only
// --settings; ctxloom's settings reach it through the session home.
var errTurnSettingsPresented = errors.New("claude: a structured turn's only --settings is its posture, and a presentation names another settings file — deliver settings to the session home instead")

// execArgs is the argv up to the prompt: the label's args, the permission
// posture (headless: permissionArgs; interactive: the human's own session,
// interactivePermissionArgs), the model, the session name (interactive) or
// --print, the repository's sources unless the verdict trusts it
// (repoSourceArgs), every presentation's args in delivery order, then the
// resumed native key. It refuses an argv naming --settings twice, and an
// untrusted session's presented --settings.
func (i *instance) execArgs(presented []present.Presentation) ([]string, error) {
	args := slices.Clone(i.s.Label.Args)
	interactive := i.s.Mode == engine.Interactive
	if interactive {
		posture, err := interactivePermissionArgs(i.pos, i.s.MCPServers)
		if err != nil {
			return nil, err
		}
		args = append(args, posture...)
	} else {
		args = append(args, permissionArgs(i.pos, i.s.MCPServers)...)
	}
	if i.s.Label.Model != "" {
		args = append(args, flagModel, i.s.Label.Model)
	}
	if interactive && i.s.Identity.Harp != "" {
		args = append(args, flagName, i.s.Identity.Harp)
	}
	if !interactive {
		args = append(args, flagPrint)
	}
	args = append(args, repoSourceArgs(i.s.Trust)...)
	surfaces, err := presentedArgs(presented, i.s.Trust, path.Join(i.s.Roots.ProjectRoot.Engine, MCPFileName))
	if err != nil {
		return nil, err
	}
	args = append(args, surfaces...)
	if i.key != "" {
		args = append(args, flagResume, i.key)
	}
	if countFlag(args, flagSettings) > 1 {
		return nil, errSettingsTwice
	}
	return args, nil
}

// presentedArgs is every presentation's args in delivery order. An
// untrusted session refuses one naming --settings (a source
// --setting-sources does not filter) and one that is the project's own
// .mcp.json, projectMCP (a file --strict-mcp-config ignores).
func presentedArgs(presented []present.Presentation, trust engine.WorkspaceTrust, projectMCP string) ([]string, error) {
	if trust == engine.TrustTrusted {
		return kit.PresentedArgs(presented, nil)
	}
	return kit.PresentedArgs(presented, func(p present.Presentation) error { return untrustedRefusal(p, projectMCP) })
}

// untrustedRefusal is why an untrusted session cannot take p, or nil.
func untrustedRefusal(p present.Presentation, projectMCP string) error {
	switch {
	case slices.Contains(p.Args, flagSettings):
		return errUntrustedSettingsPresented
	case p.EnginePath == projectMCP:
		return ErrUntrustedProjectMCP
	}
	return nil
}

// countFlag counts the occurrences of flag in args.
func countFlag(args []string, flag string) int {
	n := 0
	for _, a := range args {
		if a == flag {
			n++
		}
	}
	return n
}

// execEnv is the engine-native env: the relocated home vars, every
// presentation's env channel, and the classic-screen switch when
// interactive — or, when structured, background tasks off (the turn's
// process ends at its result, and a task left running past it would answer
// into a turn nobody reads).
func (i *instance) execEnv(presented []present.Presentation) map[string]string {
	env := kit.ComposeEnv(i.s, presented)
	if i.s.Mode == engine.Interactive {
		env[classicScreenEnv] = "1"
	} else {
		env[disableBackgroundTasksEnv] = "1"
	}
	return env
}

// attachPrompt puts the prompt behind "--" for an interactive run, and on
// stdin for a print run.
func (i *instance) attachPrompt(ex *engine.Exec) {
	if i.s.Prompt == "" {
		return
	}
	if ex.Interactive {
		ex.Args = append(ex.Args, "--", i.s.Prompt)
		return
	}
	ex.StdinPrompt = []byte(i.s.Prompt)
}

// Drivers: the stream-json conversation is claude's one structured driver,
// run on a discrete per-turn process (kit.ProcessTurn). claude supplies its
// argv (turnArgv), its NDJSON user message and its stream-json line codec
// (turnStream).
func (i *instance) Drivers() []engine.StructuredDriver {
	return []engine.StructuredDriver{kit.ProcessTurn{
		Name:        "claude",
		Argv:        i.turnArgv,
		WritePrompt: writeUserMessage,
		NewMapper:   func() kit.LineMapper { return &turnStream{} },
		Open:        i.c.open,
		Now:         i.c.now,
	}}
}

// Resume re-attaches the instance to a native session: the next Exec
// continues it.
func (i *instance) Resume(key string) error {
	i.key = key
	return nil
}

// turnArgv is claude's native structured protocol for one turn's process —
// `--print --input-format stream-json --output-format stream-json
// --verbose` — over the Exec (which already keeps an untrusted repository's
// sources out): the protocol flags, the native key the turn names (unless
// the Exec already resumes it), the session's name, so a delegated child's
// session is findable in claude's /resume picker, and the turn's posture as
// the process's one --settings (turnSettings). It refuses an Exec that
// already names --settings, whether or not the turn has a posture to say.
func (i *instance) turnArgv(ex engine.Exec, in engine.Turn) ([]string, error) {
	if slices.Contains(ex.Args, flagSettings) {
		return nil, errTurnSettingsPresented
	}
	args := slices.Clone(ex.Args)
	args = append(args, flagInputFormat, "stream-json", flagOutputFormat, "stream-json", flagVerbose)
	if in.Resume != "" && !slices.Contains(ex.Args, flagResume) {
		args = append(args, flagResume, in.Resume)
	}
	if harp := i.s.Identity.Harp; harp != "" {
		args = append(args, flagName, harp)
	}
	doc, err := turnSettings(i.pos, i.s.MCPServers, in.Posture)
	if err != nil {
		return nil, err
	}
	if doc != "" {
		args = append(args, flagSettings, doc)
	}
	return args, nil
}

var _ engine.Instance = (*instance)(nil)
