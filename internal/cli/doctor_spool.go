package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// doctorSpoolBacklogMarker is the DOCTOR-CHECK-* vocabulary entry for stuck
// spool entries — see doctorCheckSpoolBacklog's doc for the incident this
// exists to make visible.
const doctorSpoolBacklogMarker = "DOCTOR-CHECK-SPOOL-BACKLOG-t0"

// doctorSpoolStuckAge is how long a message may sit UNCONSUMED in a spool's
// live in/ or out/ directory before this check calls it stuck rather than
// merely slow.
//
// It is five times spoolSweepInterval (internal/agentcoord/coord/
// spooldelivery.go: 30s, the slow reconciliation cadence on both sides) —
// generous enough that a startup sweep, a reconnect sweep and a couple of
// missed periodic ticks all still have room to catch up before this fires,
// so a merely-busy, healthy coordinator never trips it. The number is
// duplicated here rather than imported: coord's constant is unexported by
// design (spoolSweepInterval's own doc: "a tunable constant rather than
// config surface"), and this check reads only the filesystem substrate coord
// itself reads — never coord's in-process state — so it has no other way to
// learn the cadence. If that constant ever changes materially, this
// threshold should be revisited alongside it.
const doctorSpoolStuckAge = 5 * 30 * time.Second

// doctorSpoolStuckMaxNamed caps how many entries doctorCheckSpoolBacklog
// names individually — for stuck entries AND for malformed ones — before
// summarizing the rest as a count. Same "cap at ~5 with a count" shape
// doctorCheckHarpDurability and the other listing checks in this package
// use, shared across both lists rather than duplicated per-list.
const doctorSpoolStuckMaxNamed = 5

