package isolation

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	taskpaths "github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// SessionState is the run's session identity threaded into the isolation seam:
// which harp names this run's per-session state dir, and which stable project
// id keys the shared task log. It decides the SCOPED read-write state mounts a
// containerized run gets (sessionStateMounts) and where a worktree's checkout
// and per-run scratch land (SessionState.workDir, SessionState.scratchDir).
// A run without a usable harp is refused, container and worktree alike, and
// SpecBuilder.Session stamps every prepared run's state with its own harp; a
// zero ProjectID skips the shared task-log facet. Never a
// blanket ~/.ctxloom mount: that would
// expose cache/bundles/config and every OTHER session's state to the run.
type SessionState struct {
	Harp      string
	ProjectID string
}

// noProjectIDNotice is the durability degrade a run without a project id
// reports. It is a WarnOnce line: a delegated fan-out (agent_run) puts every
// member through sessionStateMounts in ONE process, so N identical lines would
// be startup spam. The cost of that collapse is that the surviving line cannot
// name WHICH members it covers, so it says plainly that it speaks for every
// affected run rather than reading as one run's notice.
const noProjectIDNotice = "container runs with no project id: their in-container task writes will not reach the shared task log — reported once for every affected run in this session"

// safePathSegment reports whether s can be trusted as a single path segment
// under ~/.ctxloom/sessions. Harps are normally minted by AssignSession, but
// they arrive HERE from an env map — the same untrusted channel the task
// store validates project ids from — and this is the point where the value
// becomes both a host path and a bind-mount source.
func safePathSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\")
}

// errNoSessionHarp and errUnsafeSessionHarp are memberDir's two refusals of
// the harp itself, told apart from a failure to prepare the dir. Every caller
// refuses the run on either: a member dir outside the session is one no
// session teardown or reaper would ever find.
var (
	errNoSessionHarp     = errors.New("the run carries no session harp")
	errUnsafeSessionHarp = errors.New("session harp is not a safe path segment")
)

// scratchDir resolves and creates the session's scratch/ dir
// (paths.HarpScratchDir) — where every per-run scratch a workspace makes
// lives, so the session layout accounts for it and the session's Close
// sweeps whatever an owner that died left behind.
func (s SessionState) scratchDir() (string, error) { return s.memberDir(paths.HarpScratchDir) }

// workDir resolves and creates the session's work/ dir (paths.HarpWorkDir) —
// where its worktree checkouts live.
func (s SessionState) workDir() (string, error) { return s.memberDir(paths.HarpWorkDir) }

