package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
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
	return &instance{c: c, s: s}, nil
}

// Home: CLAUDE_CONFIG_DIR relocates claude's config into a session home,
// which gets its own .claude.json through claudeInstanceConfig (the account
// identity and the onboarding answers, carried across by name and nothing
// else). No credential is placed there: how a run authenticates is the
// agent's declared mode, resolved by claudeAuth.Credentials.
func (c Claude) Home() engine.HomeSpec {
	return engine.HomeSpec{
		Vars:           []engine.HomeVar{{Name: ConfigDirEnv, Subdir: HomeLeaf}},
		Auth:           engine.Provide[engine.Auth](claudeAuth{binary: "claude", engine: string(c.Name)}),
		InstanceConfig: claudeInstanceConfig{},
	}
}

// Container: no official image (ghcr.io/anthropics/claude-code appears in
// docs but does not resolve publicly), so the composed install fragment,
// which fetches the most recent claude, is the build source.
func (c Claude) Container() (engine.ContainerSpec, error) {
	return engine.ContainerSpec{
		Install:            installFragment,
		ValidateCommand:    "claude --version",
		OverlayDirs:        []string{ConfigDirName},
		TranscriptStoreRel: path.Join(ConfigDirName, TranscriptsDirName),
	}, nil
}

// Transcripts are the readers the constructor was handed (WithTranscripts):
// the conversion of claude's own store is a transcript adapter, composed
// beside this kind at the root, never imported by it.
func (c Claude) Transcripts() []engine.TranscriptReader { return c.transcripts }

// Hooks is claude's hook codec.
func (c Claude) Hooks() engine.HookCodec { return hookCodec{} }

// Wake is claude's cross-session messaging socket, and it is NOT declared
// yet: a bypass-permissions receiver may hold a post it cannot attribute to
// its own child, and the runner is its parent. Until that is measured the
// absence is the truth.
func (c Claude) Wake() engine.Declared[engine.WakeSpec] {
	return engine.Absent[engine.WakeSpec]("claude's wake is its cross-session messaging socket, which is not bound until a bypass-permissions session is measured to accept the runner's post")
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
	ex := engine.Exec{Binary: binary, Args: i.execArgs(presented), Env: i.execEnv(presented), WorkDir: i.s.WorkDir, Interactive: interactive}
	i.attachPrompt(&ex)
	return ex, nil
}

// execArgs is the argv up to the prompt: the label's args, the permission
// posture, the model, the session name (interactive) or --print, every
// presentation's args in delivery order, then the resumed native key.
func (i *instance) execArgs(presented []present.Presentation) []string {
	args := slices.Clone(i.s.Label.Args)
	args = append(args, permissionArgs(i.s.Permission, i.s.MCPServers)...)
	if i.s.Label.Model != "" {
		args = append(args, flagModel, i.s.Label.Model)
	}
	interactive := i.s.Mode == engine.Interactive
	if interactive && i.s.Identity.Harp != "" {
		args = append(args, flagName, i.s.Identity.Harp)
	}
	if !interactive {
		args = append(args, flagPrint)
	}
	for _, p := range presented {
		args = append(args, p.Args...)
	}
	if i.key != "" {
		args = append(args, flagResume, i.key)
	}
	return args
}

