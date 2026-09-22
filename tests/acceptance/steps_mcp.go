//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// toolOutcome is the assertion-side view of one tools/call: the SDK result
// when the server answered with one, or the JSON-RPC error it answered with
// instead. Exactly one of the two is set once a call has been made; the zero
// value means no call yet. Transport failures never land here — callTool
// fails the step on those, because a server that did not answer at all is a
// harness failure, not an outcome a scenario can assert about.
type toolOutcome struct {
	res    *mcp.CallToolResult
	rpcErr *jsonrpc.Error
}

// Inner unwraps the operation result: the result's structuredContent when
// the server set one (the SDK's typed slot — every typed tool fills it, and
// the coordination tools carry their payload ONLY there, their text content
// being a status line), else the JSON the server embeds in the first text
// content. It returns an error when the call produced a JSON-RPC error or
// the result carries neither.
func (o toolOutcome) Inner() (map[string]any, error) {
	if o.rpcErr != nil {
		return nil, fmt.Errorf("tool error: %v", o.rpcErr)
	}
	if o.res == nil {
		return nil, errors.New("no tool call has been made")
	}
	if o.res.StructuredContent != nil {
		raw, err := json.Marshal(o.res.StructuredContent)
		if err != nil {
			return nil, fmt.Errorf("re-marshal structured content: %w", err)
		}
		var inner map[string]any
		if err := json.Unmarshal(raw, &inner); err != nil {
			return nil, fmt.Errorf("unwrap structured content: %w", err)
		}
		return inner, nil
	}
	text, ok := firstText(o.res)
	if !ok {
		return nil, errors.New("result.content has no leading text content")
	}
	var inner map[string]any
	if err := json.Unmarshal([]byte(text), &inner); err != nil {
		return nil, fmt.Errorf("unwrap tool json: %w", err)
	}
	return inner, nil
}

// IsError reports whether the tool call failed and a short failure message.
// Failure means EITHER a JSON-RPC error answer OR a CallToolResult with
// isError=true — the form the MCP SDK uses for handler errors and input
// validation failures, which never become a JSON-RPC error. The
// "succeeds"/"fails" assertions must consult this, not just rpcErr.
func (o toolOutcome) IsError() (bool, string) {
	if o.rpcErr != nil {
		return true, o.rpcErr.Error()
	}
	if o.res != nil && o.res.IsError {
		text, _ := firstText(o.res)
		return true, text
	}
	return false, ""
}

// JSON renders the outcome for substring assertions and doc capture: the
// SDK result in its wire shape, or the JSON-RPC error object. Empty before
// any call.
func (o toolOutcome) JSON() string {
	switch {
	case o.rpcErr != nil:
		data, _ := json.Marshal(map[string]any{"error": o.rpcErr})
		return string(data)
	case o.res != nil:
		data, _ := json.Marshal(o.res)
		return string(data)
	}
	return ""
}

// Text joins every text content of a successful result — the status line a
// coordination tool answers beside its structured payload, or the whole
// answer of a tool that speaks text. Empty for a JSON-RPC error or no call.
func (o toolOutcome) Text() string {
	if o.res == nil {
		return ""
	}
	var parts []string
	for _, c := range o.res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// firstText returns the text of a result's first content when that content
// is text.
func firstText(res *mcp.CallToolResult) (string, bool) {
	if len(res.Content) == 0 {
		return "", false
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		return "", false
	}
	return tc.Text, true
}

// callCtx bounds one MCP round trip. The parent is deliberately not the
// step's context: the scenario's lifetime is not the call's, and the
// per-call ceiling is what turns a wedged server into a failed step instead
// of a hung suite.
func callCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), testenv.MCPCallTimeout)
}

func registerMCPSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the agent calls tool "([^"]*)"$`, func(c context.Context, name string) error {
		return callTool(c, name, map[string]any{})
	})

	ctx.Step(`^the agent calls tool "([^"]*)" with:$`, func(c context.Context, name string, table *godog.Table) error {
		args, err := tableToArgs(table)
		if err != nil {
			return err
		}
		return callTool(c, name, args)
	})

	// The session owner loads its config ONCE, at start, and the mock
	// engine's reply rides that config's llm.configs.mock.mock_control — so a
	// "the mock LLM responds" step taken AFTER the first tool call changes
	// nothing the standing session can see. Restarting is what makes a
	// re-pointed mock reach it, and it is also the production shape: the
	// session that loads an essence is rarely the one that compacted it. The
	// next tool call stands a fresh owner under the same isolated env.
	ctx.Step(`^the MCP server is restarted$`, func(c context.Context) error {
		w := worldFrom(c)
		if w.mcp == nil {
			return fmt.Errorf("no MCP session is open to restart: a restart only means something after a tool call has opened one")
		}
		if err := w.mcp.Close(); err != nil {
			return fmt.Errorf("close the mcp session for restart: %w", err)
		}
		w.mcp = nil
		w.owner.stop()
		w.owner = nil
		return nil
	})

	ctx.Step(`^the tool call succeeds$`, func(c context.Context) error {
		return assertToolCallSucceeds(worldFrom(c))
	})

	ctx.Step(`^the tool call fails$`, func(c context.Context) error {
		w := worldFrom(c)
		if isErr, _ := w.lastTool.IsError(); !isErr {
			return fmt.Errorf("expected tool call to fail; result:\n%s", w.lastTool.JSON())
		}
		return nil
	})

	// A FAILED tool call's payload is its error message, not a JSON envelope, so
	// "the tool result contains" (which unwraps the inner JSON) can never assert
	// on it — it reports the unwrap failure instead. This step is how a refusal's
	// own wording gets asserted: a refusal that does not tell the caller what to
	// do instead is only half a refusal.
	ctx.Step(`^the tool failure message contains "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		isErr, msg := w.lastTool.IsError()
		if !isErr {
			return fmt.Errorf("expected the tool call to have failed; result:\n%s", w.lastTool.JSON())
		}
		if !strings.Contains(msg, want) {
			return fmt.Errorf("tool failure message does not contain %q; message:\n%s", want, msg)
		}
		return nil
	})

	ctx.Step(`^the tool result contains "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		// This used to substring-match the WHOLE re-marshalled
		// JSON-RPC envelope (w.lastTool.JSON()) -- field names, the isError
		// flag, and any error text included -- so a tool call that FAILED
		// with an error message quoting `want` would pass. Match against
		// what a SUCCESSFUL result says instead: the unwrapped payload (the
		// same one "the tool result field … equals …" already trusts) and
		// the result's own text — a coordination tool answers with its
		// payload as structured content and a status line as text, and a
		// model reads both. A failed call never matches, and an envelope
		// that could not be unwrapped fails loud rather than silently
		// falling through to the raw envelope.
		if isErr, msg := w.lastTool.IsError(); isErr {
			return fmt.Errorf("the tool call failed, so its result cannot contain %q: %s", want, msg)
		}
		if w.lastInnerErr != nil {
			return fmt.Errorf("tool result envelope could not be unwrapped: %v; result:\n%s", w.lastInnerErr, w.lastTool.JSON())
		}
		innerJSON, err := json.Marshal(w.lastInner)
		if err != nil {
			return fmt.Errorf("re-marshal unwrapped tool result: %w; result:\n%s", err, w.lastTool.JSON())
		}
		surface := string(innerJSON) + "\n" + w.lastTool.Text()
		if !strings.Contains(surface, want) {
			return fmt.Errorf("tool result does not contain %q; unwrapped result:\n%s\ntext:\n%s", want, innerJSON, w.lastTool.Text())
		}
		return nil
	})

	// The negative half of "the tool result contains", and it only means
	// anything PAIRED with the positive one: a scenario proves the tool
	// answered X INSTEAD OF Y, never merely that Y is absent. Matches the
	// same unwrapped payload, and fails loud on an envelope that could not be
	// unwrapped rather than finding Y vacuously absent from nothing.
	ctx.Step(`^the tool result does not contain "([^"]*)"$`, func(c context.Context, unwanted string) error {
		w := worldFrom(c)
		if w.lastInnerErr != nil {
			return fmt.Errorf("tool result envelope could not be unwrapped: %v; result:\n%s", w.lastInnerErr, w.lastTool.JSON())
		}
		innerJSON, err := json.Marshal(w.lastInner)
		if err != nil {
			return fmt.Errorf("re-marshal unwrapped tool result: %w; result:\n%s", err, w.lastTool.JSON())
		}
		if strings.Contains(string(innerJSON), unwanted) {
			return fmt.Errorf("tool result contains %q and must not; unwrapped result:\n%s", unwanted, innerJSON)
		}
		return nil
	})

	ctx.Step(`^the tool result field "([^"]*)" is set$`, func(c context.Context, path string) error {
		w := worldFrom(c)
		if w.lastInnerErr != nil {
			return fmt.Errorf("tool result envelope could not be unwrapped: %v; result:\n%s", w.lastInnerErr, w.lastTool.JSON())
		}
		v, ok := lookupField(w.lastInner, path)
		if !ok || v == nil || v == "" {
			return fmt.Errorf("tool result field %q is not set; result:\n%s", path, w.lastTool.JSON())
		}
		return nil
	})

	ctx.Step(`^the tool result field "([^"]*)" equals "([^"]*)"$`, func(c context.Context, path, want string) error {
		w := worldFrom(c)
		if w.lastInnerErr != nil {
			return fmt.Errorf("tool result envelope could not be unwrapped: %v; result:\n%s", w.lastInnerErr, w.lastTool.JSON())
		}
		v, ok := lookupField(w.lastInner, path)
		if !ok {
			return fmt.Errorf("tool result field %q is absent; result:\n%s", path, w.lastTool.JSON())
		}
		if got := fmt.Sprintf("%v", v); got != want {
			return fmt.Errorf("tool result field %q = %q, want %q", path, got, want)
		}
		return nil
	})

	ctx.Step(`^the agent reads resource "([^"]*)"$`, func(c context.Context, uri string) error {
		w := worldFrom(c)
		agent, err := w.agent()
		if err != nil {
			return err
		}
		ctx, cancel := callCtx()
		defer cancel()
		res, err := agent.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			return err
		}
		if len(res.Contents) == 0 {
			return fmt.Errorf("resource %q: result.contents missing or empty", uri)
		}
		w.lastRes, w.lastMime = res.Contents[0].Text, res.Contents[0].MIMEType
		return nil
	})

	ctx.Step(`^the resource contains "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		if !strings.Contains(w.lastRes, want) {
			return fmt.Errorf("resource does not contain %q; content:\n%s", want, w.lastRes)
		}
		return nil
	})

	ctx.Step(`^the resource does not contain "([^"]*)"$`, func(c context.Context, unwant string) error {
		w := worldFrom(c)
		if strings.Contains(w.lastRes, unwant) {
			return fmt.Errorf("resource unexpectedly contains %q; content:\n%s", unwant, w.lastRes)
		}
		return nil
	})

	ctx.Step(`^the resource MIME type is "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		if w.lastMime != want {
			return fmt.Errorf("resource MIME type = %q, want %q", w.lastMime, want)
		}
		return nil
	})
}

func callTool(c context.Context, name string, args map[string]any) error {
	w := worldFrom(c)
	agent, err := w.agent()
	if err != nil {
		return err
	}
	ctx, cancel := callCtx()
	defer cancel()
	res, err := agent.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	var rpcErr *jsonrpc.Error
	switch {
	case err == nil:
		w.lastTool = toolOutcome{res: res}
	case errors.As(err, &rpcErr):
		// The server ANSWERED, with a refusal (unknown tool, malformed
		// params). That is an outcome the "fails" assertions exist to
		// inspect, not a harness failure.
		w.lastTool = toolOutcome{rpcErr: rpcErr}
	default:
		return err
	}
	w.lastInner, w.lastInnerErr = w.lastTool.Inner()
	// An MCP tool call is an invocation like a CLI command, so it advances a
	// counter the doc-capture sidecar reads for the same reason it reads
	// env.RunCount(): a step that INVOKED something owns the result as its
	// evidence, and a later assertion about it inherits that result rather
	// than showing nothing.
	w.toolCalls++
	return nil
}

// assertToolCallSucceeds is "the tool call succeeds"'s body, named so it is
// directly unit-testable without going through godog. Checks two distinct
// failure shapes: the outcome's IsError (a JSON-RPC error answer OR a
// CallToolResult with isError=true — the MCP SDK reports handler/validation
// failures as the latter, never as a JSON-RPC error), AND whether the
// result could be unwrapped at all. Before this, a result that failed to
// unwrap (a malformed/error payload Inner() couldn't parse) left
// w.lastInner nil with no signal, so every subsequent "the tool result
// field X is set" assertion reported the misleading "field is absent"
// instead of naming the real unwrap failure, and "the tool call succeeds"
// itself could pass on a payload that never parsed.
func assertToolCallSucceeds(w *World) error {
	// Real evidence for the @doc capture sidecar (set-and-consume; no-op
	// when capture is off): the actual tool result envelope this call
	// returned, not a restatement of "it succeeded". MCP traffic never
	// touches w.env's CLI stream, so nothing captures this automatically.
	w.docStepMaterialized = w.lastTool.JSON()
	if isErr, msg := w.lastTool.IsError(); isErr {
		return fmt.Errorf("tool call returned an error: %s\nresult:\n%s", msg, w.lastTool.JSON())
	}
	if w.lastInnerErr != nil {
		return fmt.Errorf("tool result envelope could not be unwrapped: %v\nresult:\n%s", w.lastInnerErr, w.lastTool.JSON())
	}
	return nil
}

// tableToArgs turns a two-column Gherkin table (key | value) into a tool-argument
// map. Values pass as strings; the server coerces per its schema. A dotted
// key ("input.prompt") nests, mirroring the dotted path lookupField reads
// results with — the coordination tools take their task input as an object.
func tableToArgs(table *godog.Table) (map[string]any, error) {
	args := map[string]any{}
	for _, row := range table.Rows {
		if len(row.Cells) != 2 {
			return nil, fmt.Errorf("argument table rows must have exactly 2 cells, got %d", len(row.Cells))
		}
		if err := setField(args, row.Cells[0].Value, row.Cells[1].Value); err != nil {
			return nil, err
		}
	}
	return args, nil
}

// setField writes value at a dotted path, creating intermediate objects. A
// path ending in "[]" names a LIST argument: the cell is split on commas
// into a string array, so a table can pass a tool an array of refs.
func setField(obj map[string]any, path string, value any) error {
	if strings.HasSuffix(path, "[]") {
		path = strings.TrimSuffix(path, "[]")
		var items []any
		for _, item := range strings.Split(fmt.Sprint(value), ",") {
			if item = strings.TrimSpace(item); item != "" {
				items = append(items, item)
			}
		}
		value = items
	}
	segs := strings.Split(path, ".")
	cur := obj
	for _, seg := range segs[:len(segs)-1] {
		next, ok := cur[seg]
		if !ok {
			m := map[string]any{}
			cur[seg] = m
			cur = m
			continue
		}
		m, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("argument %q: %q is already a scalar, cannot nest under it", path, seg)
		}
		cur = m
	}
	cur[segs[len(segs)-1]] = value
	return nil
}

// lookupField walks a dotted path (e.g. "result.name") through a decoded JSON
// object.
func lookupField(obj map[string]any, path string) (any, bool) {
	cur := any(obj)
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}