// memberDir resolves the session member at, refusing a missing or unsafe harp,
// and creates it.
func (s SessionState) memberDir(at func(string) (string, error)) (string, error) {
	if s.Harp == "" {
		return "", errNoSessionHarp
	}
	if !safePathSegment(s.Harp) {
		return "", fmt.Errorf("%w: %q", errUnsafeSessionHarp, s.Harp)
	}
	dir, err := at(s.Harp)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// containerOutputDir is where a containerized run reaches its session's
// output dir; CTXLOOM_OUTPUT_DIR names it for every process in the container
// (sessions.EnvOutputDir), because the sidecar that records the host path is
// not mounted.
const containerOutputDir = "/ctxloom/out"

// sessionStateMounts builds the scoped read-write state mounts that keep a
// containerized run's ctxloom-stateful writes durable across teardown, and
// the env they imply. The container gets a fresh HOME, so without these every
// in-container ~/.ctxloom write and session output dies with it. Each mount
// is scoped to exactly one concern:
//
//	~/.ctxloom/sessions/<harp>/<member>, for each member
//	    paths.MountedMembers names → the same path relative to the CONTAINER
//	    home. The table (paths.HarpMembers) decides what a container reaches.
//	the session's output dir (sessions.OutputDir) → containerOutputDir, with
//	    CTXLOOM_OUTPUT_DIR naming it: the readable outputs an agent writes
//	    (plans, reports) land where the human reads them.
//	~/.ctxloom/tasks/<project-id>.jsonl (and its .lock sidecar) → the same
//	    path under the container home, so an in-container taskloom's
//	    task_add/deferral reports reach the one host log every session of THIS
//	    project shares. Two single FILES, not the tasks dir: that dir holds
//	    every project on the machine, and a run keyed to one project id has no
//	    business reading — let alone appending to — another project's task
//	    log. Which project it is comes from the CTXLOOM_PROJECT_ID the run env
//	    pins.
//
// Native history is NOT among these: it is mounted beside the engine homes
// by the relocator (containerRelocator.relocate), where the home's relative
// link expects it. The container's ~/.ctxloom/locks is not either: see
// Container.lockMounts.
//
// VM-FS append hazard on the log: host and container both APPEND to it. On
// native Linux a bind mount is the same inode, so O_APPEND keeps concurrent
// appends atomic; on Docker Desktop's VM filesystems (gRPC-FUSE/9p) that
// atomicity is NOT guaranteed and interleaved appends can tear. The
// single-writer decision is parked on the macOS runbook (sudsy-sip).
//
// All of them are RW host state and so ride the container identity contract
// (entrypoint PUID/PGID remap): the in-container writer must be the host user
// or these dirs collect wrongly-owned files. A missing project id skips the
// task-log facet with a streamed warning, not a strictness finding. A missing
// or unsafe harp, a session with no recorded output dir, or a preparation
// FAILURE for a known identity, errors so the caller's degrade chain raises
// the fatal ClassIsolation finding.
func (c Container) sessionStateMounts() ([]mount, []string, error) {
	if !safePathSegment(c.state.Harp) {
		return nil, nil, fmt.Errorf("container session-state mounts: session harp %q is not a safe path segment", c.state.Harp)
	}
	mounts, err := c.harpStateMounts()
	if err != nil {
		return nil, nil, err
	}
	out, err := c.outputMounts()
	if err != nil {
		return nil, nil, err
	}
	taskMounts, err := c.taskStoreMounts()
	if err != nil {
		return nil, nil, err
	}
	mounts = append(append(mounts, out...), taskMounts...)
	return mounts, []string{sessions.EnvOutputDir + "=" + containerOutputDir}, nil
}

// harpStateMounts binds each Mounted member, creating every bind source
// first as the KIND of thing it is: a runtime asked to bind a source that is
// not there creates a directory in its place.
func (c Container) harpStateMounts() ([]mount, error) {
	var mounts []mount
	layout, err := sessions.HomeLayout()
	if err != nil {
		return nil, fmt.Errorf("container session-state mounts: %w", err)
	}
	for _, m := range paths.MountedMembers() {
		host := layout.Member(c.state.Harp, m)
		if m.File {
			if err := os.MkdirAll(filepath.Dir(host), 0o755); err != nil {
				return nil, fmt.Errorf("container session-state mounts: %w", err)
			}
			err = ensureFile(host)
		} else {
			err = os.MkdirAll(host, 0o755)
		}
		if err != nil {
			return nil, fmt.Errorf("container session-state mounts: %w", err)
		}
		mounts = append(mounts, bind(host, path.Join(c.home, paths.AppDirName, paths.SessionsDir, c.state.Harp, m.Rel()), false))
	}
	return mounts, nil
}

// sessionOutputDir is sessions.OutputDir, indirected so this package's tests
// can give the many container fixtures that mint no session an output dir.
var sessionOutputDir = sessions.OutputDir

// outputMounts binds the session's recorded output dir at containerOutputDir.
func (c Container) outputMounts() ([]mount, error) {
	dir, err := sessionOutputDir(c.state.Harp)
	if err != nil {
		return nil, fmt.Errorf("container output mount: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("container output mount: %w", err)
	}
	return []mount{bind(dir, containerOutputDir, false)}, nil
}

// taskStoreMounts binds the project's task log and its lock, or — with no
// pinned project id — warns and binds nothing.
func (c Container) taskStoreMounts() ([]mount, error) {
	var mounts []mount
	if c.state.ProjectID == "" {
		// Without a pinned project id the in-container taskloom would MINT a
		// fresh one and write a wrongly-keyed log; better that write dies with
		// the container than pollutes the shared host store.
		clidiag.WarnOnce("ctxloom", "%s", noProjectIDNotice)
	} else {
		// HomeTasksLogPath validates the project id as a single clean path
		// segment first — it arrives from the run env, the same untrusted
		// channel the harp does, and here it becomes a host path and a bind
		// source.
		logPath, err := taskpaths.HomeTasksLogPath(c.state.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("container task-store mount: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return nil, fmt.Errorf("container task-store mount: %w", err)
		}
		// The lock rides along because the log alone is only half the
		// protocol: every writer of this log takes paths.PathFor(log)
		// first, and a lock file the container cannot see is a lock that
		// excludes nothing across the boundary.
		//
		// Both sources must exist, as FILES, before `run`: a runtime asked to
		// bind a source that is not there creates a DIRECTORY in its place,
		// and neither the log nor the lock survives that.
		for _, src := range []string{logPath, paths.PathFor(logPath)} {
			if err := ensureFile(src); err != nil {
				return nil, fmt.Errorf("container task-store mount: %w", err)
			}
			mounts = append(mounts, bind(src, path.Join(c.home, taskpaths.AppDirName, taskpaths.TasksDir, filepath.Base(src)), false))
		}
	}
	return mounts, nil
}

// lockMounts gives the container its OWN ~/.ctxloom/locks — a per-run dir
// under scratchRoot, removed with it — never the host's. The host's locks dir
// guards host-only state (every session's files),
// and a child holding it RW could stall those writers by holding a lock,
// break their exclusion by deleting or replacing a lock file, or plant a
// symlink for the host to open.
//
// The one exception is a file both sides really do write: each of the
// engine's inPlaceFiles under dir, reached through the project bind. Its host
// lock file alone is bound, as a single file, at the name the container's own
// paths.HomePathFor gives it — computed from the IN-CONTAINER path, so a
// runtime that maps the project elsewhere still lands both sides on one
// inode. A single-file bind cannot be unlinked or replaced from inside; the
// child can still hold it, which stalls only writers of a file it may already
// rewrite. Overlaid files need no such lock: the overlay puts the container's
// copy on a different inode from the host's.
func (c Container) lockMounts(dir, scratchRoot string) ([]mount, error) {
	locks := safefs.New().Locks
	project, err := anchor(c.runtime, dir, false)
	if err != nil {
		return nil, fmt.Errorf("container lock mounts: project %s has no route into the container: %w", dir, err)
	}
	runLocks := filepath.Join(scratchRoot, paths.HomeLocksDirName)
	if err := os.MkdirAll(runLocks, 0o755); err != nil {
		return nil, fmt.Errorf("container lock mounts: %w", err)
	}
	containerLocks := path.Join(c.home, paths.AppDirName, paths.HomeLocksDirName)
	mounts := []mount{bind(runLocks, containerLocks, false)}
	for _, rel := range c.engineSpec.inPlaceFiles {
		protected := filepath.Join(dir, rel)
		hostLock, err := paths.HomePathFor(protected)
		if err != nil {
			return nil, fmt.Errorf("container lock mounts: %w", err)
		}
		inContainer, err := childPath(c.runtime, protected, project)
		if err != nil {
			return nil, fmt.Errorf("container lock mounts: %s has no route into the container: %w", protected, err)
		}
		name := paths.HomeLockName(inContainer)
		// Both must exist as FILES before `run`: the bind source, or the
		// runtime creates a directory in its place; and the target inside the
		// per-run dir, or a rootful runtime creates it there as root.
		for _, f := range []string{hostLock, filepath.Join(runLocks, name)} {
			if err := prepareLockFile(locks, f); err != nil {
				return nil, fmt.Errorf("container lock mounts: %w", err)
			}
		}
		mounts = append(mounts, bind(hostLock, path.Join(containerLocks, name), false))
	}
	return mounts, nil
}

// prepareLockFile makes the lock file at path (and its directory) exist as a
// regular file, under the refusals taking a lock applies, by taking it and
// letting it go: a bind source has to exist as a FILE before the runtime sees
// it. A holder of the lock at that instant is waited out.
func prepareLockFile(locks safefs.Locks, path string) error {
	lock, err := locks.Lock(path)
	if err != nil {
		return err
	}
	return lock.Unlock()
}

// ensureFile creates path as an empty regular file if it does not exist, and
// leaves an existing one untouched — never truncating a log that already has
// tasks in it.
func ensureFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}
