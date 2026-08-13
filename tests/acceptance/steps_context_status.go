//go:build acceptance

// Fixtures and assertions for context_status (features/context_status.feature).
//
// The seeding steps write through internal/contextmetrics' OWN writer rather
// than emitting JSONL by hand. That is the point of them: this suite drives
// the tool across a process boundary (the MCP server is a real `ctxloom mcp
// serve` child), so the only thing tying the fixture to the product is the
// file format — and a hand-rolled fixture would keep passing after the writer
// changed shape, proving the reader still parses a format nothing writes.
// Calling Append resolves that: if the writer's shape moves, the fixture moves
// with it, and only a genuine reader/writer disagreement can fail.
//
// Writing in-process reaches the same file the child reads because
// TestEnvironment.Setup rebinds HOME for the test process too, so
// paths.HarpPersistDir resolves under the isolated home the MCP child inherits.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/contextmetrics"
)

// contextSampleBase is the timestamp the seeded series starts from. Fixed
// rather than time.Now() so a scenario's samples are reproducible and their
// ORDER is a property of the fixture, not of how fast the suite ran.
var contextSampleBase = time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

// seedContextSamples appends one sample per table row (percent | tokens |
// window), a minute apart, oldest first.
//
// Append, not Record: the sampling rule would silently drop a row whose
// percent had not moved far enough, and a fixture that quietly seeds fewer
// samples than it lists is how a trend assertion goes green against the wrong
// series. The rule is exercised in its own unit tests; here the scenario must
// get exactly the series it wrote down.
func seedContextSamples(harp string, table *godog.Table) error {
	if table == nil || len(table.Rows) < 2 {
		return fmt.Errorf("context samples table needs a header row and at least one sample row")
	}
	header := table.Rows[0]
	cols := map[string]int{}
	for i, cell := range header.Cells {
		cols[strings.TrimSpace(cell.Value)] = i
	}
	for _, want := range []string{"percent", "tokens", "window"} {
		if _, ok := cols[want]; !ok {
			return fmt.Errorf("context samples table is missing a %q column", want)
		}
	}

	for i, row := range table.Rows[1:] {
		nums := map[string]float64{}
		for _, name := range []string{"percent", "tokens", "window"} {
			raw := strings.TrimSpace(row.Cells[cols[name]].Value)
			v, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return fmt.Errorf("context samples row %d: %s %q is not a number: %w", i+1, name, raw, err)
			}
			nums[name] = v
		}
		if err := contextmetrics.Append(harp, contextmetrics.Sample{
			TS:         contextSampleBase.Add(time.Duration(i) * time.Minute),
			Harp:       harp,
			ContextPct: nums["percent"],
			TokensUsed: int(nums["tokens"]),
			Window:     int(nums["window"]),
		}); err != nil {
			return fmt.Errorf("seed context sample %d for %s: %w", i+1, harp, err)
		}
	}
	return nil
}

// contextResultTrend pulls the decoded trend array out of the last tool
// result, failing loudly when the envelope never unwrapped rather than
// reporting an empty trend — "the tool returned nothing" and "the tool
// returned a trend of zero samples" are different findings.
func contextResultTrend(w *World) ([]any, error) {
	if w.lastInnerErr != nil {
		return nil, fmt.Errorf("tool result envelope could not be unwrapped: %v; result:\n%s", w.lastInnerErr, w.lastTool.JSON())
	}
	raw, ok := lookupField(w.lastInner, "trend")
	if !ok || raw == nil {
		return nil, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("tool result field \"trend\" is not an array; result:\n%s", w.lastTool.JSON())
	}
	return arr, nil
}

// contextNumber reads a numeric field off a decoded sample object. JSON
// numbers arrive as float64, and formatting them with %v renders 1000000 as
// "1e+06" — so every numeric comparison here goes through the float, never
// through its default string form.
func contextNumber(sample any, field string) (float64, error) {
	obj, ok := sample.(map[string]any)
	if !ok {
		return 0, fmt.Errorf("context sample is not an object: %#v", sample)
	}
	raw, ok := obj[field]
	if !ok {
		return 0, fmt.Errorf("context sample has no %q field: %#v", field, obj)
	}
	num, ok := raw.(float64)
	if !ok {
		return 0, fmt.Errorf("context sample field %q is not a number: %#v", field, raw)
	}
	return num, nil
}