// execEnv is the engine-native env: the relocated home vars, every
// presentation's env channel, and the classic-screen switch when
// interactive.
func (i *instance) execEnv(presented []present.Presentation) map[string]string {
	env := map[string]string{}
	for _, h := range i.s.Home {
		env[h.Var] = h.Path
	}
	for _, p := range presented {
		maps.Copy(env, p.Env)
	}
	if i.s.Mode == engine.Interactive {
		env[classicScreenEnv] = "1"
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

// Drivers: the stream-json conversation is claude's one structured driver.
func (i *instance) Drivers() []engine.StructuredDriver {
	return []engine.StructuredDriver{&streamJSONDriver{inst: i}}
}

// Resume re-attaches the instance to a native session: the next Exec
// continues it.
func (i *instance) Resume(key string) error {
	i.key = key
	return nil
}

// streamJSONDriver runs claude's native structured protocol — `--print
// --input-format stream-json --output-format stream-json --verbose` — for
// one turn on a discrete per-turn process: the Exec the instance composed,
// the protocol flags, the native key the turn names (unless the Exec
// already resumes it) and the session's name, so a delegated child's
// session is findable in claude's /resume picker.
type streamJSONDriver struct{ inst *instance }

// argv is the per-turn process's argv: Exec plus the protocol.
func (d *streamJSONDriver) argv(ex engine.Exec, in engine.Turn) []string {
	args := slices.Clone(ex.Args)
	args = append(args, flagInputFormat, "stream-json", flagOutputFormat, "stream-json", flagVerbose)
	if in.Resume != "" && !slices.Contains(ex.Args, flagResume) {
		args = append(args, flagResume, in.Resume)
	}
	if harp := d.inst.s.Identity.Harp; harp != "" {
		args = append(args, flagName, harp)
	}
	return args
}

// Turn spawns one stream-json process, writes the one user message, relays
// every native event and returns the native key the next turn resumes by
// with the assistant's answer. The env is the Exec's laid over the
// process's own (spawnChatTransport merges it onto os.Environ). A nil out
// relays nothing.
func (d *streamJSONDriver) Turn(ctx context.Context, ex engine.Exec, in engine.Turn, out chan<- engine.Event) (engine.TurnResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	open, now := d.seams()
	tr, err := open(ctx, ex.Binary, d.argv(ex, in), ex.Env, ex.WorkDir)
	if err != nil {
		return engine.TurnResult{}, err
	}
	events := make(chan agent.ChatEvent, 64)
	readerDone := make(chan struct{})
	go func() {
		readChatEvents(ctx, tr.stdout, events, readerDone, now)
		close(events) // readChatEvents closes readerDone, never its output
	}()
	if err := writeUserMessage(tr.stdin, in.Prompt); err != nil {
		_ = tr.Close()
		<-readerDone
		return engine.TurnResult{}, err
	}
	_ = tr.stdin.Close()
	var res engine.TurnResult
	var answer []string
	for {
		select {
		case <-ctx.Done():
			_ = tr.Close()
			<-readerDone
			return res, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				<-readerDone
				_ = tr.Close()
				res.Answer = strings.Join(answer, "")
				return res, nil
			}
			answer = absorbChatEvent(&res, answer, ev)
			cancelled, err := relayChatEvent(ctx, out, ev)
			if cancelled {
				_ = tr.Close()
				<-readerDone
				return res, err
			}
			if err != nil {
				return res, err
			}
		}
	}
}

// seams is the transport opener and clock, the real ones unless the
// instance's constructor injected stand-ins.
func (d *streamJSONDriver) seams() (chatTransportFunc, func() time.Time) {
	open := d.inst.c.open
	if open == nil {
		open = spawnChatTransport
	}
	now := d.inst.c.now
	if now == nil {
		now = time.Now
	}
	return open, now
}

// absorbChatEvent records a session event's native key on res and returns
// answer with an assistant entry's content appended.
func absorbChatEvent(res *engine.TurnResult, answer []string, ev agent.ChatEvent) []string {
	if ev.Session != nil && ev.Session.SessionID != "" {
		res.NativeKey = ev.Session.SessionID
	}
	if ev.Entry != nil && ev.Entry.Type == agent.EntryTypeAssistant {
		answer = append(answer, ev.Entry.Content)
	}
	return answer
}

// relayChatEvent sends ev on out (a nil out relays nothing). cancelled
// reports that ctx ended while waiting to send, with ctx's error.
func relayChatEvent(ctx context.Context, out chan<- engine.Event, ev agent.ChatEvent) (cancelled bool, err error) {
	if out == nil {
		return false, nil
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return false, fmt.Errorf("claude stream-json event: %w", err)
	}
	select {
	case out <- engine.Event{Kind: ev.Kind(), Payload: payload}:
		return false, nil
	case <-ctx.Done():
		return true, ctx.Err()
	}
}

var _ engine.Instance = (*instance)(nil)
