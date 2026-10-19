package operations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// LockSidecarReport is the lock-sidecar step of `ctxloom clean`: the sidecars
// in the home lock directory (paths.HomeLocksDir) that outlived the file they
// guard, and what became of them. A session's own sidecars are not here: they
// live in its locks/ member and are reaped with the session.
type LockSidecarReport struct {
	// Dir is the home lock directory the step read.
	Dir string `json:"dir"`
	// Applied reports whether this run reaped (or would have, had the gate
	// allowed it).
	Applied bool `json:"applied"`
	// Orphaned counts the sidecars whose guarded path no longer exists: the
	// reap's candidates.
	Orphaned int `json:"orphaned"`
	// Reaped counts the candidates this run removed.
	Reaped int `json:"reaped"`
	// Busy counts the candidates the reap left: a taker held the lock when it
	// tried, or the sidecar had changed under it.
	Busy int `json:"busy"`
	// InUse counts the sidecars whose guarded path still exists.
	InUse int `json:"in_use"`
	// Unreapable counts the sidecars that record no guarded path and whose
	// name cannot be read back into one (paths.FlatName keeps a tail and a
	// hash): nothing can tell whether what they guard is gone.
	Unreapable int `json:"unreapable"`
	// Refused is why no candidate was reaped, nor would be under --yes: the
	// gate's finding (LockReapGate). Empty when nothing needed gating or the
	// gate allowed the reap.
	Refused string `json:"refused,omitempty"`
	// Remedy is what to do about Refused.
	Remedy string `json:"remedy,omitempty"`
}

// LockSidecarRequest is one run of the lock-sidecar step.
type LockSidecarRequest struct {
	// Apply reaps; without it the step only reports.
	Apply bool
	// Gate is what must hold before any sidecar is removed.
	Gate LockReapGate
}

// ReapLockSidecars is the lock-sidecar step of `ctxloom clean`. It sorts
// every sidecar in the home lock directory by what it guards (classifySidecar)
// and, once Gate allows it and Apply asks for it, removes each candidate with
// safefs.ReapLockFile — never one a taker holds. The gate is consulted
// whenever there is a candidate, with or without Apply, so the report says
// what --yes would do.
//
// Removing a sidecar is safe only because every taker of this build re-checks
// the file it locked and retries on a replaced one (safefs.Locks); a binary
// older than that, or a container's bind of a host sidecar, defeats the
// re-check, which is what the gate rules out.
func ReapLockSidecars(ctx context.Context, req LockSidecarRequest) (LockSidecarReport, error) {
	dir, err := paths.HomeLocksDir()
	if err != nil {
		return LockSidecarReport{}, fmt.Errorf("clean: lock sidecars: %w", err)
	}
	rep := LockSidecarReport{Dir: dir, Applied: req.Apply}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return rep, nil
	}
	if err != nil {
		return rep, fmt.Errorf("clean: lock sidecars: read %s: %w", dir, err)
	}

	var candidates []string
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), lockFileSuffix) {
			continue
		}
		switch classifySidecar(filepath.Join(dir, e.Name())) {
		case sidecarOrphaned:
			candidates = append(candidates, filepath.Join(dir, e.Name()))
		case sidecarInUse:
			rep.InUse++
		default:
			rep.Unreapable++
		}
	}
	rep.Orphaned = len(candidates)
	if len(candidates) == 0 {
		return rep, nil
	}
	if rep.Refused, rep.Remedy = req.Gate.refusal(ctx); rep.Refused != "" || !req.Apply {
		return rep, nil
	}

	locks := safefs.New().Locks
	for _, path := range candidates {
		// Judged again at the reap: the guarded file may have come back since
		// the listing, and then its sidecar is in use again.
		if classifySidecar(path) != sidecarOrphaned {
			rep.Busy++
			continue
		}
		reaped, err := safefs.ReapLockFile(locks, path)
		if err != nil {
			return rep, fmt.Errorf("clean: lock sidecars: %w", err)
		}
		if reaped {
			rep.Reaped++
		} else {
			rep.Busy++
		}
	}
	return rep, nil
}

