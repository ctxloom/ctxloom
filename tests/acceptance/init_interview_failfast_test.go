//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// TestFreshInitInterviewFailsFastOnAnEngineMenu reproduces the host-PATH
// leak that once spent the suite's shared deadline: a second engine on init's
// PATH makes the REAL `ctxloom init` show its engine menu where the interview
// expects the personal-repositories question. The interview must fail at once
// and name the menu, not wait for a question that never comes. Outside the
// suite eventBudget is unbounded, so a wait that does not fail fast hangs
// this test until go test's own timeout.
func TestFreshInitInterviewFailsFastOnAnEngineMenu(t *testing.T) {
	env, err := newScenarioEnv(false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Cleanup() })
	w := &World{env: env}
	if err := installFreshInitEngineStub(w); err != nil {
		t.Fatal(err)
	}

	path := twoEnginePath(t, w)

	start := time.Now()
	out, err := runFreshInitInterview(env, path)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("the interview passed with two engines on PATH; output:\n%s", out)
	}
	for _, want := range []string{initEngineMenu.Name, initEngineMenu.Signature, "personal ctxloom repositories"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not say %q:\n%v", want, err)
		}
	}
	t.Logf("failed in %s: %v", elapsed.Round(time.Millisecond), err)
}

// twoEnginePath is init's PATH with the claude stub, an opencode stub beside
// it, and no other engine from the host: init sees exactly two engines.
func twoEnginePath(t *testing.T, w *World) string {
	t.Helper()
	reg := engines.Registry()
	second := operations.EngineBinary(reg, "opencode")
	if second == "" {
		t.Fatal("no opencode engine binary registered to stand in as the second engine")
	}
	if err := os.WriteFile(filepath.Join(freshInitStubDir(w), second), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, name := range operations.EngineNames(reg) {
		if bin := operations.EngineBinary(reg, name); bin != "" {
			all = append(all, bin)
		}
	}
	scratch := filepath.Join(w.env.Root, "engine-path-mirrors")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	rest, err := testenv.PathHiding(os.Getenv("PATH"), scratch, all...)
	if err != nil {
		t.Fatal(err)
	}

	return freshInitStubDir(w) + string(os.PathListSeparator) + rest
}
