package isolation

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/exitstatus"
)

const gib = int64(1) << 30

// flagValue returns the value of the `--name=value` flag in args, and
// whether it is there at all.
func flagValue(args []string, name string) (string, bool) {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v, true
		}
	}
	return "", false
}

// imageIndex is where the image sits in a run argv: every run FLAG must come
// before it, or the runtime reads the flag as the in-container command.
func imageIndex(t *testing.T, args []string, image string) int {
	t.Helper()
	i := slices.Index(args, image)
	require.GreaterOrEqual(t, i, 0, "image %q missing from %v", image, args)
	return i
}

// Both OCI runtimes, in both ownership modes, render the resolved caps as
// --memory, --memory-swap EQUAL to it (so swap cannot stretch the cap), and
// --pids-limit — all ahead of the image.
func TestRunArgs_RenderResourceLimitsForEveryRuntime(t *testing.T) {
	runtimes := map[string]Runtime{
		"docker rootful":  Docker{},
		"docker rootless": Docker{rootless: true},
		"podman rootful":  Podman{},
		"podman rootless": Podman{rootless: true},
	}
	spec := RunSpec{
		Image: "img:1", Name: "n", Command: []string{"ctxloom", "runner"},
		Limits: resourceLimits{memory: 3 * gib, memorySwap: 3 * gib, pids: 512},
	}
	for name, rt := range runtimes {
		args, err := rt.RunArgs(spec)
		require.NoError(t, err, name)
		img := imageIndex(t, args, "img:1")
		for flag, want := range map[string]string{
			"--memory":      fmt.Sprint(3 * gib),
			"--memory-swap": fmt.Sprint(3 * gib),
			"--pids-limit":  "512",
		} {
			got, ok := flagValue(args[:img], flag)
			assert.True(t, ok, "%s: %s missing from %v", name, flag, args)
			assert.Equal(t, want, got, "%s: %s", name, flag)
		}
	}
}

// An off cap renders no flag at all: the runtime's own default (none) holds.
func TestRenderRunSpec_NoLimitsRenderNoFlags(t *testing.T) {
	args, err := renderRunSpec(RunSpec{Image: "img:1"}, HostLayer())
	require.NoError(t, err)
	for _, flag := range []string{"--memory", "--memory-swap", "--pids-limit"} {
		_, ok := flagValue(args, flag)
		assert.False(t, ok, "%s rendered for an off cap: %v", flag, args)
	}
}

// A memory cap with no enforceable swap limit renders --memory alone.
func TestRenderRunSpec_MemoryWithoutSwapLimit(t *testing.T) {
	args, err := renderRunSpec(RunSpec{Image: "img:1", Limits: resourceLimits{memory: 2 * gib}}, HostLayer())
	require.NoError(t, err)
	got, ok := flagValue(args, "--memory")
	assert.True(t, ok)
	assert.Equal(t, fmt.Sprint(2*gib), got)
	_, ok = flagValue(args, "--memory-swap")
	assert.False(t, ok)
}

// The default memory cap is half the daemon host's memory, floored at
// defaultMemoryFloor, and never above the host's own total.
func TestDefaultMemoryLimit_DerivedFromHostMemory(t *testing.T) {
	for host, want := range map[int64]int64{
		64 * gib:    32 * gib,
		32 * gib:    16 * gib,
		6 * gib:     3 * gib,
		4 * gib:     defaultMemoryFloor, // half is exactly the floor
		3 * gib:     defaultMemoryFloor, // half would be under it
		1 * gib:     1 * gib,            // the floor never exceeds the host
		33302732800: 16651366400,        // an odd byte count halves exactly
		0:           0,                  // unknown host: nothing to derive
		-1:          0,
	} {
		assert.Equal(t, want, defaultMemoryLimit(host), "host %d", host)
	}
}

func enforcing(hostMemory int64) limitSupport {
	return limitSupport{hostMemory: hostMemory, memory: true, swap: true, pids: true}
}

// resolveLimits: unset takes the host-derived default, an explicit value is
// taken as written, 0/unlimited renders nothing.
func TestResolveLimits(t *testing.T) {
	fixed := func(n int64) config.ContainerLimit { return config.ContainerLimit{Kind: config.LimitFixed, Value: n} }
	off := config.ContainerLimit{Kind: config.LimitUnlimited}

	got := resolveLimits(config.ContainerLimits{}, enforcing(32*gib))
	assert.Equal(t, resourceLimits{memory: 16 * gib, memorySwap: 16 * gib, pids: defaultPidsLimit}, got, "unset: defaults")

	got = resolveLimits(config.ContainerLimits{Memory: fixed(5 * gib), PIDs: fixed(100)}, enforcing(32*gib))
	assert.Equal(t, resourceLimits{memory: 5 * gib, memorySwap: 5 * gib, pids: 100}, got, "explicit values")

	got = resolveLimits(config.ContainerLimits{Memory: off, PIDs: off}, enforcing(32*gib))
	assert.Equal(t, resourceLimits{}, got, "disabled: no caps")

	args, err := renderRunSpec(RunSpec{Image: "i", Limits: got}, HostLayer())
	require.NoError(t, err)
	assert.Equal(t, []string{"i"}, args, "disabled caps render no flags")
}

