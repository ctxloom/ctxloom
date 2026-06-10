//go:build acceptance

package acceptance

import (
	"context"
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
		w := worldFrom(c)
		if e := w.lastTool.Raw["error"]; e != nil {
			return fmt.Errorf("tool call returned an error: %v", e)
		}
		return nil
	})

	ctx.Step(`^the tool call fails$`, func(c context.Context) error {
		w := worldFrom(c)
		if _, err := w.lastTool.Inner(); err == nil {
			return fmt.Errorf("expected tool call to fail; result:\n%s", w.lastTool.JSON())
		}
		return nil
	})

	ctx.Step(`^the tool result contains "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		if !strings.Contains(w.lastTool.JSON(), want) {
			return fmt.Errorf("tool result does not contain %q; result:\n%s", want, w.lastTool.JSON())
		}
		return nil
	})

	ctx.Step(`^the tool result field "([^"]*)" is set$`, func(c context.Context, path string) error {
		w := worldFrom(c)
		v, ok := lookupField(w.lastInner, path)
		if !ok || v == nil || v == "" {
			return fmt.Errorf("tool result field %q is not set; result:\n%s", path, w.lastTool.JSON())
		}
		return nil
	})

	ctx.Step(`^the tool result field "([^"]*)" equals "([^"]*)"$`, func(c context.Context, path, want string) error {
		w := worldFrom(c)
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
	w.lastInner, _ = res.Inner()
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
