package containercell

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestDockerProbe_RootfulAndRootlessAreExclusive pins the one thing a reader
// most easily gets wrong: docker-rootful and docker-rootless are derived from
// ONE probe and can never both be available. If they could, a matrix would
// report covering an ownership axis it never crossed.
func TestDockerProbe_RootfulAndRootlessAreExclusive(t *testing.T) {
	for _, p := range []dockerProbe{
		{reachable: true, rootless: true, detail: "docker 29.3.0"},
		{reachable: true, rootless: false, detail: "docker 29.3.0"},
		{reachable: false, detail: "no `docker` on PATH"},
	} {
		full, less := p.asRootful(), p.asRootless()
		if full.Available && less.Available {
			t.Fatalf("probe %+v reported BOTH rootful and rootless available", p)
		}
		if p.reachable && !full.Available && !less.Available {
			t.Fatalf("probe %+v is reachable but classified as neither rootful nor rootless", p)
		}
		if full.Detail == "" || less.Detail == "" {
			t.Fatalf("probe %+v produced an empty Detail; a skip must always name why", p)
		}
	}
}

// TestDockerProbe_UnavailableDetailNamesTheOtherMode: a rootless host skipping
// the rootful row must say WHY it is not the rootful runtime, not merely that
// it is not available — otherwise "docker is right there" reads as a bug.
func TestDockerProbe_UnavailableDetailNamesTheOtherMode(t *testing.T) {
	p := dockerProbe{reachable: true, rootless: true, detail: "docker 29.3.0"}
	if got := p.asRootful().Detail; !strings.Contains(got, "ROOTLESS") {
		t.Fatalf("rootful Detail on a rootless host = %q, want it to name the rootless daemon", got)
	}
	q := dockerProbe{reachable: true, rootless: false, detail: "docker 29.3.0"}
	if got := q.asRootless().Detail; !strings.Contains(got, "ROOTFUL") {
		t.Fatalf("rootless Detail on a rootful host = %q, want it to name the rootful daemon", got)
	}
}

// TestUserFlag_TracksTheOwnershipMapping is the rule the whole ownership axis
// rests on: run as root where root IS the invoker, remap where it is not.
// Inverting either half writes files the host user does not own.
func TestUserFlag_TracksTheOwnershipMapping(t *testing.T) {
	rootless := Runtime{Name: DockerRootless, RootMapsToInvoker: true}
	if got := rootless.UserFlag(); got != "" {
		t.Fatalf("rootless UserFlag = %q, want empty: --user would select an unmapped subuid that cannot write the mount", got)
	}
	rootful := Runtime{Name: DockerRootful, RootMapsToInvoker: false}
	want := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	if got := rootful.UserFlag(); got != want {
		t.Fatalf("rootful UserFlag = %q, want %q", got, want)
	}
}

// TestRun_RefusesAWorkDirOutsideTheMounts is the anti-vacuity check on the cell
// itself. A container whose working directory is not under a bind mount runs
// against its own ephemeral layer: it can exit 0, print success, and leave the
// host with nothing — the silent no-op, reproduced inside the machinery built
// to catch it. The cell must refuse before it starts, not report an empty
// result afterwards.
func TestRun_RefusesAWorkDirOutsideTheMounts(t *testing.T) {
	r := Runtime{Name: DockerRootless, Command: "docker", Available: true, RootMapsToInvoker: true}
	_, err := r.Run(t.Context(), Spec{Mounts: []Mount{{Source: "/tmp/mounted", Path: "/tmp/mounted"}}, WorkDir: "/elsewhere", Args: []string{"version"}})
	if err == nil {
		t.Fatal("expected a refusal for a WorkDir outside every mount")
	}
	if !strings.Contains(err.Error(), "not under any mount") {
		t.Fatalf("error does not name the problem: %v", err)
	}
}

// TestRunArgv_BindsTheDaemonsSourceAtThePath: the container sees a mount at
// the path the test uses, while the daemon is handed that directory's own name
// for it — from a process that shares nothing with the daemon at the same path
// the two differ, and a bind of the test's path would be a blank directory.
func TestRunArgv_BindsTheDaemonsSourceAtThePath(t *testing.T) {
	r := Runtime{Name: DockerRootless, Command: "docker", Available: true, RootMapsToInvoker: true}
	argv := r.runArgv(Spec{
		Mounts:  []Mount{{Source: "/home/runner/work/_temp/cell", Path: "/__w/_temp/cell"}},
		WorkDir: "/__w/_temp/cell/project",
		Args:    []string{"version"},
	})
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-v /home/runner/work/_temp/cell:/__w/_temp/cell ") {
		t.Fatalf("the daemon is not handed its own name for the mount: %s", joined)
	}
	if !strings.Contains(joined, "-w /__w/_temp/cell/project ") {
		t.Fatalf("the container does not work at the test's path: %s", joined)
	}
}