// What the runtime cannot enforce is not rendered (a rootless podman on
// cgroup v1 REFUSES the run outright when given a cap): each unenforceable
// cap is dropped, and a swap limit the kernel does not account is dropped
// alone, leaving --memory.
func TestResolveLimits_DropsWhatTheRuntimeCannotEnforce(t *testing.T) {
	none := limitSupport{hostMemory: 32 * gib, unenforced: "rootless podman on cgroup v1"}
	assert.Equal(t, resourceLimits{}, resolveLimits(config.ContainerLimits{}, none))

	noSwap := enforcing(32 * gib)
	noSwap.swap = false
	assert.Equal(t, resourceLimits{memory: 16 * gib, pids: defaultPidsLimit}, resolveLimits(config.ContainerLimits{}, noSwap))

	noPids := enforcing(32 * gib)
	noPids.pids = false
	assert.Equal(t, resourceLimits{memory: 16 * gib, memorySwap: 16 * gib}, resolveLimits(config.ContainerLimits{}, noPids))

	unknownHost := enforcing(0)
	assert.Equal(t, resourceLimits{pids: defaultPidsLimit}, resolveLimits(config.ContainerLimits{}, unknownHost),
		"no host memory reported: no default memory cap to derive")
}

// The engine's `info` answer decides enforceability and host memory.
func TestDockerLimitSupport(t *testing.T) {
	sup := dockerLimitSupport("33302732800 true true true [name=seccomp,profile=builtin name=rootless name=cgroupns]")
	assert.Equal(t, limitSupport{hostMemory: 33302732800, memory: true, swap: true, pids: true}, sup)

	// rootless docker on cgroup v1: the daemon reports no cgroup control.
	sup = dockerLimitSupport("8000000000 false false false [name=rootless]")
	assert.Equal(t, int64(8000000000), sup.hostMemory)
	assert.False(t, sup.memory)
	assert.False(t, sup.pids)
	assert.NotEmpty(t, sup.unenforced)

	assert.Equal(t, limitSupport{}, dockerLimitSupport("garbage"), "an unreadable answer enforces nothing it cannot confirm")
}

func TestPodmanLimitSupport(t *testing.T) {
	cases := map[string]struct {
		answer   string
		rootless bool
		want     limitSupport
	}{
		"rootless v2 systemd, delegated": {
			"true pasta 33302732800 v2 systemd [cpuset cpu io memory pids]", true,
			limitSupport{hostMemory: 33302732800, memory: true, swap: true, pids: true},
		},
		"rootless v1": {
			"true slirp4netns 1000 v1 cgroupfs []", true,
			limitSupport{hostMemory: 1000, unenforced: podmanRootlessV1},
		},
		"rootless v2 cgroupfs": {
			"true pasta 1000 v2 cgroupfs [memory pids]", true,
			limitSupport{hostMemory: 1000, unenforced: podmanRootlessNoSystemd},
		},
		"rootless v2, pids not delegated": {
			"true pasta 1000 v2 systemd [memory]", true,
			limitSupport{hostMemory: 1000, memory: true, swap: true},
		},
		"rootful v1": {
			"false  1000 v1 systemd [memory pids]", false,
			limitSupport{hostMemory: 1000, memory: true, swap: true, pids: true},
		},
		"empty network field keeps positions": {
			"true  2000 v2 systemd [memory pids]", true,
			limitSupport{hostMemory: 2000, memory: true, swap: true, pids: true},
		},
	}
	for name, c := range cases {
		assert.Equal(t, c.want, podmanLimitSupport(c.answer, c.rootless), name)
	}
}

// Selection's one `info` call carries both the ownership and the limit
// facts: the probed runtime holds them, with no second exec.
func TestEngineProbe_CarriesLimitSupport(t *testing.T) {
	resetStrictness(t)
	stubEngine(t, &engineAnswers{ownership: func() (string, error) {
		return "true pasta 4000 v2 systemd [memory pids]", nil
	}})
	p, owns := newPodmanRuntime(func(string) bool { return true })
	assert.Equal(t, RuntimeContainerRootless, owns)
	assert.Equal(t, "pasta", p.rootlessNet, "the network command still reads from the same answer")
	assert.Equal(t, limitSupport{hostMemory: 4000, memory: true, swap: true, pids: true}, p.limitSupport())

	stubEngine(t, &engineAnswers{ownership: func() (string, error) {
		return "4000 true false true [name=seccomp]", nil
	}})
	d, owns := newDockerRuntime(func(string) bool { return true })
	assert.Equal(t, RuntimeContainerRootful, owns)
	assert.Equal(t, limitSupport{hostMemory: 4000, memory: true, pids: true}, d.limitSupport())
}