// lockFileSuffix ends every lock file paths names.
const lockFileSuffix = ".lock"

type sidecarVerdict int

const (
	// sidecarUnreapable: no recorded path, and a name that does not decode.
	sidecarUnreapable sidecarVerdict = iota
	// sidecarInUse: what it guards exists, or cannot be shown not to.
	sidecarInUse
	// sidecarOrphaned: what it guards is shown not to exist.
	sidecarOrphaned
)

// classifySidecar judges the sidecar at path by what it guards: the path it
// records (safefs.GuardedPath) when it records one; for a zero-byte sidecar
// that records nothing, the path its legacy unhashed name spells, when that
// name is one (legacyLockStem). Anything else — a hashed name with no record,
// a record caught half-written, content that is no record — is unreapable.
func classifySidecar(path string) sidecarVerdict {
	content, err := readSidecar(path)
	if err != nil {
		return sidecarUnreapable
	}
	if guarded, ok := safefs.GuardedPath(content); ok {
		if pathAbsent(guarded) {
			return sidecarOrphaned
		}
		return sidecarInUse
	}
	if len(content) > 0 {
		return sidecarUnreapable
	}
	parts, ok := legacyLockStem(filepath.Base(path))
	if !ok {
		return sidecarUnreapable
	}
	if legacyReadingExists(string(filepath.Separator), parts) {
		return sidecarInUse
	}
	return sidecarOrphaned
}

// sidecarReadMax bounds how much of a sidecar is read: past any record
// safefs.GuardedPath accepts, so a longer file reads as no record.
const sidecarReadMax = 8193

// readSidecar reads at most sidecarReadMax bytes of the sidecar at path.
func readSidecar(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, sidecarReadMax))
}

