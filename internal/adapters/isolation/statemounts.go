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
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
	taskpaths "github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
)

// SessionState is the run's session identity threaded into the isolation seam:
// which harp names this run's per-session state dir, and which stable project
// id keys the shared task log. It decides the SCOPED read-write state mounts a
// containerized run gets (sessionStateMounts) and where a worktree's ephemeral
// scratch lands (Worktree.scratchBase). A container run without a usable harp
// is refused (SessionState.ephemeralDir); a worktree without one falls back to
// the OS temp dir; a zero ProjectID skips the shared task-log facet. Never a
// blanket ~/.ctxloom mount: that would
// expose cache/bundles/config and every OTHER session's state to the run.
type SessionState struct {
	Harp      string
	ProjectID string
}

// SessionStateFromEnv reads the session identity from a run's env map — the
// same CTXLOOM_SESSION_HARP / CTXLOOM_PROJECT_ID the launch paths already
// export into the engine env (run.go's runEnv, the delegated child's env), so
// the isolation layer and the in-container writers key off ONE source. Absent
// keys yield zero fields.
func SessionStateFromEnv(env map[string]string) SessionState {
	return SessionState{
		Harp:      env[sessions.EnvHarp],
		ProjectID: env[sessions.EnvProjectID],
	}
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

// errNoSessionHarp and errUnsafeSessionHarp are ephemeralDir's two refusals of
// the harp itself, told apart from a failure to prepare the dir: the worktree
// half stays silent on the first, warns on the second, and falls back to the
// OS temp dir on either, while the container half refuses the run on both.
var (
	errNoSessionHarp     = errors.New("the run carries no session harp")
	errUnsafeSessionHarp = errors.New("session harp is not a safe path segment")
)

// ephemeralDir resolves and creates the session's ephemeral/ dir
// (paths.HarpEphemeralDir) — where every per-run scratch a workspace makes
// lives, so the session layout accounts for it and cleanup of the session dir
// sweeps whatever an owner that died left behind.
func (s SessionState) ephemeralDir() (string, error) {
	if s.Harp == "" {
		return "", errNoSessionHarp
	}
	if !safePathSegment(s.Harp) {
		return "", fmt.Errorf("%w: %q", errUnsafeSessionHarp, s.Harp)
	}
	dir, err := paths.HarpEphemeralDir(s.Harp)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// sessionStateMounts builds the scoped read-write state mounts that keep a
// containerized run's ctxloom-stateful writes durable across teardown. The
// container gets a fresh HOME, so without these every engine transcript,
// in-container ~/.ctxloom write, and session artifact dies with it. Each mount
// is scoped to exactly one concern:
//
//	~/.ctxloom/sessions/<harp>/persist/transcripts → the engine's native
//	    transcript STORE ROOT in the container home (per-backend
//	    transcriptStoreRel). The transcript leaf name is runtime-generated,
//	    so the ROOT is the bind target; the container's fresh HOME means the
//	    root holds only this run's transcript. Location under the harp dir is
//	    what makes the transcript harp-addressable when the SessionStart bind
//	    hook never fires (sessions.LocateTranscript).
//	~/.ctxloom/sessions/<harp>/<dir>, for each dir paths.MountedLocations
//	    names → the same path relative to the CONTAINER home. The table
//	    (paths.HarpMembers) decides what a container reaches: the spool row
//	    is Mounted, so persist/ rides here and container mail with it, and
//	    everything else under persist/ (the claim store, the canonical
//	    transcript, in-container artifact writes) lands on the host through
//	    the same mount.
//	~/.ctxloom/tasks/<project-id>.jsonl (and its .lock sidecar) → the same
//	    path under the container home, so an in-container taskloom's
//	    task_add/deferral reports reach the one host log every session of THIS
//	    project shares. Two single FILES, not the tasks dir: that dir holds
//	    every project on the machine, and a run keyed to one project id has no
//	    business reading — let alone appending to — another project's task
//	    log. Which project it is comes from the CTXLOOM_PROJECT_ID the run env
//	    pins.
//
// The container's ~/.ctxloom/locks is NOT among these: see
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
// or unsafe harp, or a preparation FAILURE for a known identity, errors so the
// caller's degrade chain raises the fatal ClassIsolation finding.
func (c Container) sessionStateMounts() ([]mount, error) {
	if !safePathSegment(c.state.Harp) {
		return nil, fmt.Errorf("container session-state mounts: session harp %q is not a safe path segment", c.state.Harp)
	}
	mounts, err := c.harpStateMounts()
	if err != nil {
		return nil, err
	}
	taskMounts, err := c.taskStoreMounts()
	if err != nil {
		return nil, err
	}
	return append(mounts, taskMounts...), nil
}

// harpStateMounts binds the session's native transcript store (when the
// engine spec names one) and each of its mounted locations, creating every
// bind source first.
func (c Container) harpStateMounts() ([]mount, error) {
	var mounts []mount
	layout, err := sessions.HomeLayout()
	if err != nil {
		return nil, fmt.Errorf("container session-state mounts: %w", err)
	}
	store, err := paths.HarpTranscriptStoreDir(c.state.Harp)
	if err != nil {
		return nil, fmt.Errorf("container session-state mounts: %w", err)
	}
	// The bind SOURCE must exist before `run`.
	if err := os.MkdirAll(store, 0o755); err != nil {
		return nil, fmt.Errorf("container session-state mounts: %w", err)
	}
	// transcriptStoreRel is set by every engineContainerSpecFor branch; ""
	// only reaches here through a hand-built spec, which then simply
	// has no native store to persist.
	if c.engineSpec.transcriptStoreRel != "" {
		mounts = append(mounts, c.runtime.paths().bind(
			store,
			path.Join(c.home, c.engineSpec.transcriptStoreRel),
			false,
		))
	}
	for _, dir := range paths.MountedLocations() {
		host := filepath.Join(layout.Dir(c.state.Harp), dir)
		if err := os.MkdirAll(host, 0o755); err != nil {
			return nil, fmt.Errorf("container session-state mounts: %w", err)
		}
		mounts = append(mounts, c.runtime.paths().bind(
			host,
			path.Join(c.home, paths.AppDirName, paths.SessionsDir, c.state.Harp, dir),
			false,
		))
	}
	return mounts, nil
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
			mounts = append(mounts, c.runtime.paths().bind(
				src,
				path.Join(c.home, taskpaths.AppDirName, taskpaths.TasksDir, filepath.Base(src)),
				false,
			))
		}
	}
	return mounts, nil
}

