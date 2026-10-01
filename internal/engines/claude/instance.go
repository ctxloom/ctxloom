package claude

import (
	"context"
	"encoding/json"
	"errors"
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

// execArgs is the argv up to the prompt: the label's args, the permission
// posture (headless: permissionArgs; interactive: the human's own session,
// interactivePermissionArgs), the model, the session name (interactive) or
// --print, every presentation's args in delivery order, then the resumed
// native key. It refuses an argv naming --settings twice.
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
	for _, p := range presented {
		args = append(args, p.Args...)
	}
	if i.key != "" {
		args = append(args, flagResume, i.key)
	}
	if countFlag(args, flagSettings) > 1 {
		return nil, errSettingsTwice
	}
	return args, nil
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
// into a turn nobody reads) and no idle abort on an MCP call (the
// permission host holds one while the human decides).
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
	} else {
		env[disableBackgroundTasksEnv] = "1"
		env[mcpToolIdleTimeoutEnv] = "0"
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

// argv is the per-turn process's argv: Exec plus the protocol, and the
// turn's posture as the process's one --settings (turnSettings). It refuses
// an Exec that already names --settings: the turn's would replace it.
func (d *streamJSONDriver) argv(ex engine.Exec, in engine.Turn) ([]string, error) {
	args := slices.Clone(ex.Args)
	args = append(args, flagInputFormat, "stream-json", flagOutputFormat, "stream-json", flagVerbose)
	if in.Resume != "" && !slices.Contains(ex.Args, flagResume) {
		args = append(args, flagResume, in.Resume)
	}
	if harp := d.inst.s.Identity.Harp; harp != "" {
		args = append(args, flagName, harp)
	}
	doc, err := turnSettings(d.inst.pos, d.inst.s.MCPServers, in.Posture)
	if err != nil {
		return nil, err
	}
	if doc != "" {
		if slices.Contains(ex.Args, flagSettings) {
			return nil, errSettingsTwice
		}
		args = append(args, flagSettings, doc)
	}
	return args, nil
}

// Turn spawns one stream-json process, writes the one user message, relays
// every native event and returns the native key the next turn resumes by
// with the assistant's answer. The env is the Exec's laid over the
// process's own (spawnChatTransport merges it onto os.Environ). A nil out
// relays nothing.
//
// ctx ending is an INTERRUPT, not a teardown: the transport asks the process
// to stop and kills it after its grace, while the driver keeps reading, so
// what the process says on its way out (its result, its session) is still
// relayed. The turn then returns ctx's error — it was cut short. A process
// that ends without a result frame and exits in failure died mid-turn
// (errTurnProcessDied).
func (d *streamJSONDriver) Turn(ctx context.Context, ex engine.Exec, in engine.Turn, out chan<- engine.Event) (engine.TurnResult, error) {
	argv, err := d.argv(ex, in)
	if err != nil {
		return engine.TurnResult{}, err
	}
	open, now := d.seams()
	tr, err := open(ctx, ex.Binary, argv, ex.Env, ex.WorkDir)
	if err != nil {
		return engine.TurnResult{}, err
	}
	events := make(chan agent.ChatEvent, 64)
	go func() {
		readChatEvents(tr.stdout, events, now)
		close(events)
	}()
	if err := writeUserMessage(tr.stdin, in.Prompt); err != nil {
		_ = tr.Close()
		for range events {
			// drained, so the reader can return
		}
		return engine.TurnResult{}, err
	}
	_ = tr.stdin.Close()
	return relayTurn(ctx, tr, events, out, turnInterruptGrace)
}

// relayTurn folds and relays the turn's events until the process's stdout
// ends, then classifies how the turn ended. grace is the interrupt's: a
// process whose stdout outlives the interrupt by twice it (a grandchild
// holding stdout open) is torn down, so an interrupted turn always returns.
func relayTurn(ctx context.Context, tr *chatTransport, events <-chan agent.ChatEvent, out chan<- engine.Event, grace time.Duration) (engine.TurnResult, error) {
	finished := make(chan struct{})
	defer close(finished)
	go closeOverdue(ctx, finished, tr, 2*grace)
	relay := turnRelay{ctx: ctx, out: out, grace: grace}
	var res engine.TurnResult
	var acc turnAccumulator
	var relayErr error
	for ev := range events {
		acc.absorb(&res, &ev)
		if err := relay.send(ev); err != nil && relayErr == nil {
			relayErr = err
			_ = tr.Close()
		}
	}
	res.Answer = acc.answer()
	exitErr := tr.Wait()
	res.ExitCode = engineExit(ctx, tr, exitErr)
	switch {
	case relayErr != nil:
		return res, relayErr
	case ctx.Err() != nil:
		return res, ctx.Err()
	case acc.results == 0 && exitErr != nil:
		return res, fmt.Errorf("%w: %w", errTurnProcessDied, exitErr)
	}
	return res, nil
}

// closeOverdue tears tr down when the turn is still unfinished bound after ctx
// ended.
func closeOverdue(ctx context.Context, finished <-chan struct{}, tr *chatTransport, bound time.Duration) {
	select {
	case <-finished:
		return
	case <-ctx.Done():
	}
	select {
	case <-finished:
	case <-time.After(bound):
		_ = tr.Close()
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

// turnAccumulator folds one turn's events, which may span more than one
// result frame (one process can answer more than once): the answer is the
// LAST result's text, while the denials are EVERY result's, each joined with
// the reason its permission_denied frame gave — result.permission_denials
// carries no reason of its own. The runner keeps the last completion, so
// absorb rewrites each completion's Denials to the turn's so far.
type turnAccumulator struct {
	segment  []string // assistant text since the last result
	last     string   // the last result's text
	results  int
	reasons  map[string]string // tool_use_id → permission_denied message
	denials  []agent.PermissionDenial
	seenCall map[string]bool
}

// absorb records ev's native key on res and folds ev into the turn; a
// completion's Denials are rewritten in place before it is relayed.
func (a *turnAccumulator) absorb(res *engine.TurnResult, ev *agent.ChatEvent) {
	switch {
	case ev.Session != nil:
		if ev.Session.SessionID != "" {
			res.NativeKey = ev.Session.SessionID
		}
	case ev.Entry != nil:
		if ev.Entry.Type == agent.EntryTypeAssistant {
			a.segment = append(a.segment, ev.Entry.Content)
		}
	case ev.Denied != nil:
		if a.reasons == nil {
			a.reasons = map[string]string{}
		}
		a.reasons[ev.Denied.ToolCallID] = ev.Denied.Reason
	case ev.Complete != nil:
		a.complete(ev.Complete)
	}
}

// complete closes one result frame: its text becomes the answer so far, and
// its Denials are replaced by the turn's, each first-seen call once.
func (a *turnAccumulator) complete(m *agent.TurnMeta) {
	a.last = strings.Join(a.segment, "")
	a.segment = nil
	a.results++
	for _, d := range m.Denials {
		if a.seenCall[d.ToolCallID] {
			continue
		}
		if a.seenCall == nil {
			a.seenCall = map[string]bool{}
		}
		a.seenCall[d.ToolCallID] = true
		if d.Reason == "" {
			d.Reason = a.reasons[d.ToolCallID]
		}
		a.denials = append(a.denials, d)
	}
	m.Denials = slices.Clone(a.denials)
}

// answer is the turn's answer: the last result's text, or — a process that
// ended with no result — everything it said.
func (a *turnAccumulator) answer() string {
	if a.results == 0 {
		return strings.Join(a.segment, "")
	}
	return a.last
}

// turnRelay sends a turn's events on out (a nil out relays nothing). Before the
// interrupt a send waits for the consumer; after it, the consumer still gets
// the process's last words, but a send waits no longer than the grace in all —
// a consumer that stopped reading must not hold the turn open.
type turnRelay struct {
	ctx      context.Context
	out      chan<- engine.Event
	grace    time.Duration
	deadline <-chan time.Time // armed at the interrupt
	expired  bool
}

// send relays ev; the only error is one ev cannot be encoded.
func (r *turnRelay) send(ev agent.ChatEvent) error {
	if r.out == nil || r.expired {
		return nil
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("claude stream-json event: %w", err)
	}
	e := engine.Event{Kind: ev.Kind(), Payload: payload}
	if r.ctx.Err() == nil {
		select {
		case r.out <- e:
			return nil
		case <-r.ctx.Done():
		}
	}
	if r.deadline == nil {
		r.deadline = time.After(r.grace)
	}
	select {
	case r.out <- e:
	case <-r.deadline:
		r.expired = true
	}
	return nil
}

var _ engine.Instance = (*instance)(nil)