// buildRunnerSpec resolves the configured caps against the selected
// runtime's support: the run argv the runner starts with carries them.
func TestBuildRunnerSpec_CarriesResolvedLimits(t *testing.T) {
	d := Docker{}
	d.limits = enforcing(8 * gib)
	c := containerFor(d, "claude-code", ImageConfig{Image: "img:1"})
	cw := &containerWorkspace{}
	spec := c.buildRunnerSpec("claude-code", "ctr", cw, nil)
	assert.Equal(t, resourceLimits{memory: 4 * gib, memorySwap: 4 * gib, pids: defaultPidsLimit}, spec.Limits)

	c = containerFor(d, "claude-code", ImageConfig{Image: "img:1", Limits: config.ContainerLimits{
		Memory: config.ContainerLimit{Kind: config.LimitUnlimited},
		PIDs:   config.ContainerLimit{Kind: config.LimitUnlimited},
	}})
	assert.Equal(t, resourceLimits{}, c.buildRunnerSpec("claude-code", "ctr", cw, nil).Limits)
}

// exitErr137 is a classified SIGKILL exit, as the runner's Wait reports it.
func exitErr137(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "kill -KILL $$").Run()
	require.Error(t, err)
	return exitstatus.Classify(fmt.Errorf("runner container %q exited: %w", "ctr", err))
}

// stubOOMEvents answers the OOM-event query with out, recording the argv.
func stubOOMEvents(t *testing.T, out string, err error) *[][]string {
	t.Helper()
	var calls [][]string
	orig := probeExec
	probeExec = func(_ context.Context, _ string, args []string) (string, error) {
		calls = append(calls, args)
		return out, err
	}
	t.Cleanup(func() { probeExec = orig })
	return &calls
}

// The daemon's record of an OOM kill turns the runner's exit into
// "OOM-killed (memory limit N)" — the end record renders this error — and the
// exit status the coordinator reads is kept.
func TestNoteOOMKill_DockerRecordsTheKill(t *testing.T) {
	calls := stubOOMEvents(t, "oom\n", nil)
	since := time.Unix(1700000000, 5)
	err := noteOOMKill(Docker{}, "ctr", resourceLimits{memory: 2 * gib}, since, exitErr137(t))
	assert.Contains(t, err.Error(), "OOM-killed (memory limit 2.0GiB)")
	var es *exitstatus.Error
	require.True(t, errors.As(err, &es), "the classified status survives")
	assert.Equal(t, 137, es.ExitStatus())
	assert.Contains(t, es.Error(), "OOM-killed", "the OUTERMOST classified error carries the note, so a reader of the status sees it too")
	require.Len(t, *calls, 1)
	args := (*calls)[0]
	assert.Equal(t, "events", args[0])
	assert.Contains(t, args, "container=ctr")
	assert.Contains(t, args, "event=oom")
	assert.Contains(t, args, "1700000000.000000005", "the query starts at the run's start")
}

// No OOM event: the error is unchanged.
func TestNoteOOMKill_NoEventLeavesTheErrorAlone(t *testing.T) {
	stubOOMEvents(t, "", nil)
	in := exitErr137(t)
	assert.Equal(t, in, noteOOMKill(Docker{}, "ctr", resourceLimits{memory: 2 * gib}, time.Now(), in))

	stubOOMEvents(t, "", errors.New("daemon gone"))
	assert.Equal(t, in, noteOOMKill(Docker{}, "ctr", resourceLimits{memory: 2 * gib}, time.Now(), in), "a failed query claims nothing")
}

// Nothing to note without a memory cap or without a failure, and the daemon
// is not asked.
func TestNoteOOMKill_OnlyForAFailedCappedRun(t *testing.T) {
	calls := stubOOMEvents(t, "oom\n", nil)
	in := exitErr137(t)
	assert.Equal(t, in, noteOOMKill(Docker{}, "ctr", resourceLimits{}, time.Now(), in))
	assert.NoError(t, noteOOMKill(Docker{}, "ctr", resourceLimits{memory: gib}, time.Now(), nil))
	assert.Empty(t, *calls)
}

// Podman records no OOM event once --rm has removed the container, so the
// kill cannot be confirmed: a SIGKILL under a cap is reported as POSSIBLY the
// cap's, never as confirmed, and any other failure is left alone.
func TestNoteOOMKill_PodmanCannotConfirm(t *testing.T) {
	calls := stubOOMEvents(t, "oom\n", nil)
	err := noteOOMKill(Podman{}, "ctr", resourceLimits{memory: 2 * gib}, time.Now(), exitErr137(t))
	assert.Contains(t, err.Error(), "possibly OOM-killed (memory limit 2.0GiB")
	assert.NotContains(t, err.Error(), "; OOM-killed")
	assert.Empty(t, *calls, "podman has no OOM record to ask for")

	other := exitstatus.Classify(fmt.Errorf("x: %w", exec.Command("false").Run()))
	assert.Equal(t, other, noteOOMKill(Podman{}, "ctr", resourceLimits{memory: gib}, time.Now(), other))
}
