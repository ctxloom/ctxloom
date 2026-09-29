package operations

import (
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
)

// doctorProjectOwnerMarker is the DOCTOR-CHECK-* vocabulary entry for who owns
// this project's coordinator.
const doctorProjectOwnerMarker = "DOCTOR-CHECK-PROJECT-OWNER-v4"

// doctorCheckProjectOwner reports who owns this project's coordinator and what
// the next `ctxloom run` here will do about it. An owned project refuses every
// other run, so an owner nobody can see — a session whose terminal died — is
// a project nobody can use; this row is where it becomes visible. The state
// dir is keyed by the same project identity the coordinator host keys it
// by, and probe reads ownership without claiming it (coord.ProbeOwner in
// production).
func doctorCheckProjectOwner(workDir string, probe func(dir string) (coord.OwnerStatus, error)) DoctorCheck {
	const marker = doctorProjectOwnerMarker
	if workDir == "" {
		return DoctorCheck{Marker: marker, Status: DoctorInfo, Detail: "no project directory to check"}
	}
	// Read the identity, never resolve it: resolution mints a marker on first
	// sight, and doctor writes nothing. Every `ctxloom run` resolves (and so
	// marks) its project before it claims it; no marker means no run here
	// ever resolved one, and the host's own fallback is the path-derived key.
	id, err := projectid.ReadMarker(workDir)
	if err != nil {
		id = ""
	}
	dir, err := coord.ProjectStateDir(id, workDir)
	if err != nil {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "could not resolve the coordinator state dir: " + err.Error()}
	}
	st, err := probe(dir)
	if err != nil {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "could not probe the project owner: " + err.Error()}
	}
	if !st.Held {
		return DoctorCheck{Marker: marker, Status: DoctorOK, Detail: "unowned: the next `ctxloom run` here claims the project"}
	}
	owner := "an unidentified session"
	if st.PID != 0 {
		owner = fmt.Sprintf("session %s (pid %d, %s, started %s)", st.Harp, st.PID, st.Mode, st.Started.Local().Format(time.RFC3339))
	}
	if st.Orphan {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: fmt.Sprintf(
			"owned by %s; orphan: yes — %s. The next `ctxloom run` here ends it (SIGTERM, then SIGKILL) and claims the project", owner, st.Reason)}
	}
	return DoctorCheck{Marker: marker, Status: DoctorInfo, Detail: fmt.Sprintf(
		"owned by %s; orphan: no — %s. The next `ctxloom run` here is refused while it owns the project; end that session, or pass --degraded to run without agent delegation", owner, st.Reason)}
}