// pathAbsent reports whether path is SHOWN not to exist: it, or a directory
// on the way to it, is missing (ENOENT), or a file sits where a directory
// should (ENOTDIR). Any other failure — a directory it cannot search — shows
// nothing, and a dangling link at path still exists.
func pathAbsent(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// hashedLockName is the tail paths.FlatName gives every lock it names: "__",
// sixteen hex digits of the whole path's sha256, ".lock".
var hashedLockName = regexp.MustCompile(`__[0-9a-f]{16}\.lock$`)

// legacyLockStem splits a sidecar named in the legacy unhashed form — the
// absolute path, every "/" spelled "__", then ".lock" (paths.HomePathFor
// before paths.FlatName) — into the components that "__" separated. ok is
// false unless the name decodes without guessing where a separator sat: it
// starts with "__" (an absolute path), carries no FlatName hash, and has no
// run of three or more underscores (a component beginning or ending in "_"
// beside a separator, which could sit on either side of it) and no empty
// component. Whether each remaining "__" was a separator or part of a
// component's own name the name cannot say; legacyReadingExists tries both.
func legacyLockStem(name string) (parts []string, ok bool) {
	if hashedLockName.MatchString(name) {
		return nil, false
	}
	stem, found := strings.CutSuffix(name, lockFileSuffix)
	if !found {
		return nil, false
	}
	rest, found := strings.CutPrefix(stem, "__")
	if !found || rest == "" || strings.Contains(stem, "___") {
		return nil, false
	}
	parts = strings.Split(rest, "__")
	for _, p := range parts {
		if p == "" {
			return nil, false
		}
	}
	return parts, true
}

// legacyReadingExists reports whether any reading of parts below dir — each
// "__" between two parts read as a separator or as two underscores of one
// component's name — names something that exists, or that cannot be shown
// not to. It descends only into readings whose directories exist, so it
// stats each real prefix once per way of reaching it.
func legacyReadingExists(dir string, parts []string) bool {
	for i := 1; i <= len(parts); i++ {
		p := filepath.Join(dir, strings.Join(parts[:i], "__"))
		if i == len(parts) {
			if !pathAbsent(p) {
				return true
			}
			continue
		}
		info, err := os.Stat(p)
		switch {
		case err == nil && info.IsDir():
			if legacyReadingExists(p, parts[i:]) {
				return true
			}
		case err != nil && !pathAbsent(p):
			return true
		}
	}
	return false
}

// LockReapGate is what must hold before any lock sidecar is removed: no
// process that can take one follows an older lock protocol than this build's
// (safefs.LockProtocol), and no ctxloom agent container runs. Each of its
// sources is a field, so a test can stand each in.
type LockReapGate struct {
	// Self is this process's pid; it is never judged, and the listing must
	// include it.
	Self int
	// Processes lists the running processes. ErrProcessListUnavailable is
	// a platform that cannot.
	Processes func() ([]RunningProcess, error)
	// Version runs one process's own binary's `version --format json`.
	// ErrProcessGone is a process that exited first.
	Version func(p RunningProcess) ([]byte, error)
	// Containers names the running ctxloom agent containers.
	Containers func(ctx context.Context) ([]string, error)
}

// RunningProcess is one process as the gate's listing sees it.
type RunningProcess struct {
	PID int
	// Comm is the kernel's name for it: the base name of what it was started
	// as (a link's own name, when it was started through one).
	Comm string
	// Exe is where its binary is, as the kernel links it: suffixed
	// " (deleted)" once that file has been replaced or removed. Empty when
	// ExeErr says why it cannot be read.
	Exe    string
	ExeErr error
}

// ErrProcessListUnavailable is a platform whose running processes the gate
// cannot list.
var ErrProcessListUnavailable = errors.New("the running processes cannot be listed on this platform")

// ErrProcessGone is a process that exited before its binary could be asked.
var ErrProcessGone = errors.New("the process has exited")

// lockTakerNames are the binaries that take ctxloom's shared lock files.
var lockTakerNames = map[string]bool{"ctxloom": true, "ltk": true, "taskloom": true}

// deletedExeSuffix is what the kernel appends to a process's exe link once
// its binary file has been replaced or removed.
const deletedExeSuffix = " (deleted)"

// lockReapRetry is the remedy every refusal ends in.
const lockReapRetry = "then run `ctxloom clean --yes` again"

// refusal is the gate's verdict: empty when the reap may proceed, otherwise
// why it may not and what to do. Every source is consulted and every reason
// reported, so one fix is not followed by the next refusal. It never allows
// on an absence of evidence: an unlistable platform, a listing that does
// not show this process, a binary that cannot be asked or answers without
// lock_protocol, and a runtime that cannot list its containers all refuse.
func (g LockReapGate) refusal(ctx context.Context) (why, remedy string) {
	var whys, remedies []string
	procs, err := g.Processes()
	switch {
	case errors.Is(err, ErrProcessListUnavailable):
		whys = append(whys, "the check that no older ctxloom, ltk or taskloom is taking these locks is unavailable here: it reads the running processes from /proc")
		remedies = append(remedies, "with no ctxloom, ltk or taskloom process and no ctxloom container running, remove the lock directory by hand (it is recreated on next use)")
	case err != nil:
		whys = append(whys, fmt.Sprintf("the running processes could not be listed (%v), so an older ctxloom, ltk or taskloom cannot be ruled out", err))
		remedies = append(remedies, "fix what the error names, "+lockReapRetry)
	default:
		if old := g.olderTakers(procs); len(old) > 0 {
			whys = append(whys, "an older ctxloom-family binary may be taking these locks — "+strings.Join(old, "; ")+
				" — and one that does not re-check its lock file keeps a lock on a removed sidecar beside the next taker of the fresh one")
			remedies = append(remedies, fmt.Sprintf("stop those processes, or upgrade them (ctxloom, ltk and taskloom on PATH, and the agent images) to a build reporting lock_protocol %d, %s", safefs.LockProtocol, lockReapRetry))
		}
	}
	names, err := g.Containers(ctx)
	switch {
	case err != nil:
		whys = append(whys, fmt.Sprintf("the running containers could not be listed (%v), so a ctxloom agent container cannot be ruled out", err))
		remedies = append(remedies, "start or repair the container runtime so it answers, "+lockReapRetry)
	case len(names) > 0:
		whys = append(whys, "ctxloom agent containers are running ("+strings.Join(names, ", ")+
			"); a container binds host lock files into itself, where a sidecar removed on the host is never seen")
		remedies = append(remedies, "wait for those runs to end, or stop them, "+lockReapRetry)
	}
	return strings.Join(whys, "; "), strings.Join(remedies, "; ")
}

// olderTakers names each listed process that may take these locks under an
// older protocol than this build's, and why; empty when none can. It also
// names the listing itself when that listing does not include this process.
func (g LockReapGate) olderTakers(procs []RunningProcess) []string {
	var old []string
	selfSeen := false
	for _, p := range procs {
		if p.PID == g.Self {
			selfSeen = true
			continue
		}
		name := lockTakerName(p)
		if name == "" {
			continue
		}
		who := fmt.Sprintf("%s (pid %d)", name, p.PID)
		switch {
		case p.ExeErr != nil:
			old = append(old, fmt.Sprintf("%s, whose binary cannot be read (%v)", who, p.ExeErr))
			continue
		case strings.HasSuffix(p.Exe, deletedExeSuffix):
			old = append(old, fmt.Sprintf("%s runs %s, a binary since replaced, so possibly an older build", who, p.Exe))
			continue
		}
		raw, err := g.Version(p)
		switch {
		case errors.Is(err, ErrProcessGone):
			continue
		case err != nil:
			old = append(old, fmt.Sprintf("%s (%s) could not be asked its lock protocol: %v", who, p.Exe, err))
			continue
		}
		info, err := cliversion.ParseInfo(raw)
		switch {
		case err != nil:
			old = append(old, fmt.Sprintf("%s (%s) answered no lock protocol: %v", who, p.Exe, err))
		case info.LockProtocol == 0:
			old = append(old, fmt.Sprintf("%s (%s) reports no lock_protocol, so it predates the re-check", who, p.Exe))
		case info.LockProtocol < safefs.LockProtocol:
			old = append(old, fmt.Sprintf("%s (%s) reports lock_protocol %d, below %d", who, p.Exe, info.LockProtocol, safefs.LockProtocol))
		}
	}
	if !selfSeen {
		old = append(old, fmt.Sprintf("the process listing does not show this process (pid %d), so it cannot be trusted to show the others", g.Self))
	}
	return old
}

// lockTakerName is the family binary p runs, or "" for any other process:
// by its exe's base name, or — when it was started through a link of another
// name, or its exe cannot be read — by its kernel name.
func lockTakerName(p RunningProcess) string {
	if base := filepath.Base(strings.TrimSuffix(p.Exe, deletedExeSuffix)); p.Exe != "" && lockTakerNames[base] {
		return base
	}
	if lockTakerNames[p.Comm] {
		return p.Comm
	}
	return ""
}

// lockGateContainerWait bounds the container listing: a wedged runtime must
// refuse, not stall the clean.
const lockGateContainerWait = 15 * time.Second

// HostLockReapGate is the gate over this machine: its running processes, each
// family binary asked through the kernel's own link to the file it runs (so
// the answer is that binary's, wherever the path now points), and the
// container runtimes on PATH.
func HostLockReapGate() LockReapGate {
	return LockReapGate{
		Self:       os.Getpid(),
		Processes:  listProcesses,
		Version:    processVersion,
		Containers: runningAgentContainers,
	}
}

// runningAgentContainers names the running ctxloom agent containers of every
// OCI runtime on PATH. A runtime that is not installed launched nothing; one
// that is installed but cannot answer is an error, never an empty list.
func runningAgentContainers(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, lockGateContainerWait)
	defer cancel()
	var names []string
	for _, rt := range ociRuntimes() {
		if _, err := exec.LookPath(rt.Binary()); err != nil {
			continue
		}
		got, err := isolation.RunningContainers(ctx, rt)
		if err != nil {
			return nil, err
		}
		names = append(names, got...)
	}
	return names, nil
}
