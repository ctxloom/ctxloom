package isolation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// attachRuntime is a fakeRuntime whose RunArgs renders a real (if minimal) argv,
// so RunAttached execs the stub script in Binary. RemoveArgs stays nil and the
// specs below carry no Name, so teardown never shells out to a remove.
type attachRuntime struct{ fakeRuntime }

func (attachRuntime) RunArgs(RunSpec) []string { return []string{"run"} }

// attachScript writes a stub "container runtime" that behaves like an engine
// adapter ending a conversation: it reads stdin to EOF, then spends `flushFor`
// finishing its own asynchronous work before exiting cleanly.
func attachScript(t *testing.T, flushFor string, marker string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-attached-runtime")
	body := fmt.Sprintf("#!/bin/sh\ncat >/dev/null\nsleep %s\nprintf flushed > %q\n", flushFor, marker)
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	return script
}

// TestRunAttached_CloseWaitsForTheContainerToFinishFlushing is the container
// half of the teardown-grace contract the host transport already honors: an
// engine that flushes its native transcript after the conversation ends must be
// allowed to finish before the container is destroyed. Removing the container
// takes the filesystem the half-written transcript lives on with it, so this
// loss is unrecoverable rather than merely truncated.
func TestRunAttached_CloseWaitsForTheContainerToFinishFlushing(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "flushed")
	script := attachScript(t, "0.2", marker)

	rt := attachRuntime{fakeRuntime{name: "docker", binary: script, available: true}}
	ac, err := RunAttached(context.Background(), rt, RunSpec{}, nil)
	require.NoError(t, err)
	ac.ShutdownGrace = 5 * time.Second // comfortably longer than the stub's flush

	require.NoError(t, ac.Close())

	data, err := os.ReadFile(marker)
	require.NoError(t, err, "the container's post-stdin-EOF flush must complete before teardown destroys it")
	assert.Equal(t, "flushed", string(data))
}

// TestRunAttached_CloseIsBoundedWhenTheContainerIgnoresEOF proves the other half:
// the grace is a bound, not a wait. A container that never reacts to stdin
// closing is still force-removed.
func TestRunAttached_CloseIsBoundedWhenTheContainerIgnoresEOF(t *testing.T) {
	script := attachScript(t, "100", filepath.Join(t.TempDir(), "never"))

	rt := attachRuntime{fakeRuntime{name: "docker", binary: script, available: true}}
	ac, err := RunAttached(context.Background(), rt, RunSpec{}, nil)
	require.NoError(t, err)
	ac.ShutdownGrace = 50 * time.Millisecond

	done := make(chan struct{})
	go func() {
		_ = ac.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close must be bounded by the grace period, not wait on a container that ignores stdin EOF")
	}
}

// TestRunAttached_RuntimeThatLaunchesNoContainer pins a regression.
// interactiveRunArgs indexed args[0] under a comment claiming "Args always start
// with 'run' (every Runtime.RunArgs implementation here does)". That invariant is
// FALSE: Host.RunArgs returns nil — Host launches no container at all — and so
// does any Runtime stub. RunAttached is exported and takes a Runtime, so an empty
// argv reached the index and panicked with index-out-of-range instead of telling
// the caller its runtime cannot start a container.
func TestRunAttached_RuntimeThatLaunchesNoContainer(t *testing.T) {
	assert.Empty(t, interactiveRunArgs(nil), "an empty argv has no insertion point; never index into it")

	require.NotPanics(t, func() {
		_, err := RunAttached(context.Background(), Host{}, RunSpec{}, nil)
		require.Error(t, err, "a runtime that renders no run argv must be refused, not indexed into")
		assert.Contains(t, err.Error(), "cannot start a container")
	})
}

// TestInteractiveRunArgs_InsertsAfterRun pins the positive shape the fix must not
// disturb: `-i` lands immediately after the leading "run" verb, keeping stdin
// open for a caller speaking its own stdio protocol, with every other argument in
// its original order.
func TestInteractiveRunArgs_InsertsAfterRun(t *testing.T) {
	assert.Equal(t,
		[]string{"run", "-i", "--rm", "--name", "c", "img"},
		interactiveRunArgs([]string{"run", "--rm", "--name", "c", "img"}))
	assert.Equal(t, []string{"run", "-i"}, interactiveRunArgs([]string{"run"}))
}

// attachExitScript writes a stub "container runtime" that reads stdin to EOF and
// then exits with the given status — an engine that ends its own conversation
// badly, as opposed to one teardown had to destroy.
func attachExitScript(t *testing.T, status int) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-exiting-runtime")
	body := fmt.Sprintf("#!/bin/sh\ncat >/dev/null\nexit %d\n", status)
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	return script
}

