package isolation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
// in-container ~/.ctxloom write, and session artifact dies with it. Three
// mounts, each scoped to exactly one concern:
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
//	~/.ctxloom/tasks → the project-SHARED task-log dir, so an in-container
//	    taskloom's task_add/deferral reports reach the one host log every
//	    session shares (keyed by the CTXLOOM_PROJECT_ID the run env pins).
//
// All three are RW host state and so ride the container identity contract
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
		clidiag.WarnOnce("ctxloom", "container run has no session harp; the engine transcript and session artifacts will not survive the container")
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
		// transcriptStoreRel is set by every containerProfileFor branch; ""
		// only reaches here through a hand-built profile, which then simply
		// has no native store to persist.
		if c.profile.transcriptStoreRel != "" {
			mounts = append(mounts, Mount{
				Host:      store,
				Container: filepath.Join(c.home, c.profile.transcriptStoreRel),
			})
		}
		mounts = append(mounts, Mount{
			Host:      persist,
			Container: filepath.Join(c.home, paths.AppDirName, paths.SessionsDir, c.state.Harp, paths.PersistDirName),
		})
	}
	if c.state.ProjectID == "" {
		// Without a pinned project id the in-container taskloom would MINT a
		// fresh one and write a wrongly-keyed log; better that write dies with
		// the container than pollutes the shared host store.
		clidiag.WarnOnce("ctxloom", "container run has no project id; in-container task writes will not reach the shared task log")
	} else {
		tasksDir, err := taskpaths.HomeTasksDir()
		if err != nil {
			return nil, fmt.Errorf("container task-store mount: %w", err)
		}
		if err := os.MkdirAll(tasksDir, 0o755); err != nil {
			return nil, fmt.Errorf("container task-store mount: %w", err)
		}
		// VM-FS append hazard: host and container both APPEND to the shared
		// per-project JSONL under this mount. On native Linux a bind mount is
		// the same inode, so O_APPEND keeps concurrent appends atomic; on
		// Docker Desktop's VM filesystems (gRPC-FUSE/9p) that atomicity is NOT
		// guaranteed and interleaved appends can tear. Deliberately no locking
		// here — the single-writer/lock decision is parked on the macOS
		// runbook (sudsy-sip).
		mounts = append(mounts, Mount{
			Host:      tasksDir,
			Container: filepath.Join(c.home, taskpaths.AppDirName, taskpaths.TasksDir),
		})
	}
	return mounts, nil
}
