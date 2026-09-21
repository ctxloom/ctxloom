// Package coordharness is what an adapter's suite needs of the coordinator
// to test its own side against it: a coordinator that spawns nothing (the
// adapter dials or serves it itself), over a state dir and a HOME of the
// test's own. It imports core alone, so an adapter's in-package tests may
// use it.
package coordharness

import (
	"context"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport/spooltest"
)

// OwnerHarp is the coordinating session's harp in these suites.
const OwnerHarp = "coordinator-harp"

// NopSpawner is a coord.Spawner that spawns nothing: every resolve refuses,
// so a test that reaches it has asked the coordinator for a child it never
// meant to start.
type NopSpawner struct{}

func (NopSpawner) Resolve(context.Context, string) (*coord.SpawnPlan, error) {
	return nil, context.Canceled
}
func (NopSpawner) AssignSession(_, _ string) (string, error)           { return "", context.Canceled }
func (NopSpawner) RecordEngineVersion(context.Context, string, string) {}
func (NopSpawner) ResolveLaunch(context.Context, *coord.SpawnPlan, coord.SpawnStart) (coord.Resolved, error) {
	return coord.Resolved{}, context.Canceled
}
func (NopSpawner) Start(context.Context, launch.Launch, sessions.Endpoint) (*coord.EngineSpawn, error) {
	return nil, context.Canceled
}
func (NopSpawner) Adopt(context.Context, coord.RunRecord) (func() error, error) {
	return func() error { return nil }, nil
}
func (NopSpawner) ResumeHistory(context.Context, string) string { return "" }
func (NopSpawner) MarkSessionEnded(string)                      {}

// Sink is the diagnostics sink these suites report through.
func Sink() report.Sink { return strictness.Sink("ctxloom") }

// New builds an unserved coordinator over stateDir that spawns nothing,
// with a HOME of the test's own; Close runs at cleanup.
func New(t *testing.T, stateDir string) *coord.Coordinator {
	t.Helper()
	spooltest.TeeHome(t)
	c, err := coord.New(coord.Options{
		ProjectDir: stateDir,
		StateDir:   stateDir,
		Spawner:    NopSpawner{},
		OwnerHarp:  OwnerHarp,
		Reporter:   Sink(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}
