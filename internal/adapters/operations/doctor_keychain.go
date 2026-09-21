package operations

import (
	"fmt"
	"os"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
)

// doctorKeychainOrphansMarker is the DOCTOR-CHECK-* vocabulary entry for
// macOS Keychain credential items no session explains.
const doctorKeychainOrphansMarker = "DOCTOR-CHECK-KEYCHAIN-ORPHANS-x4"

// doctorKeychainOrphansMaxNamed caps how many orphans the detail names
// individually — the "cap at ~5 with a count" shape the other listing
// checks in this package use.
const doctorKeychainOrphansMaxNamed = 5

// doctorCheckKeychainOrphans lists the macOS Keychain credential items whose
// service carries a session-item prefix but whose hash matches no session
// home under ~/.ctxloom/sessions (isolation.OrphanedKeychainItems): the
// items a crashed run never deleted at teardown and the reaper has not yet
// reached. Each is named with the `security` command that removes it, and
// the reap that removes them all. Off darwin the lister reports nothing and
// the check is OK by construction.
//
// READ-ONLY: this lists; it deletes nothing. The reap is the one clock that
// deletes (sessions.Reap, through sessionTriage).
func doctorCheckKeychainOrphans(list func() ([]string, error), account string) DoctorCheck {
	orphans, err := list()
	if err != nil {
		return DoctorCheck{Marker: doctorKeychainOrphansMarker, Status: DoctorWarn,
			Detail: "cannot list Keychain credential items: " + err.Error()}
	}
	if len(orphans) == 0 {
		return DoctorCheck{Marker: doctorKeychainOrphansMarker, Status: DoctorOK,
			Detail: "no Keychain credential item is left over from a session that no longer exists"}
	}
	shown := orphans
	var more int
	if len(shown) > doctorKeychainOrphansMaxNamed {
		shown = shown[:doctorKeychainOrphansMaxNamed]
		more = len(orphans) - doctorKeychainOrphansMaxNamed
	}
	cmds := make([]string, 0, len(shown))
	for _, s := range shown {
		cmds = append(cmds, fmt.Sprintf("security delete-generic-password -a %s -s %q", account, s))
	}
	list1 := strings.Join(cmds, "; ")
	if more > 0 {
		list1 += fmt.Sprintf("; … +%d more", more)
	}
	return DoctorCheck{Marker: doctorKeychainOrphansMarker, Status: DoctorWarn, Detail: fmt.Sprintf(
		"%d Keychain credential item(s) belong to sessions that no longer exist — a run seeded them and nothing deleted them: %s — or let `ctxloom session reap` take them with the next aged sweep",
		len(orphans), list1)}
}

// doctorKeychainAccount is the account the items were written under: the
// CLI's own $USER rule (isolation.KeychainAccount).
func doctorKeychainAccount() string {
	return isolation.KeychainAccount(os.Getenv("USER"))
}
