package isolation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
	taskpaths "github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
)

// SessionState is the run's session identity threaded into the isolation seam:
// which harp names this run's per-session state dir, and which stable project
// id keys the shared task log. It decides the SCOPED read-write state mounts a
// containerized run gets (sessionStateMounts) and where a worktree's ephemeral
// scratch lands (Worktree.scratchBase). Zero fields degrade per facet — an
// ensemble member without per-member session accounting simply has no
// per-session state to persist. Never a blanket ~/.ctxloom mount: that would
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
		Harp:      env["CTXLOOM_SESSION_HARP"],
		ProjectID: env["CTXLOOM_PROJECT_ID"],
	}
}

// noHarpNotice and noProjectIDNotice are the two durability degrades a run
// without session identity reports. They are WarnOnce lines: a delegated
// fan-out (agent_run) puts every member through sessionStateMounts in ONE
// process, so N identical lines would be startup spam. The cost of that
// collapse is that the surviving line cannot name WHICH members it covers —
// there is no identity available here to name them with, since the missing
// harp IS that identity — so each says plainly that it speaks for every
// affected run rather than reading as one run's notice.
const (
	noHarpNotice      = "container runs with no session harp: their engine transcripts and session artifacts will not survive the container — reported once for every affected run in this session"
	noProjectIDNotice = "container runs with no project id: their in-container task writes will not reach the shared task log — reported once for every affected run in this session"
)

// safePathSegment reports whether s can be trusted as a single path segment
// under ~/.ctxloom/sessions. Harps are normally minted by AssignSession, but
// they arrive HERE from an env map — the same untrusted channel the task
// store validates project ids from — and this is the point where the value
// becomes both a host path and a bind-mount source.
func safePathSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\")
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
//	~/.ctxloom/sessions/<harp>/persist → the same path relative to the
//	    CONTAINER home, so in-container hooks/MCP writing session-scoped
//	    artifacts land them on the host.
//	~/.ctxloom/tasks/<project-id>.jsonl (and its .lock sidecar) → the same
//	    path under the container home, so an in-container taskloom's
//	    task_add/deferral reports reach the one host log every session of THIS
//	    project shares. Two single FILES, not the tasks dir: that dir holds
//	    every project on the machine, and a run keyed to one project id has no
//	    business reading — let alone appending to — another project's task
//	    log. Which project it is comes from the CTXLOOM_PROJECT_ID the run env
//	    pins.
//
// VM-FS append hazard on the log: host and container both APPEND to it. On
// native Linux a bind mount is the same inode, so O_APPEND keeps concurrent
// appends atomic; on Docker Desktop's VM filesystems (gRPC-FUSE/9p) that
// atomicity is NOT guaranteed and interleaved appends can tear. The
// single-writer decision is parked on the macOS runbook (sudsy-sip).
//
// All of them are RW host state and so ride the container identity contract
// (entrypoint PUID/PGID remap): the in-container writer must be the host user
// or these dirs collect wrongly-owned files. A missing harp/project id skips
// the facet with a streamed warning — NOT a strictness finding, because
// ensemble members legitimately run without per-member session accounting
// today and there is no user-side fix-it; a preparation FAILURE for a known
// identity, by contrast, errors so the caller's degrade chain raises the
// fatal-unless-degraded ClassIsolation finding.
func (c Container) sessionStateMounts() ([]Mount, error) {
	var mounts []Mount
	switch {
	case c.state.Harp == "":
		clidiag.WarnOnce("ctxloom", "%s", noHarpNotice)
	case !safePathSegment(c.state.Harp):
		return nil, fmt.Errorf("container session-state mounts: session harp %q is not a safe path segment", c.state.Harp)
	default:
		persist, err := paths.HarpPersistDir(c.state.Harp)
		if err != nil {
			return nil, fmt.Errorf("container session-state mounts: %w", err)
		}
		store, err := paths.HarpTranscriptStoreDir(c.state.Harp)
		if err != nil {
			return nil, fmt.Errorf("container session-state mounts: %w", err)
		}
		// Creates persist/ too; the bind SOURCE must exist before `run`.
		if err := os.MkdirAll(store, 0o755); err != nil {
			return nil, fmt.Errorf("container session-state mounts: %w", err)
		}
		// transcriptStoreRel is set by every engineContainerSpecFor branch; ""
		// only reaches here through a hand-built spec, which then simply
		// has no native store to persist.
		if c.engineSpec.transcriptStoreRel != "" {
			mounts = append(mounts, c.runtime.Expose(
				store,
				filepath.Join(c.home, c.engineSpec.transcriptStoreRel),
				false,
			))
		}
		mounts = append(mounts, c.runtime.Expose(
			persist,
			filepath.Join(c.home, paths.AppDirName, paths.SessionsDir, c.state.Harp, paths.PersistDirName),
			false,
		))
	}
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
		// protocol: every writer of this log takes filelock.PathFor(log)
		// first, and a lock file the container cannot see is a lock that
		// excludes nothing across the boundary.
		//
		// Both sources must exist, as FILES, before `run`: a runtime asked to
		// bind a source that is not there creates a DIRECTORY in its place,
		// and neither the log nor the lock survives that.
		for _, src := range []string{logPath, filelock.PathFor(logPath)} {
			if err := ensureFile(src); err != nil {
				return nil, fmt.Errorf("container task-store mount: %w", err)
			}
			mounts = append(mounts, c.runtime.Expose(
				src,
				filepath.Join(c.home, taskpaths.AppDirName, taskpaths.TasksDir, filepath.Base(src)),
				false,
			))
		}
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
