package operations

import (
	"fmt"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
)

// doctorProjectOwnerMarker is the DOCTOR-CHECK-* vocabulary entry for this
// project's coordinator roots and who owns each.
const doctorProjectOwnerMarker = "DOCTOR-CHECK-PROJECT-OWNER-v4"

// doctorCheckProjectOwner lists this project's coordinator roots — one per
// independent tree a session founded — and who owns each. A new `ctxloom run`
// is never refused by any of them: it founds a root of its own. What the row
// is FOR is the root nobody will end by running — an owner whose terminal
// died, or a root its owner left with runs not ended — which only a resume
// (`ctxloom run --session <root>`) or the session reaper ends. Doctor writes
// nothing and removes no root, and says so: it has no repair mode.
//
// The roots are keyed by the same project identity the coordinator host keys
// them by, and list reads ownership without claiming (coord.ListRoots in
// production).
func doctorCheckProjectOwner(workDir string, list func(projectID, projectDir string) ([]coord.RootStatus, error)) DoctorCheck {
	const marker = doctorProjectOwnerMarker
	if workDir == "" {
		return DoctorCheck{Marker: marker, Status: DoctorInfo, Detail: "no project directory to check"}
	}
	// Read the identity, never resolve it: resolution mints a marker on first
	// sight, and doctor writes nothing. Every `ctxloom run` resolves (and so
	// marks) its project before it claims a root; no marker means no run here
	// ever resolved one, and the host's own fallback is the path-derived key.
	id, err := projectid.ReadMarker(workDir)
	if err != nil {
		id = ""
	}
	roots, lerr := list(id, workDir)
	if lerr != nil && len(roots) == 0 {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "could not list the coordinator roots: " + lerr.Error()}
	}
	if len(roots) == 0 {
		return DoctorCheck{Marker: marker, Status: DoctorOK, Detail: "no coordinator roots: the next `ctxloom run` here founds its own"}
	}
	status, detail := summarizeRoots(roots)
	if lerr != nil {
		status = DoctorWarn
		detail += ". Some roots could not probe: " + lerr.Error()
	}
	return DoctorCheck{Marker: marker, Status: status, Detail: detail}
}

// summarizeRoots renders a non-empty root list as the check's status and
// detail: a warning when any root is stranded (describeRoot), with what ends
// one, since doctor will not.
func summarizeRoots(roots []coord.RootStatus) (DoctorStatus, string) {
	lines := make([]string, 0, len(roots))
	stranded := false
	for _, r := range roots {
		line, needsHand := describeRoot(r)
		lines = append(lines, line)
		stranded = stranded || needsHand
	}
	noun := "roots"
	if len(roots) == 1 {
		noun = "root"
	}
	detail := fmt.Sprintf("%d coordinator %s: %s. A new `ctxloom run` here founds its own root alongside them", len(roots), noun, strings.Join(lines, "; "))
	if !stranded {
		return DoctorInfo, detail
	}
	return DoctorWarn, detail + ". doctor removes no root: a resume adopts a stranded one (ending an orphaned owner first), and `ctxloom session sweep` removes it with its session"
}

// describeRoot renders one root, and reports whether it is stranded: no live
// process will end it on its own.
func describeRoot(r coord.RootStatus) (string, bool) {
	st := r.Owner
	if !st.Held {
		return fmt.Sprintf("%s unowned — its owner exited with runs not ended; `ctxloom run --session %s` adopts them", r.RootHarp, r.RootHarp), true
	}
	owner := "an unidentified session"
	if st.PID != 0 {
		owner = fmt.Sprintf("session %s (pid %d, %s, started %s)", st.Harp, st.PID, st.Mode, st.Started.Local().Format(time.RFC3339))
	}
	if st.Orphan {
		return fmt.Sprintf("%s orphaned — owned by %s: %s; `ctxloom run --session %s` ends it and adopts the root", r.RootHarp, owner, st.Reason, r.RootHarp), true
	}
	return fmt.Sprintf("%s live — owned by %s", r.RootHarp, owner), false
}