// doctorCheckSpoolBacklog surfaces spool entries that have sat UNCONSUMED in
// a session's live in/ or out/ directory well past the sweep's own
// reconciliation cadence (spooldelivery.go's header has the full delivery
// model: consumption is a rename into consumed/, and that rename IS the
// acknowledgement).
//
// This exists because four confirmed message losses were previously
// invisible: a report a child definitely filed never reached the
// coordinator, and nothing on this machine noticed — no gate, no warning, no
// log line. A human found it by reading the filesystem by hand, months of
// runs later. This check is that filesystem read, run every time doctor is.
//
// It is READ-ONLY with respect to the spool: it lists directories and parses
// filenames (spool.Sweep), it renames nothing, deletes nothing, and delivers
// nothing. Recovering a stuck message is deliberately out of scope — under
// today's single-coordinator deployment, prior message loss is an accepted
// risk, and this check's whole job is making the state visible, never fixing
// it.
//
// It does NOT attempt to detect a message that was renamed into consumed/
// without the delivery it names ever having happened — that state is not
// distinguishable from a genuine delivery by reading the spool alone (the
// rename is the only record either way), which is exactly the ordering gap a
// sibling fix restores going forward. What this check CAN see, and does, is
// the complementary symptom: a message still sitting in in/ or out/,
// unconsumed, long after every sweep path should have picked it up — a
// sweep that stopped running, or a doorbell miss with no periodic tick
// behind it.
//
// It ALSO surfaces spool.Sweep's Problems: directory entries that are not
// stuck-but-valid messages at all — a filename outside the
// "<unixnano>.<seq>.<writer>.md" grammar, or a file whose content would not
// parse (spool.Parse). Sweep already computes and returns these; nothing
// consumed them before this check existed, so a corrupt or hand-edited spool
// file passed every check silently. This is deliberately a DIFFERENT finding
// from "stuck": a malformed entry is never a candidate for eventual, delayed
// delivery — no amount of waiting turns an unparseable filename into a
// message — and it is also different from a validly-parsed message whose
// Kind a mailbox does not recognize (that classification lives in
// coord/spooldelivery.go and coord/messagekind.go, sibling territory this
// check does not touch). The wording below keeps the three apart so a
// reader knows which one they have.
//
// Distinguishable outcomes, all doctorOK when nothing is wrong, worded
// differently on purpose (this project's characteristic defect is a success
// message over zero bytes examined, and this check exists specifically to
// not be another instance of it):
//   - the sessions directory itself does not exist yet: nothing has ever run.
//   - it exists, but no session in it ever turned spool delivery on: nothing
//     to check.
//   - one or more sessions have a spool, all of it was swept, and nothing was
//     found stuck or malformed: the state actually observed.
func doctorCheckSpoolBacklog() doctorCheck {
	sessionsRoot, err := paths.HomeSessionsDir()
	if err != nil {
		return doctorCheck{Marker: doctorSpoolBacklogMarker, Status: doctorWarn,
			Detail: "cannot resolve sessions dir: " + err.Error()}
	}
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return doctorCheck{Marker: doctorSpoolBacklogMarker, Status: doctorOK,
				Detail: "no session directories yet; no spool to check"}
		}
		return doctorCheck{Marker: doctorSpoolBacklogMarker, Status: doctorWarn,
			Detail: "cannot read sessions dir: " + err.Error()}
	}

	mapper := spool.NewHomeMapper()
	now := time.Now()
	spoolsFound := 0
	var stuck []string
	var sweepErrs []string
	var malformed []string
	var oldest time.Duration

	for _, e := range entries {
		if !e.IsDir() {
			continue // e.g. index.yaml, sitting beside the harp dirs
		}
		harp := e.Name()
		root, err := spool.Root(mapper, harp)
		if err != nil {
			continue // not a syntactically valid harp id; not this check's job
		}
		if _, statErr := os.Stat(root); statErr != nil {
			continue // this session never turned spool delivery on: no spool root at all
		}
		spoolsFound++
		for _, dir := range []spool.Dir{spool.DirIn, spool.DirOut} {
			res, sweepErr := spool.Sweep(mapper, harp, dir)
			if sweepErr != nil {
				sweepErrs = append(sweepErrs, fmt.Sprintf("%s/%s: %v", harp, dir, sweepErr))
				continue
			}
			for _, entry := range res.Entries {
				age := now.Sub(time.Unix(0, entry.Name.Nanos))
				if age < doctorSpoolStuckAge {
					continue
				}
				if age > oldest {
					oldest = age
				}
				stuck = append(stuck, fmt.Sprintf("%s (%s old)", entry.Ref, age.Round(time.Second)))
			}
			for _, prob := range res.Problems {
				malformed = append(malformed, fmt.Sprintf("%s:%s/%s: %v",
					harp, dir, filepath.Base(prob.Path), prob.Err))
			}
		}
	}

	if spoolsFound == 0 {
		return doctorCheck{Marker: doctorSpoolBacklogMarker, Status: doctorOK,
			Detail: "no session has a spool directory; nothing to check"}
	}
	if len(stuck) == 0 && len(sweepErrs) == 0 && len(malformed) == 0 {
		return doctorCheck{Marker: doctorSpoolBacklogMarker, Status: doctorOK, Detail: fmt.Sprintf(
			"%d session spool(s) checked, 0 entries stuck unconsumed past %s, 0 malformed entries",
			spoolsFound, doctorSpoolStuckAge)}
	}

	var parts []string
	if len(stuck) > 0 {
		sort.Strings(stuck)
		shown := stuck
		var more int
		if len(shown) > doctorSpoolStuckMaxNamed {
			shown = shown[:doctorSpoolStuckMaxNamed]
			more = len(stuck) - doctorSpoolStuckMaxNamed
		}
		list := strings.Join(shown, ", ")
		if more > 0 {
			list += fmt.Sprintf(", … +%d more", more)
		}
		parts = append(parts, fmt.Sprintf(
			"%d spool entr(ies) sat unconsumed past %s (oldest %s): %s — a report or an instruction may not have been delivered",
			len(stuck), doctorSpoolStuckAge, oldest.Round(time.Second), list))
	}
	if len(malformed) > 0 {
		sort.Strings(malformed)
		shown := malformed
		var more int
		if len(shown) > doctorSpoolStuckMaxNamed {
			shown = shown[:doctorSpoolStuckMaxNamed]
			more = len(malformed) - doctorSpoolStuckMaxNamed
		}
		list := strings.Join(shown, ", ")
		if more > 0 {
			list += fmt.Sprintf(", … +%d more", more)
		}
		parts = append(parts, fmt.Sprintf(
			"%d spool entr(ies) are malformed (filename does not parse, or content is unreadable/invalid — this is NOT a stuck-but-valid entry, and NOT a recognized message with an unmappable kind): %s",
			len(malformed), list))
	}
	if len(sweepErrs) > 0 {
		parts = append(parts, fmt.Sprintf("%d spool director(ies) could not be swept: %s",
			len(sweepErrs), strings.Join(sweepErrs, "; ")))
	}
	return doctorCheck{Marker: doctorSpoolBacklogMarker, Status: doctorWarn, Detail: strings.Join(parts, "; ")}
}