// lockMounts gives the container its OWN ~/.ctxloom/locks — a per-run dir
// under scratchRoot, removed with it — never the host's. The host's locks dir
// guards host-only state (the countersign trust index, every session's files),
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
	seam := c.runtime.paths()
	runLocks := filepath.Join(scratchRoot, paths.HomeLocksDirName)
	if err := os.MkdirAll(runLocks, 0o755); err != nil {
		return nil, fmt.Errorf("container lock mounts: %w", err)
	}
	containerLocks := path.Join(c.home, paths.AppDirName, paths.HomeLocksDirName)
	mounts := []mount{seam.bind(runLocks, containerLocks, false)}
	for _, rel := range c.engineSpec.inPlaceFiles {
		protected := filepath.Join(dir, rel)
		hostLock, err := paths.HomePathFor(protected)
		if err != nil {
			return nil, fmt.Errorf("container lock mounts: %w", err)
		}
		inContainer, err := seam.targetFor(protected)
		if err != nil {
			return nil, fmt.Errorf("container lock mounts: %s has no route into the container: %w", protected, err)
		}
		name := paths.HomeLockName(inContainer)
		// Both must exist as FILES before `run`: the bind source, or the
		// runtime creates a directory in its place; and the target inside the
		// per-run dir, or a rootful runtime creates it there as root.
		for _, f := range []string{hostLock, filepath.Join(runLocks, name)} {
			if err := filelock.Prepare(f); err != nil {
				return nil, fmt.Errorf("container lock mounts: %w", err)
			}
		}
		mounts = append(mounts, seam.bind(hostLock, path.Join(containerLocks, name), false))
	}
	return mounts, nil
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
