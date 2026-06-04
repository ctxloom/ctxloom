//go:build acceptance

// Package acceptance is the full-stack godog suite. Each scenario asserts a
// ctxloom state change across three axes at once — on-disk files, CLI stdout/
// exit, and mock-agent MCP traffic — over the shared testenv harness.
package acceptance

import (
	"context"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// World is the per-scenario state binding all three evaluation axes. It is
// constructed fresh in a Before hook and torn down in After, so scenarios share
// nothing.
type World struct {
	env  *testenv.TestEnvironment // isolated home+project, CLI exec, file asserts
	mock *testenv.MockLM          // deterministic LLM backend (set by fixtures)
	mcp  *testenv.MCPClient       // mock agent: JSON-RPC stdio client (lazy)

	lastTool     testenv.ToolResult // last tools/call envelope
	lastInner    map[string]any     // unwrapped inner result of lastTool
	lastRes      string             // last resources/read text
	lastMime     string             // last resources/read MIME type
	lastTaskHarp string             // harp_id of the most recent task tool result
}

type worldKey struct{}

// worldFrom retrieves the per-scenario World from the step context.
func worldFrom(ctx context.Context) *World {
	w, _ := ctx.Value(worldKey{}).(*World)
	return w
}

// agent returns the mock-agent MCP client, starting and initializing it on first
// use so scenarios that never touch the agent pay nothing.
func (w *World) agent() (*testenv.MCPClient, error) {
	if w.mcp != nil {
		return w.mcp, nil
	}
	c, err := w.env.StartMCP()
	if err != nil {
		return nil, err
	}
	if err := c.Initialize(); err != nil {
		_ = c.Close()
		return nil, err
	}
	w.mcp = c
	return c, nil
}

// InitializeScenario wires the lifecycle hooks and registers every step. godog
// calls this once per scenario.
func InitializeScenario(ctx *godog.ScenarioContext) {
	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		env, err := testenv.NewTestEnvironment()
		if err != nil {
			return ctx, err
		}
		if err := env.Setup(); err != nil {
			return ctx, err
		}
		w := &World{env: env}
		return context.WithValue(ctx, worldKey{}, w), nil
	})

	ctx.After(func(ctx context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		w := worldFrom(ctx)
		if w == nil {
			return ctx, nil
		}
		_ = w.mcp.Close()
		_ = w.env.Cleanup()
		return ctx, nil
	})

	registerFixtureSteps(ctx)
	registerCLISteps(ctx)
	registerFileSteps(ctx)
	registerMCPSteps(ctx)
	registerLiveSteps(ctx)
}
