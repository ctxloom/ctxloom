package mock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// The mock IMPLEMENTS AND USES hooks: a hook delivered through its hooks
// surface fires when the event it names happens in a turn, exactly as a
// vendor engine's would. Each turn is one engine process, so a turn fires
// session_start first, turn_start for the prompt, the tool's events around
// a tool call (pre_shell for a shell tool, pre_tool, then post_tool and
// post_file_edit for an editing tool), turn_end, and session_end last. The turn reads the hook file its
// argv names (--hooks, announced by the hooks approach), runs every command
// hook registered for the event whose matcher admits the tool, and writes
// the mock's own payload to the hook's stdin. Hooks() decodes that payload.
// A turn with no hooks delivered spawns nothing.

// The unified events the mock can fire, in the order a turn fires them.
// The lossy double drops session_start and session_end (WithoutHookEvents).
var hookEvents = []string{"session_start", "turn_start", "pre_shell", "pre_tool", "post_tool", "post_file_edit", "turn_end", "session_end"}

// shellTools and editTools are the tools that narrow pre_tool to pre_shell
// and post_tool to post_file_edit.
var (
	shellTools = map[string]bool{"Bash": true, "PowerShell": true}
	editTools  = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}
)

// eventsOfTurn lists the events one turn fires, in order.
func eventsOfTurn(tool string, hasTool bool) []string {
	out := []string{"session_start", "turn_start"}
	if hasTool {
		if shellTools[tool] {
			out = append(out, "pre_shell")
		}
		out = append(out, "pre_tool", "post_tool")
		if editTools[tool] {
			out = append(out, "post_file_edit")
		}
	}
	return append(out, "turn_end", "session_end")
}

// toolCallPattern is the mock's control grammar for a tool call: a prompt
// carrying `mock:tool=<name>` runs the named tool for that turn.
var toolCallPattern = regexp.MustCompile(`mock:tool=(\S+)`)

// ToolCall renders the prompt directive that makes the mock's turn run tool.
func ToolCall(tool string) string { return "mock:tool=" + tool }

// denyPattern is the mock's engine-policy denial: a prompt carrying
// `mock:deny=<tool>` attempts the named tool and has it refused.
var denyPattern = regexp.MustCompile(`mock:deny=(\S+)`)

// Deny renders the prompt directive that makes the mock's turn attempt tool
// and have it denied by policy.
func Deny(tool string) string { return "mock:deny=" + tool }

// deniedToolIn reads the tool a prompt asks the turn to have denied.
func deniedToolIn(prompt string) (string, bool) {
	m := denyPattern.FindStringSubmatch(prompt)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// toolCallIn reads the tool a prompt asks the turn to run.
func toolCallIn(prompt string) (string, bool) {
	m := toolCallPattern.FindStringSubmatch(prompt)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// sessionKey is the mock's native session key: one value, since the mock
// keeps no session state to key.
const sessionKey = "mock-session"

// hookPayload is the JSON the mock writes to a hook's stdin.
type hookPayload struct {
	Event     string `json:"hook_event_name"`
	SessionID string `json:"session_id"`
	ToolName  string `json:"tool_name,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	// Prompt is the submitted prompt on turn_start, under the field name the
	// turn-start hooks read (the mail-drain hook redeems a wake from it).
	Prompt string `json:"prompt,omitempty"`
}

// hookCodec decodes the mock's own payload.
type hookCodec struct{ name engine.Name }

func (c hookCodec) Decode(event string, payload []byte) (engine.HookEvent, error) {
	var p hookPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return engine.HookEvent{}, fmt.Errorf("%s hook payload: %w", c.name, err)
	}
	if p.Event == "" {
		p.Event = event
	}
	return engine.HookEvent{Event: p.Event, NativeSession: p.SessionID}, nil
}

// hooksFileOf reads the hook file the exec's argv names; "" when the turn
// was composed without a hooks surface.
func hooksFileOf(ex engine.Exec) string {
	for i, a := range ex.Args {
		if a == HooksFlag && i+1 < len(ex.Args) {
			return ex.Args[i+1]
		}
	}
	return ""
}

// deliveredHooks reads the hook file the exec's argv names; the zero set
// when the turn was composed without a hooks surface.
func deliveredHooks(ex engine.Exec) (wire.UnifiedHooks, error) {
	file := hooksFileOf(ex)
	if file == "" {
		return wire.UnifiedHooks{}, nil
	}
	return DeliveredHooksFile(file)
}

// DeliveredHooksFile decodes a hook file the mock's hooks surface wrote: the
// unified set as JSON, one key per event.
func DeliveredHooksFile(file string) (wire.UnifiedHooks, error) {
	var hooks wire.UnifiedHooks
	raw, err := os.ReadFile(file)
	if err != nil {
		return hooks, fmt.Errorf("mock: read the delivered hook file: %w", err)
	}
	if err := json.Unmarshal(raw, &hooks); err != nil {
		return hooks, fmt.Errorf("mock: decode the delivered hook file %s: %w", file, err)
	}
	return hooks, nil
}

// registered is the hook file's slice for one unified event: the mock's
// native event IS the unified one, so the file's own key names it.
func registered(hooks wire.UnifiedHooks, event string) []wire.Hook {
	switch event {
	case "session_start":
		return hooks.SessionStart
	case "pre_shell":
		return hooks.PreShell
	case "pre_tool":
		return hooks.PreTool
	case "post_tool":
		return hooks.PostTool
	case "post_file_edit":
		return hooks.PostFileEdit
	case "turn_end":
		return hooks.TurnEnd
	case "session_end":
		return hooks.SessionEnd
	case "turn_start":
		return hooks.TurnStart
	}
	return nil
}

// fireHooks runs every command hook the delivered set registers for event
// whose matcher admits tool, with the mock's payload on stdin, in the turn's
// working directory and environment.
func fireHooks(ctx context.Context, ex engine.Exec, hooks wire.UnifiedHooks, event, tool string) error {
	return FireHooks(ctx, hooks, event, tool, "", ex.WorkDir, ex.Env)
}

// FireHooks is fireHooks for a caller that is not a hosted turn — the mock
// binary's interactive loop, which fires turn_start for each line it reads
// in its own working directory. The payload names the mock's one session,
// and carries prompt (the submitted line on turn_start; "" otherwise).
func FireHooks(ctx context.Context, hooks wire.UnifiedHooks, event, tool, prompt, workDir string, env map[string]string) error {
	payload, err := json.Marshal(hookPayload{Event: event, SessionID: sessionKey, ToolName: tool, Cwd: workDir, Prompt: prompt})
	if err != nil {
		return err
	}
	var errs []error
	for _, h := range registered(hooks, event) {
		if h.Type != "" && h.Type != "command" {
			continue
		}
		if h.Matcher != "" {
			ok, err := regexp.MatchString(h.Matcher, tool)
			if err != nil {
				return fmt.Errorf("mock: hook matcher %q: %w", h.Matcher, err)
			}
			if !ok {
				continue
			}
		}
		if err := runHook(ctx, h.Command, payload, workDir, env); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// runHook execs one command hook through the shell with the payload on
// stdin, in the given working directory with env laid over the process's.
func runHook(ctx context.Context, command string, payload []byte, workDir string, env map[string]string) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mock: hook %q: %w: %s", command, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
