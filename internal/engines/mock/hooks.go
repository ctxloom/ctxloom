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
// surface fires when a turn runs a tool, exactly as a vendor engine's would.
// The turn reads the hook file its argv names (--hooks, announced by the
// hooks approach), runs every command hook registered for the event whose
// matcher admits the tool, and writes the mock's own payload to the hook's
// stdin. Hooks() decodes that payload. A turn that runs no tool spawns
// nothing.

// toolCallPattern is the mock's control grammar for a tool call: a prompt
// carrying `mock:tool=<name>` runs the named tool for that turn.
var toolCallPattern = regexp.MustCompile(`mock:tool=(\S+)`)

// ToolCall renders the prompt directive that makes the mock's turn run tool.
func ToolCall(tool string) string { return "mock:tool=" + tool }

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
		if a == hooksFlag && i+1 < len(ex.Args) {
			return ex.Args[i+1]
		}
	}
	return ""
}

// fireHooks runs every pre_tool command hook the delivered hook file
// registers for tool. The mock's unified event is the file's own key: the
// mock translates nothing, so the payload names the unified event.
func fireHooks(ctx context.Context, ex engine.Exec, event, tool string) error {
	file := hooksFileOf(ex)
	if file == "" {
		return nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("mock: read the delivered hook file: %w", err)
	}
	var hooks wire.UnifiedHooks
	if err := json.Unmarshal(raw, &hooks); err != nil {
		return fmt.Errorf("mock: decode the delivered hook file %s: %w", file, err)
	}
	payload, err := json.Marshal(hookPayload{Event: event, SessionID: sessionKey, ToolName: tool, Cwd: ex.WorkDir})
	if err != nil {
		return err
	}
	var errs []error
	for _, h := range hooks.PreTool {
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
		if err := runHook(ctx, ex, h.Command, payload); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// runHook execs one command hook through the shell with the payload on
// stdin, in the turn's working directory and environment.
func runHook(ctx context.Context, ex engine.Exec, command string, payload []byte) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Dir = ex.WorkDir
	cmd.Env = os.Environ()
	for k, v := range ex.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mock: hook %q: %w: %s", command, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
