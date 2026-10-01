package mock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// A mock:ask turn makes one tool call its rules leave open and asks about it
// the way claude does in a run whose approver is the human: the call goes
// out on the stream, the engine calls the session endpoint's permission
// host (which holds it), then runs the delivered permission_ask hooks with
// the call as its codec reads it; the first hook that answers decides, and
// with none answering the host's own answer does. Any other approver is
// nobody to ask: the call is denied at once, as --permission-prompts none
// denies it.

// askPattern is the marker: `mock:ask=<tool>:<input JSON, no spaces>`.
var askPattern = regexp.MustCompile(`mock:ask=([^:\s]+):(\S+)`)

// Ask renders the prompt directive that makes the mock's turn ask about a
// call to tool with input.
func Ask(tool, input string) string { return "mock:ask=" + tool + ":" + input }

// AskCallID is the id of the call a mock:ask turn makes.
const AskCallID = "mock-ask-1"

// errAskInput refuses a marker whose input is not JSON.
var errAskInput = errors.New("mock: the mock:ask input is not JSON")

// askIn reads the call a prompt asks the turn to make.
func askIn(prompt string) (string, json.RawMessage, bool, error) {
	m := askPattern.FindStringSubmatch(prompt)
	if m == nil {
		return "", nil, false, nil
	}
	if !json.Valid([]byte(m[2])) {
		return "", nil, true, fmt.Errorf("%w: %s", errAskInput, m[2])
	}
	return m[1], json.RawMessage(m[2]), true, nil
}

// askDecision is the mock codec's answer, whether the hook or the host
// gave it.
type askDecision struct {
	Allow   bool   `json:"allow"`
	Message string `json:"message"`
}

// ask makes the call and has it decided; a denied call is returned as the
// denial the turn reports.
func (d driver) ask(ctx context.Context, ex engine.Exec, hooks wire.UnifiedHooks, send func(agent.ChatEvent) error, tool string, input json.RawMessage) (*agent.PermissionDenial, error) {
	if err := send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: tool, ToolCallID: AskCallID, ToolInput: input}}); err != nil {
		return nil, err
	}
	decision := askDecision{Message: "mock: nobody is asked; " + tool + " is denied"}
	if d.approver == engine.ApproverHuman {
		var err error
		if decision, err = d.askTheHuman(ctx, ex, hooks, tool, input); err != nil {
			return nil, err
		}
	}
	result := agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolCallID: AskCallID, ToolOutput: "mock: " + tool + " ran"}
	if !decision.Allow {
		result.ToolOutput, result.IsError = decision.Message, true
	}
	if err := send(agent.ChatEvent{Entry: &result}); err != nil {
		return nil, err
	}
	if decision.Allow {
		return nil, nil
	}
	denial := agent.PermissionDenial{ToolName: tool, ToolCallID: AskCallID, Reason: decision.Message, Decider: agent.DeciderPolicy}
	return &denial, send(agent.ChatEvent{Denied: &denial})
}

// askTheHuman holds the call on the permission host and runs the
// permission_ask hooks; the hook's answer wins, the host's stands in when no
// hook answers. The host call is abandoned once a hook has decided.
func (d driver) askTheHuman(ctx context.Context, ex engine.Exec, hooks wire.UnifiedHooks, tool string, input json.RawMessage) (askDecision, error) {
	call, err := json.Marshal(mockCall{Tool: tool, Input: input, ToolUseID: AskCallID})
	if err != nil {
		return askDecision{}, err
	}
	hostCtx, abandon := context.WithCancel(ctx)
	defer abandon()
	host := make(chan hostAnswer, 1)
	go func() { host <- callPermissionHost(hostCtx, d.endpoint, call) }()

	// The hook's payload is the call as the codec reads it, minus the id
	// (claude's PermissionRequest carries none, so the runner must anchor it
	// on the held host).
	payload, err := json.Marshal(mockCall{Tool: tool, Input: input})
	if err != nil {
		return askDecision{}, err
	}
	for _, h := range hooks.PermissionAsk {
		if ok, merr := hookMatches(h, tool); merr != nil || !ok {
			if merr != nil {
				return askDecision{}, merr
			}
			continue
		}
		out, rerr := runHook(ctx, h.Command, payload, ex.WorkDir, ex.Env)
		if rerr != nil || len(out) == 0 {
			continue // no decision from this hook
		}
		var dec askDecision
		if json.Unmarshal(out, &dec) == nil {
			return dec, nil
		}
	}
	a := <-host
	if a.err != nil {
		return askDecision{Message: "mock: the permission host failed: " + a.err.Error()}, nil
	}
	var dec askDecision
	if err := json.Unmarshal([]byte(a.text), &dec); err != nil {
		return askDecision{Message: "mock: the permission host's answer is unreadable: " + a.text}, nil
	}
	return dec, nil
}

// hookMatches reports whether h's matcher admits tool (no matcher admits
// every tool).
func hookMatches(h wire.Hook, tool string) (bool, error) {
	if h.Matcher == "" {
		return true, nil
	}
	ok, err := regexp.MatchString("^(?:"+h.Matcher+")$", tool)
	if err != nil {
		return false, fmt.Errorf("mock: hook matcher %q: %w", h.Matcher, err)
	}
	return ok, nil
}

type hostAnswer struct {
	text string
	err  error
}

// callPermissionHost calls the session endpoint's permission host with the
// call, as the engine's MCP client does.
func callPermissionHost(ctx context.Context, ep sessions.Endpoint, call json.RawMessage) hostAnswer {
	if ep.URL == "" {
		return hostAnswer{err: errors.New("the session has no endpoint")}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: string(Name), Version: "0"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: ep.URL, HTTPClient: &http.Client{Transport: bearer{token: ep.Credential}}}
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return hostAnswer{err: err}
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: engine.PermissionHostTool, Arguments: call})
	if err != nil {
		return hostAnswer{err: err}
	}
	i := slices.IndexFunc(res.Content, func(c mcp.Content) bool { _, ok := c.(*mcp.TextContent); return ok })
	if i < 0 {
		return hostAnswer{err: errors.New("the permission host answered no text")}
	}
	return hostAnswer{text: res.Content[i].(*mcp.TextContent).Text}
}

// bearer stamps the session endpoint's bearer on every request.
type bearer struct{ token string }

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(req)
}
