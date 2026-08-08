//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
)

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
		// with an error message quoting `want` would pass. Match against the
		// unwrapped inner payload instead (the same one "the tool result
		// field … equals …" already trusts), and fail loud if the envelope
		// could not be unwrapped at all rather than silently falling through
		// to the raw envelope.
		if w.lastInnerErr != nil {
			return fmt.Errorf("tool result envelope could not be unwrapped: %v; result:\n%s", w.lastInnerErr, w.lastTool.JSON())
		}
		innerJSON, err := json.Marshal(w.lastInner)
		if err != nil {
			return fmt.Errorf("re-marshal unwrapped tool result: %w; result:\n%s", err, w.lastTool.JSON())
		}
		if !strings.Contains(string(innerJSON), want) {
			return fmt.Errorf("tool result does not contain %q; unwrapped result:\n%s", want, innerJSON)
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
		text, mime, err := agent.ReadResource(uri)
		if err != nil {
			return err
		}
		w.lastRes, w.lastMime = text, mime
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
	res, err := agent.CallTool(name, args)
	if err != nil {
		return err
	}
	w.lastTool = res
	w.lastInner, w.lastInnerErr = res.Inner()
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
// failure shapes: the CallToolResult's isError flag (the MCP SDK reports
// handler/validation failures as a result with isError=true and a nil
// envelope error, not just a JSON-RPC error), AND whether the
// envelope could be unwrapped at all. Before this, an envelope that failed
// to unwrap (a malformed/error payload Inner() couldn't parse) left
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
// map. Values pass as strings; the server coerces per its schema.
func tableToArgs(table *godog.Table) (map[string]any, error) {
	args := map[string]any{}
	for _, row := range table.Rows {
		if len(row.Cells) != 2 {
			return nil, fmt.Errorf("argument table rows must have exactly 2 cells, got %d", len(row.Cells))
		}
		args[row.Cells[0].Value] = row.Cells[1].Value
	}
	return args, nil
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
