//go:build acceptance

// evaluate_triggers over MCP: does a Deferred task's revive condition actually
// get judged, and does the judgment reach the caller?
//
// The tool asks a model whether each Deferred task's trigger has fired, given
// the repository's recent commits as evidence. Its result carries counters —
// evaluated, cache_hits, cache_misses, omitted — every one of which a run that
// produced NO usable verdict fills in just as convincingly as a working one.
// The result type's own comment names that failure mode: a chunk can come back
// well-formed and simply not mention a task, which is why `omitted` exists as a
// field distinct from an outright chunk failure.
//
// So the assertions here are about the VERDICT: that it is attributed to the
// task that was seeded, and that the model's own reasoning text survives to the
// caller.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
)

// triggerVerdictMarker appears only in the canned verdict's reasoning, so
// finding it in the tool result proves the MODEL's answer travelled back rather
// than a counter having been incremented around an empty response.
const triggerVerdictMarker = "TRIGGER-VERDICT-REACHED-THE-CALLER"

// evalTriggersState remembers the harp taskloom minted for the seeded task.
//
// This is why the canned response cannot live in the feature file: harp ids are
// generated at write time, and triggers.Parse REQUIRES every verdict to carry a
// harp_id that matches a task in the batch. The response has to be built after
// the task exists.
type evalTriggersState struct {
	harpID string
}

func evalTriggersOf(w *World) *evalTriggersState {
	if w.evalTriggers == nil {
		w.evalTriggers = &evalTriggersState{}
	}
	return w.evalTriggers
}

func registerEvaluateTriggersSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a deferred task "([^"]*)" waiting on "([^"]*)"$`, func(c context.Context, text, trigger string) error {
		w := worldFrom(c)
		out, stderr, err := w.env.RunTaskloom(nil, "add", text, "--status", "Deferred", "--trigger", trigger)
		if err != nil {
			return fmt.Errorf("add deferred task: %w: %s", err, stderr)
		}
		harp, perr := parseTaskloomAddHarp(out)
		if perr != nil {
			return fmt.Errorf("add deferred task: %w", perr)
		}
		evalTriggersOf(w).harpID = harp
		return nil
	})

	// Builds the model's answer around the harp minted above and pins it as the
	// mock's response.
	//
	// Deliberately a single, final verdict: a "needs-investigation" answer
	// carries follow-up queries that ctxloom executes before asking again, and
	// a canned response would return the same array in round two — testing the
	// escalation loop against a stub that cannot escalate. That path deserves
	// its own scenario against a fixture that can answer differently the
	// second time; this one asserts the single-round path and says so.
	ctx.Step(`^the trigger model answers "([^"]*)" for that task$`, func(c context.Context, outcome string) error {
		w := worldFrom(c)
		st := evalTriggersOf(w)
		if st.harpID == "" {
			return fmt.Errorf("no deferred task has been seeded, so there is no harp to answer for")
		}
		if w.mock == nil {
			return fmt.Errorf(`no mock LLM configured for this scenario (missing a "the mock LLM responds" step)`)
		}
		verdicts := []map[string]any{{
			"harp_id":   st.harpID,
			"outcome":   outcome,
			"evidence":  []string{"the acceptance fixture's seeded commit"},
			"reasoning": triggerVerdictMarker,
		}}
		body, err := json.Marshal(verdicts)
		if err != nil {
			return fmt.Errorf("encode the canned verdict: %w", err)
		}
		return w.mock.SetResponse(string(body))
	})

	// The attribution claim. A verdict carrying the right outcome but the wrong
	// harp — or a result whose verdict list is empty while `evaluated` says 1 —
	// is exactly the silent drop the tool's own `omitted` counter exists to
	// expose, and neither is visible from the counters alone.
	ctx.Step(`^the verdict is attributed to that task, with outcome "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		st := evalTriggersOf(w)
		if w.lastInnerErr != nil {
			return fmt.Errorf("tool result envelope could not be unwrapped: %v; result:\n%s", w.lastInnerErr, w.lastTool.JSON())
		}
		raw, err := json.Marshal(w.lastInner)
		if err != nil {
			return fmt.Errorf("re-marshal the tool result: %w", err)
		}
		var res struct {
			Verdicts []struct {
				HarpID  string `json:"harp_id"`
				Outcome string `json:"outcome"`
			} `json:"verdicts"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return fmt.Errorf("decode the tool result: %w; result:\n%s", err, raw)
		}
		if len(res.Verdicts) == 0 {
			return fmt.Errorf("the result carries no verdicts at all — the counters were filled in around an answer that never arrived; result:\n%s", raw)
		}
		for _, v := range res.Verdicts {
			if v.HarpID != st.harpID {
				continue
			}
			if v.Outcome != want {
				return fmt.Errorf("task %s came back with outcome %q, want %q", st.harpID, v.Outcome, want)
			}
			return nil
		}
		return fmt.Errorf("no verdict names the seeded task %s — it was dropped between the model's answer and the caller; result:\n%s",
			st.harpID, strings.TrimSpace(string(raw)))
	})
}
