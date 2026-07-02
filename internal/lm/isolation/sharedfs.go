package isolation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// sharedFSProbeTimeout bounds one marker-probe container run. Generous for a
// cold runtime, small next to an engine launch; a hung daemon degrades the
// runtime axis rather than stalling the run indefinitely.
const sharedFSProbeTimeout = 15 * time.Second

// probeExec is the exec seam for the marker probe, stubbed in tests so the
// probe logic is verifiable without a container runtime.
var probeExec = func(ctx context.Context, bin string, args []string) (string, error) {
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	return string(out), err
}

// sharedFSCheck is prepareContainerScratch's seam onto the probe, a package
// var so tests exercise the degrade path without a runtime.
var sharedFSCheck = sharedFSProbe

// sharedFSResults memoizes one probe outcome per (runtime binary, image):
// map/weave fan many members through the same runtime+image, and the answer
// cannot change within one process run — so the fleet pays for one short
// probe container, not one per member.
var (
	sharedFSMu      sync.Mutex
	sharedFSResults = map[string]error{}
)

// sharedFSProbe verifies the runtime's DAEMON shares this process's
// filesystem namespace — the invariant every identical-path bind mount in
// this package rests on. It writes a unique marker file into a temp dir,
// bind-mounts the dir into a scratch run of the (already ensured) agent
// image, and reads the marker back through the daemon. CONTENT comparison,
// not existence: a host-side daemon (docker-outside-of-docker, a Docker
// Desktop VM without file sharing) auto-creates an empty dir on ITS
// filesystem, so an empty or failed read is exactly the mismatch signature.
// nil means shared (plain host, or true docker-in-docker); an error explains
// the mismatch. Behavior-based on purpose — it also catches sharing gaps
// InContainer's heuristics can't see. Memoized per (runtime, image).
func sharedFSProbe(ctx context.Context, rt ContainerRuntime, image string) error {
	key := rt.Binary() + "|" + image
	sharedFSMu.Lock()
	res, done := sharedFSResults[key]
	sharedFSMu.Unlock()
	if done {
		return res
	}
	res = runSharedFSProbe(ctx, rt, image)
	sharedFSMu.Lock()
	sharedFSResults[key] = res
	sharedFSMu.Unlock()
	return res
}

// runSharedFSProbe performs one un-memoized probe.
func runSharedFSProbe(ctx context.Context, rt ContainerRuntime, image string) error {
	dir, err := os.MkdirTemp("", "ctxloom-fsprobe-")
	if err != nil {
		return fmt.Errorf("fs probe scratch: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// The temp dir's unique basename doubles as the marker content and the
	// scratch container's name.
	marker := filepath.Base(dir)
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte(marker), 0o644); err != nil {
		return fmt.Errorf("fs probe marker: %w", err)
	}

	cctx, cancel := context.WithTimeout(ctx, sharedFSProbeTimeout)
	defer cancel()
	args := rt.RunArgs(RunSpec{
		Image:   image,
		Name:    marker,
		Command: []string{"cat", "/probe/marker"},
		Mounts:  []Mount{{Host: dir, Container: "/probe", ReadOnly: true}},
	})
	out, err := probeExec(cctx, rt.Binary(), args)
	if err != nil {
		return fmt.Errorf("marker not readable through the daemon: %w", err)
	}
	if strings.TrimSpace(out) != marker {
		return fmt.Errorf("marker content mismatch (daemon read %q)", strings.TrimSpace(out))
	}
	return nil
}