func registerContextStatusSteps(ctx *godog.ScenarioContext) {
	// Seeds the CALLING session's series. The harp is the one the preceding
	// "the session harp is" step handed the MCP child, so this fixture and the
	// tool's own identity resolution have to agree for the scenario to pass.
	ctx.Step(`^the session has recorded context samples:$`, func(c context.Context, table *godog.Table) error {
		w := worldFrom(c)
		harp := w.env.ChildEnv("CTXLOOM_SESSION_HARP")
		if harp == "" {
			return fmt.Errorf("no session harp set — put `the session harp is \"...\"` before this step")
		}
		return seedContextSamples(harp, table)
	})

	// Seeds some OTHER session's series, for the leak scenario.
	ctx.Step(`^the session "([^"]*)" has recorded context samples:$`, func(c context.Context, harp string, table *godog.Table) error {
		return seedContextSamples(harp, table)
	})

	// "No measurement" asserted as the ABSENCE of a sample, not as a flag.
	// available=false beside a zero-valued latest would satisfy a flag check
	// while handing an agent the one number it must never be given: a 0% that
	// means "unknown" and reads as "empty".
	ctx.Step(`^the tool result carries no context measurement$`, func(c context.Context) error {
		w := worldFrom(c)
		if w.lastInnerErr != nil {
			return fmt.Errorf("tool result envelope could not be unwrapped: %v; result:\n%s", w.lastInnerErr, w.lastTool.JSON())
		}
		if v, ok := lookupField(w.lastInner, "latest"); ok && v != nil {
			return fmt.Errorf("tool result carries a latest sample where it should carry none: %#v; result:\n%s", v, w.lastTool.JSON())
		}
		trend, err := contextResultTrend(w)
		if err != nil {
			return err
		}
		if len(trend) != 0 {
			return fmt.Errorf("tool result carries %d trend sample(s) where it should carry none; result:\n%s", len(trend), w.lastTool.JSON())
		}
		return nil
	})

	ctx.Step(`^the latest context sample reads (\d+)% used, (\d+) of (\d+) tokens$`,
		func(c context.Context, wantPct, wantTokens, wantWindow int) error {
			w := worldFrom(c)
			if w.lastInnerErr != nil {
				return fmt.Errorf("tool result envelope could not be unwrapped: %v; result:\n%s", w.lastInnerErr, w.lastTool.JSON())
			}
			latest, ok := lookupField(w.lastInner, "latest")
			if !ok || latest == nil {
				return fmt.Errorf("tool result carries no latest sample; result:\n%s", w.lastTool.JSON())
			}
			for _, check := range []struct {
				field string
				want  int
			}{
				{"context_pct", wantPct},
				{"tokens_used", wantTokens},
				{"window", wantWindow},
			} {
				got, err := contextNumber(latest, check.field)
				if err != nil {
					return err
				}
				if int(got) != check.want {
					return fmt.Errorf("latest sample %s = %v, want %d; result:\n%s", check.field, got, check.want, w.lastTool.JSON())
				}
			}
			return nil
		})

	// The trend as an ORDERED list. A length check alone passes on a reversed
	// series, and the tool reads its "current" reading off the last element —
	// so a reversal reports the session's oldest occupancy as its newest.
	ctx.Step(`^the context trend reads "([^"]*)" oldest first$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		trend, err := contextResultTrend(w)
		if err != nil {
			return err
		}
		var got []string
		for _, sample := range trend {
			pct, nerr := contextNumber(sample, "context_pct")
			if nerr != nil {
				return nerr
			}
			got = append(got, strconv.Itoa(int(pct)))
		}
		if strings.Join(got, ",") != want {
			return fmt.Errorf("context trend reads %q, want %q (oldest first); result:\n%s",
				strings.Join(got, ","), want, w.lastTool.JSON())
		}
		return nil
	})

	// Every sample carries the harp it was recorded under, so another
	// session's series leaking into this answer is visible by name.
	ctx.Step(`^the tool result names no samples from "([^"]*)"$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		if w.lastInnerErr != nil {
			return fmt.Errorf("tool result envelope could not be unwrapped: %v; result:\n%s", w.lastInnerErr, w.lastTool.JSON())
		}
		innerJSON, err := json.Marshal(w.lastInner)
		if err != nil {
			return fmt.Errorf("re-marshal unwrapped tool result: %w", err)
		}
		if strings.Contains(string(innerJSON), harp) {
			return fmt.Errorf("tool result names session %q — another session's samples leaked into this answer:\n%s", harp, innerJSON)
		}
		return nil
	})
}