// TestRunAttached_CloseIsCleanWhenTeardownForcedIt pins a regression.
// Close's whole destructive tail — force-remove the container, kill the `run`
// client — is teardown doing its job on a container that did not take the hint
// from stdin EOF. cmd.Wait then reports "signal: killed", which Close returned
// verbatim, so the caller could not tell an ordinary bounded teardown from a
// real failure and every such shutdown looked broken. An error we caused is not
// an error to report.
func TestRunAttached_CloseIsCleanWhenTeardownForcedIt(t *testing.T) {
	script := attachScript(t, "100", filepath.Join(t.TempDir(), "never"))

	rt := attachRuntime{fakeRuntime{name: "docker", binary: script, available: true}}
	ac, err := RunAttached(context.Background(), rt, RunSpec{}, nil)
	require.NoError(t, err)
	ac.ShutdownGrace = 50 * time.Millisecond

	assert.NoError(t, ac.Close(),
		"a container teardown force-killed is a normal shutdown, not a failure the caller must react to")
}

// TestRunAttached_CloseReportsAContainerThatDiedOnItsOwn is the other half: the
// suppression above must be scoped to the kill WE performed. A container that
// exits badly by itself, inside the grace window, is a real failure and its
// status must still reach the caller — that exit code is often the only evidence
// of why an engine adapter died.
func TestRunAttached_CloseReportsAContainerThatDiedOnItsOwn(t *testing.T) {
	rt := attachRuntime{fakeRuntime{name: "docker", binary: attachExitScript(t, 3), available: true}}
	ac, err := RunAttached(context.Background(), rt, RunSpec{}, nil)
	require.NoError(t, err)
	ac.ShutdownGrace = 5 * time.Second

	err = ac.Close()
	require.Error(t, err, "a container that exited nonzero on its own is a real failure")
	assert.Contains(t, err.Error(), "exit status 3")
}

// attachEnvRuntime renders the spec's env onto the run argv the way a real OCI
// runtime does (renderRunSpec's `-e <entry>` loop), so a test can see exactly
// what a bystander reading /proc/<pid>/cmdline would see.
type attachEnvRuntime struct{ fakeRuntime }

func (attachEnvRuntime) RunArgs(spec RunSpec) []string {
	args := []string{"run"}
	for _, e := range spec.Env {
		args = append(args, "-e", e)
	}
	return args
}

// attachRecordingScript writes a stub runtime that records its own argv and
// environment before behaving like a container that ends on stdin EOF.
func attachRecordingScript(t *testing.T, argvFile, envFile string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "recording-runtime")
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\nenv > %q\ncat >/dev/null\n", argvFile, envFile)
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	return script
}

// TestRunAttached_SpawnEnvKeepsValuesOffTheArgv covers two regressions
// together, because they are two halves of one property.
//
// The first: RunAttached had no SpawnEnv-equivalent channel, so a caller with a
// KEY→VALUE env to deliver had only spec.Env — which renders `-e KEY=VAL` into a
// `run` argv that stays world-readable via /proc/<pid>/cmdline for the whole life
// of the container. That is precisely the exposure the rest of this package
// avoids (containerAuth.envPassthrough's bare-name form, and LaunchSpec.SpawnEnv
// on the go-plugin path). The value must reach the container without the argv
// ever holding it.
//
// The second: the bare-name `-e NAME` form only works because the `run` process
// INHERITS this process's environment — the value is read from there. That was an
// unstated dependency on cmd.Env being left nil; nothing asserted it, so setting
// cmd.Env for any reason would have silently dropped the credential a container
// was launched to use. It is explicit now, and pinned here.
func TestRunAttached_SpawnEnvKeepsValuesOffTheArgv(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	envFile := filepath.Join(dir, "env")
	script := attachRecordingScript(t, argvFile, envFile)

	// A bare NAME in the spec resolves its value from THIS process's env.
	t.Setenv("U062_HOST_PASSTHROUGH", "from-the-launcher")

	rt := attachEnvRuntime{fakeRuntime{name: "docker", binary: script, available: true}}
	ac, err := RunAttached(context.Background(), rt,
		RunSpec{Env: []string{"U062_HOST_PASSTHROUGH"}},
		map[string]string{"U062_SPAWN_SECRET": "s3cr3t-value"})
	require.NoError(t, err)
	ac.ShutdownGrace = 5 * time.Second
	require.NoError(t, ac.Close())

	argv, err := os.ReadFile(argvFile)
	require.NoError(t, err)
	envRaw, err := os.ReadFile(envFile)
	require.NoError(t, err)
	// Membership, never a Contains against the whole dump: this file holds the
	// REAL environment of the machine running the suite, and an assertion that
	// prints it on failure would spill every host secret into the test log.
	childEnv := strings.Split(string(envRaw), "\n")
	hasEnv := func(entry string) bool { return slices.Contains(childEnv, entry) }

	assert.Contains(t, string(argv), "U062_SPAWN_SECRET", "the key crosses by NAME on the argv")
	assert.NotContains(t, string(argv), "s3cr3t-value",
		"the VALUE must never land on the run argv — it is world-readable via /proc/<pid>/cmdline")
	assert.True(t, hasEnv("U062_SPAWN_SECRET=s3cr3t-value"),
		"the value must reach the run process's own environment, where the runtime reads it")
	assert.True(t, hasEnv("U062_HOST_PASSTHROUGH=from-the-launcher"),
		"a bare-name -e NAME passthrough is only forwardable because the run process inherits this process's env")
}
