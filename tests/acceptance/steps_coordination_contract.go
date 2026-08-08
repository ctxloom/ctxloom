//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/cli"
)

// Steps for the coordination-contract feature: what the agent-delegation tools
// ADVERTISE on the surface a real harness actually talks to.
//
// WHAT THIS CAN AND CANNOT SEE — the same honesty constraint j21_delegation.feature
// documents, and the reason these steps do not go through w.agent(). The rest of
// this suite drives a `ctxloom mcp serve` SUBPROCESS, whose agent-delegation
// tools are a deliberately reduced surface with DIFFERENT, hand-written schemas
// (internal/cli/mcp_tools_agents.go) and no output schemas at all. The
// proto-canonical surface — the one `ctxloom run` / `ctxloom acp` give a real
// harness, generated from coordination.proto — is a spawned session's own
// per-cell runner socket, which no external MCP client here can reach.
//
// So these steps enumerate that surface the way the published reference page
// does: the in-memory MCP client round trip against the registered runner tool
// set (internal/cli's NewDocMCPServer, no handler invoked, nothing dialed). It is
// a genuine ListTools response over a real MCP transport, not a Go struct
// capture — and it is the ONLY thing in the repo that can observe a coordination
// tool's advertised RESULT shape.
//
// What it deliberately does NOT claim: that a delivered message's runtime
// payload matches. That needs the runner topology, and the delivery half of
// plane-2 Stage 2a is a separate change; asserting it here would be the
// overclaim this suite has been caught making before.

type contractState struct {
	tools map[string]cli.DocMCPToolContract
	last  cli.DocMCPToolContract
}

func registerCoordinationContractSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the coordination tool surface a harness receives$`, func(c context.Context) error {
		w := worldFrom(c)
		contracts, err := cli.ListDocMCPToolContracts(c)
		if err != nil {
			return fmt.Errorf("enumerate the runner-terminated tool surface: %w", err)
		}
		if len(contracts) == 0 {
			return fmt.Errorf("the runner-terminated tool surface advertises no tools at all")
		}
		st := &contractState{tools: map[string]cli.DocMCPToolContract{}}
		for _, t := range contracts {
			st.tools[t.Name] = t
		}
		w.contract = st
		return nil
	})

	ctx.Step(`^I read the "([^"]*)" tool's (input|result) contract$`, func(c context.Context, tool, side string) error {
		w := worldFrom(c)
		if w.contract == nil {
			return fmt.Errorf("no tool surface enumerated yet")
		}
		t, ok := w.contract.tools[tool]
		if !ok {
			return fmt.Errorf("tool %q is not on the coordination surface; it advertises %s",
				tool, strings.Join(contractToolNames(w.contract), ", "))
		}
		// Fail LOUD on an absent schema rather than letting every following
		// "advertises/does not advertise" assertion match against "" — an empty
		// haystack makes a "does not contain" assertion vacuously green, which is
		// exactly how a contract gate stops gating.
		side = strings.ToLower(side)
		schema := t.InputSchema
		if side == "result" {
			schema = t.OutputSchema
		}
		if strings.TrimSpace(schema) == "" {
			return fmt.Errorf("tool %q advertises NO %s schema, so there is nothing to assert against "+
				"(a change that dropped the schema would otherwise pass every assertion below)", tool, side)
		}
		w.contract.last = cli.DocMCPToolContract{Name: t.Name, Description: t.Description, InputSchema: schema}
		return nil
	})

	ctx.Step(`^I read the "([^"]*)" tool's description$`, func(c context.Context, tool string) error {
		w := worldFrom(c)
		if w.contract == nil {
			return fmt.Errorf("no tool surface enumerated yet")
		}
		t, ok := w.contract.tools[tool]
		if !ok {
			return fmt.Errorf("tool %q is not on the coordination surface; it advertises %s",
				tool, strings.Join(contractToolNames(w.contract), ", "))
		}
		if strings.TrimSpace(t.Description) == "" {
			return fmt.Errorf("tool %q advertises no description at all", tool)
		}
		// The following assertions read the same "last" slot; description text
		// goes in it so one set of steps serves all three surfaces.
		w.contract.last = cli.DocMCPToolContract{Name: t.Name, Description: t.Description, InputSchema: t.Description}
		return nil
	})

	ctx.Step(`^it advertises "([^"]*)"$`, func(c context.Context, want string) error {
		return contractContains(c, want, true)
	})

	ctx.Step(`^it does not advertise "([^"]*)"$`, func(c context.Context, unwant string) error {
		return contractContains(c, unwant, false)
	})

	ctx.Step(`^the kind vocabulary it advertises is exactly:$`, func(c context.Context, table *godog.Table) error {
		w := worldFrom(c)
		if w.contract == nil || w.contract.last.Name == "" {
			return fmt.Errorf("no tool contract read yet")
		}
		var want []string
		for _, row := range table.Rows {
			if len(row.Cells) != 1 {
				return fmt.Errorf("the vocabulary table takes one column, got %d", len(row.Cells))
			}
			want = append(want, row.Cells[0].Value)
		}
		sort.Strings(want)
		got := advertisedKindVocabulary(w.contract.last.InputSchema)
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			return fmt.Errorf("advertised kind vocabulary is\n  %v\nwant\n  %v\n(schema:\n%s)",
				got, want, w.contract.last.InputSchema)
		}
		return nil
	})
}

func contractContains(c context.Context, needle string, want bool) error {
	w := worldFrom(c)
	if w.contract == nil || w.contract.last.Name == "" {
		return fmt.Errorf("no tool contract read yet")
	}
	got := strings.Contains(w.contract.last.InputSchema, needle)
	if got == want {
		return nil
	}
	if want {
		return fmt.Errorf("%s's contract does not advertise %q; schema:\n%s",
			w.contract.last.Name, needle, w.contract.last.InputSchema)
	}
	return fmt.Errorf("%s's contract still advertises %q, which this change retires; schema:\n%s",
		w.contract.last.Name, needle, w.contract.last.InputSchema)
}

func contractToolNames(st *contractState) []string {
	names := make([]string, 0, len(st.tools))
	for n := range st.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// advertisedKindVocabulary pulls the MESSAGE_KIND_* names out of a schema's raw
// JSON. Scanning for the token prefix rather than walking the JSON tree keeps
// the step indifferent to WHERE the enum sits (a top-level property on
// agent_send, a nested messages[].items property on agent_recv) — the claim is
// about the vocabulary being closed and complete, not about nesting.
func advertisedKindVocabulary(schema string) []string {
	seen := map[string]bool{}
	const prefix = "MESSAGE_KIND_"
	for i := 0; i+len(prefix) <= len(schema); i++ {
		if schema[i:i+len(prefix)] != prefix {
			continue
		}
		j := i + len(prefix)
		for j < len(schema) && (schema[j] == '_' || (schema[j] >= 'A' && schema[j] <= 'Z')) {
			j++
		}
		seen[schema[i:j]] = true
		i = j - 1
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}
