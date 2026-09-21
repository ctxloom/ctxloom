package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"runtime"
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

// Home: CLAUDE_CONFIG_DIR relocates config AND credentials, so a session
// home is seeded with .credentials.json and its own .claude.json — the
// latter through claudeInstanceConfig, which carries the account identity
// and the onboarding answers across by name and nothing else (the host's
// own mcpServers registrations and history never cross).
//
// THE SEED IS A PROJECTION, and the projection is claude's own precedent:
// claude's session-seeding path (the temp config dir it makes for a
// resumed SDK session, read from the 2.1.278 bundle) copies the credential
// with claudeAiOauth.refreshToken stripped. The refresh token is
// SINGLE-USE and rotating — whichever holder refreshes consumes the grant —
// so a copy that could refresh would revoke the user's own login the
// first time it did. The instance runs on the access token alone, and the
// host's refreshes reach it through the one accepted delivery: replication,
// which re-copies (and re-projects) the host file on every change. A mount
// is not accepted: it shares by identity and cannot project, so it would
// hand the instance the very field the seed withholds.
func (c Claude) Home() engine.HomeSpec {
	return engine.HomeSpec{
		Vars: []engine.HomeVar{{Name: ConfigDirEnv, Subdir: HomeLeaf}},
		Credentials: engine.Provide(engine.CredentialSeed{
			Subdir: HomeLeaf,
			// CLAUDE_CODE_OAUTH_TOKEN is an access token in the env: it
			// outranks every credential store and needs no file (2.1.278),
			// so it is the first bypass a refusal names.
			EnvTriggers: []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"},
			LoginHint:   "claude login",
			Files: []engine.SeedFile{{
				HostRelHome: credentialRelHome(),
				DestName:    CredentialsFileName,
				Required:    true,
				Project:     projectCredential,
			}},
			Accept: []engine.MaterialDelivery{engine.MaterialDeliveryReplicated},
		}),
		InstanceConfig: claudeInstanceConfig{},
	}
}

// credentialRelHome is the host credential file, relative to the real home.
func credentialRelHome() string {
	return filepath.ToSlash(filepath.Join(ConfigDirName, CredentialsFileName))
}

// Container: no official image (ghcr.io/anthropics/claude-code appears in
// docs but does not resolve publicly), so the composed install fragment,
// which fetches the most recent claude, is the build source.
func (c Claude) Container() (engine.ContainerSpec, error) {
	rel := credentialRelHome()
	return engine.ContainerSpec{
		Install:         installFragment,
		ValidateCommand: "claude --version",
		Auth: engine.Provide(engine.ContainerAuth{
			// ANTHROPIC_AUTH_TOKEN is a trigger too: a gateway host
			// authenticates with AUTH_TOKEN+BASE_URL and carries no API key.
			EnvTriggers: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
			EnvPassthrough: []string{
				"ANTHROPIC_API_KEY",
				"ANTHROPIC_AUTH_TOKEN",
				"ANTHROPIC_BASE_URL",
				"ANTHROPIC_MODEL",
				"ANTHROPIC_SMALL_FAST_MODEL",
			},
			// READ-WRITE, and the REAL host file: the refresh token is
			// single-use and rotating (see Home), so any COPY that refreshes
			// invalidates the host's own login. Mounting the one real file
			// keeps host and container on the same rotating token.
			CredentialFiles: []engine.CredentialFile{{HostRelHome: rel, ContainerRelHome: rel}},
			Hint:            containerAuthHint(),
		}),
		OverlayDirs:        []string{ConfigDirName},
		TranscriptStoreRel: filepath.Join(ConfigDirName, TranscriptsDirName),
	}, nil
}

// containerAuthHint is platform-aware because the fallback credential path
// differs by OS. On darwin, a subscription login keeps its OAuth token in the
// macOS Keychain, NOT ~/.claude/.credentials.json — naming that file there is
// unfollowable advice, so the darwin hint names the env var instead.
func containerAuthHint() string {
	if runtime.GOOS == "darwin" {
		return "no ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN to authenticate the in-container engine (a macOS Keychain-held subscription login cannot be mounted — set ANTHROPIC_API_KEY for a containerized run on Mac)"
	}
	return "no ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN and no ~/.claude credentials to authenticate the in-container engine"
}

// Transcripts are the readers the constructor was handed (WithTranscripts):
// the conversion of claude's own store is a transcript adapter, composed
// beside this kind at the root, never imported by it.
func (c Claude) Transcripts() []engine.TranscriptReader { return c.transcripts }

// Hooks is claude's hook codec.
func (c Claude) Hooks() engine.HookCodec { return hookCodec{} }

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
	env := map[string]string{}
	for _, h := range i.s.Home {
		env[h.Var] = h.Path
	}
	for _, p := range presented {
		maps.Copy(env, p.Env)
	}
	binary := i.s.Label.Binary
	if binary == "" {
		binary = "claude"
	}
	ex := engine.Exec{Binary: binary, Args: args, Env: env, WorkDir: i.s.WorkDir, Interactive: interactive}
	if interactive {
		if i.s.Prompt != "" {
			ex.Args = append(ex.Args, "--", i.s.Prompt)
		}
	} else if i.s.Prompt != "" {
		ex.StdinPrompt = []byte(i.s.Prompt)
	}
	return ex, nil
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
// process's own (spawnChatTransport merges it onto os.Environ).
func (d *streamJSONDriver) Turn(ctx context.Context, ex engine.Exec, in engine.Turn, out chan<- engine.Event) (engine.TurnResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b := &ClaudeCode{}
	b.BaseBackend = agent.NewBaseBackend(EngineName, "")
	b.BinaryPath = ex.Binary
	tr, err := b.spawnChatTransport(ctx, d.argv(ex, in), ex.Env, ex.WorkDir)
	if err != nil {
		return engine.TurnResult{}, err
	}
	events := make(chan agent.ChatEvent, 64)
	readerDone := make(chan struct{})
	go func() {
		readChatEvents(ctx, tr.stdout, events, readerDone, time.Now)
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
			if ev.Session != nil && ev.Session.SessionID != "" {
				res.NativeKey = ev.Session.SessionID
			}
			if ev.Entry != nil && ev.Entry.Type == agent.EntryTypeAssistant {
				answer = append(answer, ev.Entry.Content)
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				return res, fmt.Errorf("claude stream-json event: %w", err)
			}
			select {
			case out <- engine.Event{Kind: eventKind(ev), Payload: payload}:
			case <-ctx.Done():
				_ = tr.Close()
				<-readerDone
				return res, ctx.Err()
			}
		}
	}
}

// eventKind names the port-level kind of a native event.
func eventKind(ev agent.ChatEvent) string {
	switch {
	case ev.Session != nil:
		return "session"
	case ev.Complete != nil:
		return "complete"
	case ev.Entry != nil:
		return string(ev.Entry.Type)
	default:
		return "event"
	}
}

var _ engine.Instance = (*instance)(nil)
