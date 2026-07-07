package isolation

import (
	"context"
	"errors"
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
// map/weave fan many members through the same runtime+image, and a DEFINITIVE
// answer cannot change within one process run — so the fleet pays for one short
// probe container, not one per member. Only definitive outcomes latch here (see
// definitiveProbe): a transient run failure (a cold Docker Desktop VM, a daemon
// stall, our own probe timeout, or a cancelled caller ctx) must re-probe next
// call, never pin every later isolation decision to degrade for the process's life.
var (
	sharedFSMu      sync.Mutex
	sharedFSResults = map[string]error{}
)

// sharedFSMismatch is a DEFINITIVE negative verdict: the probe container ran and
// the daemon read the marker back, but the content did not match this process's
// write (an empty read is the docker-outside-of-docker auto-create signature).
// Definitive because the filesystem genuinely is not shared, so it — like a
// success — latches into the memo. A run FAILURE (daemon down/cold, image
// unreadable, probe timeout, cancellation) is a plain wrapped error instead:
// transient, never latched, and reported as its real cause rather than as a
// sharing mismatch.
type sharedFSMismatch struct{ msg string }

func (e *sharedFSMismatch) Error() string { return e.msg }

// definitiveProbe reports whether a probe outcome is a permanent per-process
// verdict safe to memoize: success (nil) or a genuine content mismatch. Every
// other error is a transient run failure that must re-probe.
func definitiveProbe(err error) bool {
	if err == nil {
		return true
	}
	var mism *sharedFSMismatch
	return errors.As(err, &mism)
}

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
	// Latch ONLY a definitive outcome. Caching a transient failure (a cold VM's
	// first probe, a stall, DeadlineExceeded, context.Canceled) would pin every
	// later isolation decision in this long-lived process to degrade until restart.
	if definitiveProbe(res) {
		sharedFSMu.Lock()
		sharedFSResults[key] = res
		sharedFSMu.Unlock()
	}
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
		// The probe container did not run to completion: daemon down/cold, image
		// unreadable, our own timeout, or a cancelled caller ctx. This is NOT a
		// filesystem-sharing verdict — surface the REAL cause (docker's stderr,
		// which .Output() stashes on the ExitError) so the fix-it points at the
		// daemon/image, not at a phantom sharing gap. Transient: never memoized.
		return probeRunError(err)
	}
	if got := strings.TrimSpace(out); got != marker {
		// The container ran and the daemon read the dir back, but not our marker
		// (empty = the docker-outside-of-docker auto-create signature). Definitive.
		return &sharedFSMismatch{fmt.Sprintf("marker content mismatch (daemon read %q) — the filesystem is not shared", got)}
	}
	return nil
}

// probeRunError decorates a probe-run failure with the runtime's stderr when it
// carried one (exec's .Output() stashes it on *exec.ExitError), preserving the
// wrapped error so callers can still errors.Is it (context.Canceled/DeadlineExceeded).
func probeRunError(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if stderr := strings.TrimSpace(string(ee.Stderr)); stderr != "" {
			return fmt.Errorf("shared-fs probe container did not run: %w — %s", err, stderr)
		}
	}
	return fmt.Errorf("shared-fs probe container did not run: %w", err)
}
