package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// doctorLegacyIndexMarker is the DOCTOR-CHECK-* vocabulary entry for a
// pre-rename session index left at the sessions root — see
// doctorCheckLegacyIndex.
const doctorLegacyIndexMarker = "DOCTOR-CHECK-LEGACY-INDEX-y5"

// doctorLegacyIndexRemedy is the one action on a leftover index. The sidecar
// under each harp directory (paths.SessionSidecarFileName) is the record;
// nothing reads the index, so nothing is lost by removing it.
const doctorLegacyIndexRemedy = "delete it; the sidecars are the record"

// doctorLegacyIndexNames are the sessions-root leaves a retired binary left
// behind: the index itself and the marker its own migration wrote beside a
// consumed one. The sessions store ignores both (internal/core/sessions pins
// that); this check is the surface that SAYS so, because a file that is
// silently ignored looks, to a reader of the directory, like a file that is
// still authoritative — and an older binary still on PATH keeps appending to
// it, which is how a stale index comes to carry rows newer than the tree.
var doctorLegacyIndexNames = []string{paths.IndexFileName, paths.MigratedIndexFileName}

// doctorCheckLegacyIndex names a pre-rename session index (or the marker an
// older migration left) at the sessions root, with the remedy. It reads the
// root's own top level only — the harp directories beside these files are
// the record and are not this check's subject — and it deletes nothing.
func doctorCheckLegacyIndex() doctorCheck {
	sessionsRoot, err := paths.HomeSessionsDir()
	if err != nil {
		return doctorCheck{Marker: doctorLegacyIndexMarker, Status: doctorWarn,
			Detail: "cannot resolve sessions dir: " + err.Error()}
	}
	var found []string
	for _, name := range doctorLegacyIndexNames {
		p := filepath.Join(sessionsRoot, name)
		switch _, statErr := os.Lstat(p); {
		case statErr == nil:
			found = append(found, p)
		case os.IsNotExist(statErr):
		default:
			return doctorCheck{Marker: doctorLegacyIndexMarker, Status: doctorWarn,
				Detail: fmt.Sprintf("cannot stat %s: %v", p, statErr)}
		}
	}
	if len(found) == 0 {
		return doctorCheck{Marker: doctorLegacyIndexMarker, Status: doctorOK,
			Detail: "no pre-rename session index at " + sessionsRoot}
	}
	return doctorCheck{Marker: doctorLegacyIndexMarker, Status: doctorWarn,
		Detail: fmt.Sprintf("%d pre-rename session index file(s) at the sessions root, which nothing reads: %s — %s",
			len(found), strings.Join(found, ", "), doctorLegacyIndexRemedy)}
}
