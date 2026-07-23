package isolation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

// sharedFSResults memoizes one probe outcome per (runtime binary, image, exact
// mount-root set): map/weave fan many members through the same runtime+image
// against the SAME roots, and a DEFINITIVE answer cannot change within one
// process run — so the fleet pays for one short probe per distinct root set,
// not one per member. The root set is now PART of the key (not just runtime +
// image): a Docker Desktop custom file-sharing list grants sharing per HOST
// PATH, not per image, so two fan-out members whose mount sets differ (e.g.
// two per-agent worktree checkouts under different parent paths) can
// genuinely get different verdicts even against the identical runtime+image —
// collapsing them onto one memo key would silently paper over exactly the
// false-positive/false-negative gap this probe exists to catch. Only
// definitive outcomes latch here (see definitiveProbe): a transient run
// failure (a cold Docker Desktop VM, a daemon stall, our own probe timeout, or
// a cancelled caller ctx) must re-probe next call, never pin every later
// isolation decision to degrade for the process's life.
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

// probeKey builds the memo key for one (runtime, image, root-set) probe.
// roots is sorted by the caller (mountProbeRoots) so callers that assemble the
// same logical root set in a different order still hit the same memo entry;
// this only joins, it does not re-sort.
func probeKey(rt Runtime, image string, roots []string) string {
	return rt.Binary() + "|" + image + "|" + strings.Join(roots, "\x1f")
}

// mountProbeRoots derives the distinct HOST DIRECTORIES a container run will
// actually bind-mount, for the shared-fs probe to verify — replacing a single
// throwaway tempdir (which only ever proved os.TempDir()'s own sharing status,
// never the REAL roots a run mounts) with the actual mount set: the run's cwd
// (the live project dir, or the per-agent worktree checkout once created),
// the host scratch root (covers the socket dir, config overlays, session-state
// mounts, and any copy-based credential mount — all of them live under
// scratchRoot), and every OTHER mount's host path (a directly-mounted host
// file or dir, e.g. codex's ~/.codex/auth.json or a linked worktree's mirrored
// git common-dir). A mount whose Host is a FILE (a direct read-only credential
// mount) probes its PARENT DIRECTORY instead — the probe writes its own marker
// file alongside, never touching the real mounted file. Deduplicated and
// sorted for a stable, memoizable probe key.
func mountProbeRoots(dir, scratchRoot string, mounts []Mount) []string {
	seen := map[string]struct{}{}
	if dir != "" {
		seen[dir] = struct{}{}
	}
	if scratchRoot != "" {
		seen[scratchRoot] = struct{}{}
	}
	for _, m := range mounts {
		root := m.Host
		if root == "" {
			continue
		}
		if info, err := os.Stat(root); err == nil && !info.IsDir() {
			root = filepath.Dir(root)
		}
		seen[root] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// sharedFSProbe verifies the runtime's DAEMON shares this process's
// filesystem namespace with EVERY real mount root a container run needs —
// the invariant every identical-path bind mount in this package rests on. For
// each root it writes a unique marker file into a scratch subdirectory
// created INSIDE that root (never a synthetic directory elsewhere — a
// Docker Desktop custom file-sharing list grants sharing per host path, so
// probing anywhere else than the REAL roots is exactly the false-positive
// this replaces), bind-mounts it into a scratch run of the (already ensured)
// agent image, and reads the marker back through the daemon. CONTENT
// comparison, not existence: a host-side daemon (docker-outside-of-docker, a
// Docker Desktop VM without file sharing for that path) auto-creates an empty
// dir on ITS filesystem, so an empty or failed read is exactly the mismatch
// signature. nil means every root is shared (plain host, or true
// docker-in-docker); an error names the FIRST root that failed and explains
// why. Memoized per (runtime, image, root set) — see sharedFSResults' doc.
func sharedFSProbe(ctx context.Context, rt Runtime, image string, roots []string) error {
	key := probeKey(rt, image, roots)
	sharedFSMu.Lock()
	res, done := sharedFSResults[key]
	sharedFSMu.Unlock()
	if done {
		return res
	}
	res = runSharedFSProbe(ctx, rt, image, roots)
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

// runSharedFSProbe performs one un-memoized probe over EVERY root, failing on
// the first one that is not shared (or whose probe container did not run) —
// no false positive from a partially-shared Docker Desktop file-sharing list:
// a set with even ONE non-shared root among the real mounts must fail the
// whole gate, not just the roots probed before it.
func runSharedFSProbe(ctx context.Context, rt Runtime, image string, roots []string) error {
	if len(roots) == 0 {
		// Defensive: every real caller (container.go's prepareContainerScratch
		// gate, diagnose.go's advisory) always supplies at least the run's cwd.
		// An empty set here is an internal wiring bug, not a sharing verdict —
		// report it as a plain (non-mismatch, non-latched) error so it surfaces
		// as "could not probe" rather than a phantom sharing failure.
		return fmt.Errorf("fs probe: no mount roots to verify")
	}
	for _, root := range roots {
		if err := probeOneRoot(ctx, rt, image, root); err != nil {
			return err
		}
	}
	return nil
}

// probeOneRoot verifies a single mount root: a marker file written into a
// fresh scratch subdirectory created INSIDE root (so the marker lives at the
// REAL path a run would bind-mount, not a directory elsewhere), read back
// through one scratch container run of the given image.
func probeOneRoot(ctx context.Context, rt Runtime, image, root string) error {
	dir, err := os.MkdirTemp(root, "ctxloom-fsprobe-")
	if err != nil {
		// dir is normally "" here (MkdirTemp itself failed) — defensive
		// against a mutant flipping this check and orphaning a dir MkdirTemp
		// actually created, since the defer below is registered only after
		// this point.
		_ = os.RemoveAll(dir)
		return fmt.Errorf("fs probe scratch under %s: %w", root, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// The temp dir's unique basename doubles as the marker content and the
	// scratch container's name.
	marker := filepath.Base(dir)
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte(marker), 0o644); err != nil {
		return fmt.Errorf("fs probe marker under %s: %w", root, err)
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
		return probeRunError(root, err)
	}
	if got := strings.TrimSpace(out); got != marker {
		// The container ran and the daemon read the dir back, but not our marker
		// (empty = the docker-outside-of-docker auto-create signature). Definitive.
		return &sharedFSMismatch{fmt.Sprintf("mount root %s: marker content mismatch (daemon read %q) — this path is not shared with the container daemon", root, got)}
	}
	return nil
}

// probeRunError decorates a probe-run failure with the runtime's stderr when it
// carried one (exec's .Output() stashes it on *exec.ExitError), preserving the
// wrapped error so callers can still errors.Is it (context.Canceled/DeadlineExceeded).
func probeRunError(root string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if stderr := strings.TrimSpace(string(ee.Stderr)); stderr != "" {
			return fmt.Errorf("shared-fs probe container did not run for mount root %s: %w — %s", root, err, stderr)
		}
	}
	return fmt.Errorf("shared-fs probe container did not run for mount root %s: %w", root, err)
}