// TestRunArgv_UserFlagOnlyWhenRootIsNotTheInvoker: a runtime whose container
// root is not the invoker runs as the invoker's uid:gid; one whose root IS the
// invoker passes no --user at all, never an empty one.
func TestRunArgv_UserFlagOnlyWhenRootIsNotTheInvoker(t *testing.T) {
	spec := Spec{Mounts: []Mount{{Source: "/tmp/cell", Path: "/tmp/cell"}}, WorkDir: "/tmp/cell", Args: []string{"version"}}
	mapped := Runtime{Name: DockerRootless, Command: "docker", Available: true, RootMapsToInvoker: true}
	if argv := mapped.runArgv(spec); slices.Contains(argv, "--user") {
		t.Fatalf("root maps to the invoker, yet --user is passed: %v", argv)
	}
	unmapped := Runtime{Name: DockerRootless, Command: "docker", Available: true}
	want := fmt.Sprintf("--user %d:%d ", os.Getuid(), os.Getgid())
	if joined := strings.Join(unmapped.runArgv(spec), " "); !strings.Contains(joined, want) {
		t.Fatalf("root is not the invoker, yet the run does not say %q: %s", want, joined)
	}
}

func TestRun_RefusesAnEmptyWorkDir(t *testing.T) {
	r := Runtime{Name: DockerRootless, Command: "docker", Available: true, RootMapsToInvoker: true}
	if _, err := r.Run(t.Context(), Spec{Mounts: []Mount{{Source: "/tmp/mounted", Path: "/tmp/mounted"}}, Args: []string{"version"}}); err == nil {
		t.Fatal("expected a refusal for an empty WorkDir")
	}
}

// TestRun_RefusesAnUnavailableRuntime: the gate decides about availability, but
// a caller that skipped the gate must not silently get a zero Result that reads
// like a run.
func TestRun_RefusesAnUnavailableRuntime(t *testing.T) {
	r := Runtime{Name: Podman, Command: "podman", Available: false, Detail: "no `podman` on PATH"}
	_, err := r.Run(t.Context(), Spec{Mounts: []Mount{{Source: "/tmp", Path: "/tmp"}}, WorkDir: "/tmp", Args: []string{"version"}})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("expected an unavailable-runtime refusal, got %v", err)
	}
}

func TestUnderAny(t *testing.T) {
	// Host paths, so in the host's separator: underAny joins on filepath.Separator.
	mounts := []string{filepath.FromSlash("/tmp/root")}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/tmp/root", true},
		{"/tmp/root/project", true},
		{"/tmp/root/project/deep/er", true},
		{"/tmp/rootsibling", false}, // prefix-of-string is not under-the-directory
		{"/tmp", false},
		{"/elsewhere", false},
	} {
		if got := underAny(filepath.FromSlash(tc.path), mounts); got != tc.want {
			t.Fatalf("underAny(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestReport_NamesEveryRuntimeAndItsReason: the report is how a run that
// covered one runtime is distinguishable from one that covered three.
func TestReport_NamesEveryRuntimeAndItsReason(t *testing.T) {
	got := Report([]Runtime{
		{Name: DockerRootful, Detail: "the reachable docker daemon is ROOTLESS"},
		{Name: DockerRootless, Available: true, Detail: "docker 29.3.0"},
		{Name: Podman, Detail: "no `podman` on PATH"},
	})
	for _, want := range []string{DockerRootful, DockerRootless, Podman, "AVAILABLE", "unavailable", "no `podman` on PATH"} {
		if !strings.Contains(got, want) {
			t.Fatalf("report is missing %q:\n%s", want, got)
		}
	}
}

// TestBinary_RejectsAMissingPrebuiltBinary: EnvBinary is the CI shortcut, and a
// stale path must fail loudly rather than falling back to a build the lane
// thought it had already done.
//
// It manipulates prebuiltBinary rather than the env var, for the same reason
// dockergate's tests manipulate `required` directly: the variable is captured
// once at package init (it is in testsupport.EnvKeys and therefore CLEARED by
// Isolate), so t.Setenv here would change nothing and the test would silently
// fall through to a real build.
func TestBinary_RejectsAMissingPrebuiltBinary(t *testing.T) {
	old := prebuiltBinary
	prebuiltBinary = "/nonexistent/ctxloom"
	t.Cleanup(func() { prebuiltBinary = old })

	_, err := buildBinary(t.Context())
	if err == nil {
		t.Fatal("expected an error for a missing prebuilt binary")
	}
	if !strings.Contains(err.Error(), EnvBinary) {
		t.Fatalf("error does not name the variable that pointed at the missing binary: %v", err)
	}
}

// TestModuleRoot_FindsTheGoMod: the cell builds ctxloom from the module root,
// and tests run with their own package as cwd.
func TestModuleRoot_FindsTheGoMod(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root + "/go.mod"); err != nil {
		t.Fatalf("moduleRoot() = %q which holds no go.mod: %v", root, err)
	}
}
