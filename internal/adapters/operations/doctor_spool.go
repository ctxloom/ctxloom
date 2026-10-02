package operations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/discover"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// doctorSpoolBacklogMarker is the DOCTOR-CHECK-* vocabulary entry for stuck
// spool entries — see doctorCheckSpoolBacklog's doc for the incident this
// exists to make visible.
const doctorSpoolBacklogMarker = "DOCTOR-CHECK-SPOOL-BACKLOG-t0"

// doctorSpoolStuckAge is how long a message may sit UNDELIVERED in a spool's
// live in/ or out/ directory before this check calls it stuck rather than
// merely slow.
//
// It is five times spoolSweepInterval (internal/core/coord/
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
// names individually — for stuck entries, for malformed ones, AND for
// entries refused into in/failed/ — before summarizing the rest as a count.
// Same "cap at ~5 with a count" shape doctorCheckHarpDurability and the
// other listing checks in this package use, shared across all three lists
// rather than duplicated per-list.
const doctorSpoolStuckMaxNamed = 5

// doctorCheckSpoolBacklog surfaces spool entries that have sat UNDELIVERED in
// a session's live in/, in/claimed/ or out/ directory well past the sweep's own
// reconciliation cadence (spooldelivery.go's header has the full delivery
// model: an out/ message is acknowledged by a rename into out/consumed/, an
// inbox message by spool.Deliver, which records its identity and deletes it).
// An inbox file whose identity is already recorded is a delivery whose delete
// was interrupted, not a stuck one, and is not named.
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
// It does NOT attempt to detect a message recorded as delivered without the
// delivery it names ever having happened — that state is not distinguishable
// from a genuine delivery by reading the spool alone (the record is the only
// trace either way). What this check CAN see, and does, is the complementary
// symptom: a message still sitting in in/ or out/, undelivered, long after
// every sweep path should have picked it up — a sweep that stopped running,
// or a doorbell miss with no periodic tick behind it.
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
// It ALSO surfaces in/failed/ AND out/failed/: the terminal directories a reader moves an
// entry into when it parsed as a message but could not be classified,
// delivered or routed (spool.Fail; coord/spooldelivery.go's failSpoolEntry and
// failSpoolOut are its callers). That move is precisely what closed the visibility gap this
// check used to cover by accident: before spool.Fail existed, an entry the
// reader could not handle stayed in in/ and eventually aged past
// doctorSpoolStuckAge, so this check caught it as "stuck" without ever being
// told about the refusal. Now the entry leaves in/ within one sweep cycle —
// almost always well under doctorSpoolStuckAge — so it never trips the stuck
// check at all, and nothing else looked at in/failed/. This clause is that
// look. in/failed/ is deliberately NOT one of spool.Dirs()'s closed set (see
// FailedDirName's doc); it is created lazily, so this check reads it directly
// with os.ReadDir — absence is a normal state, not a sweep failure — and never
// renames or deletes what it finds. A
// failed entry is worded a fourth, distinct way from the other three: it did
// not "sit unconsumed" (it was actively rejected), it is not "malformed"
// (the file parsed fine as a message), and it is not a sweep I/O error (the
// directory itself may not even exist) — it is a message ctxloom was GIVEN
// and REFUSED to deliver, permanently.
//
// Distinguishable outcomes, all DoctorOK when nothing is wrong, worded
// differently on purpose (this project's characteristic defect is a success
// message over zero bytes examined, and this check exists specifically to
// not be another instance of it):
//   - the sessions directory itself does not exist yet: nothing has ever run.
//   - it exists, but no session in it ever turned spool delivery on: nothing
//     to check.
//   - one or more sessions have a spool, all of it was swept, and nothing was
//     found stuck or malformed: the state actually observed.
//   - within that: no session has ever created an in/failed/ directory
//     (the common case — in/failed/ is created lazily, on the first refusal,
//     so its absence is normal and must not read as an error) versus one or
//     more in/failed/ directories exist and were read, and were empty (a
//     rarer but equally clean state) — kept as two different sentences so
//     neither is mistaken for the other, and so an existing-but-empty
//     in/failed/ cannot be confused with "we never looked."
func doctorCheckSpoolBacklog() DoctorCheck {
	sessionsRoot, err := paths.HomeSessionsDir()
	if err != nil {
		return DoctorCheck{Marker: doctorSpoolBacklogMarker, Status: DoctorWarn,
			Detail: "cannot resolve sessions dir: " + err.Error()}
	}
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return DoctorCheck{Marker: doctorSpoolBacklogMarker, Status: DoctorOK,
				Detail: "no session directories yet; no spool to check"}
		}
		return DoctorCheck{Marker: doctorSpoolBacklogMarker, Status: DoctorWarn,
			Detail: "cannot read sessions dir: " + err.Error()}
	}

	scan := spoolBacklogScan{mapper: spool.NewHomeMapper(), now: time.Now()}
	for _, e := range entries {
		if !e.IsDir() {
			continue // a lock file or the retired index, sitting beside the harp dirs
		}
		scan.session(e.Name())
	}
	return scan.report(sessionsRoot)
}

// spoolBacklogScan accumulates what doctorCheckSpoolBacklog finds across every
// session's spool; each finding kind is kept in its own slice because each is
// worded distinctly in the report.
type spoolBacklogScan struct {
	mapper         spool.PathMapper
	now            time.Time
	spoolsFound    int
	stuck          []string
	sweepErrs      []string
	malformed      []string
	failed         []string
	failedDirsSeen int
	oldest         time.Duration
}

// session scans one harp directory's spool, if it has one.
func (s *spoolBacklogScan) session(harp string) {
	root, err := spool.Root(s.mapper, harp)
	if err != nil {
		return // not a syntactically valid harp id; not this check's job
	}
	if _, statErr := os.Stat(root); statErr != nil {
		return // this session never turned spool delivery on: no spool root at all
	}
	s.spoolsFound++
	// in/claimed/ is where the owner's turn-start hook holds what it took
	// from in/ until it acknowledges it: an entry aged there is a claim no
	// hook ever finished.
	for _, dir := range []spool.Dir{spool.DirIn, spool.ClaimedDirName, spool.DirOut} {
		s.sweepDir(harp, dir)
	}
	s.failedDirs(harp, root)
}

// sweepDir records the stuck and malformed entries of one spool direction.
func (s *spoolBacklogScan) sweepDir(harp string, dir spool.Dir) {
	res, sweepErr := spool.Sweep(s.mapper, harp, dir)
	if dir == spool.ClaimedDirName && errors.Is(sweepErr, os.ErrNotExist) {
		return // created on the first claim, so its absence is normal
	}
	if sweepErr != nil {
		s.sweepErrs = append(s.sweepErrs, fmt.Sprintf("%s/%s: %v", harp, dir, sweepErr))
		return
	}
	for _, entry := range res.Entries {
		age := s.now.Sub(time.Unix(0, entry.Name.Nanos))
		if age < doctorSpoolStuckAge {
			continue
		}
		if dir != spool.DirOut && s.alreadyDelivered(harp, entry) {
			continue
		}
		if age > s.oldest {
			s.oldest = age
		}
		s.stuck = append(s.stuck, fmt.Sprintf("%s (%s old)", entry.Ref, age.Round(time.Second)))
	}
	for _, prob := range res.Problems {
		s.malformed = append(s.malformed, fmt.Sprintf("%s:%s/%s: %v",
			harp, dir, filepath.Base(prob.Path), prob.Err))
	}
}

// alreadyDelivered reports whether an inbox entry's identity is in the
// spool's delivered record: a delivery whose delete was interrupted, which
// the reader's next sweep finishes. It is not mail anybody is still owed. A
// record that cannot be read is a sweep error, and the entry is still named.
func (s *spoolBacklogScan) alreadyDelivered(harp string, entry spool.Entry) bool {
	delivered, err := spool.Delivered(s.mapper, harp, entry.Identity())
	if err != nil {
		s.sweepErrs = append(s.sweepErrs, fmt.Sprintf("%s: reading the delivered record for %s: %v", harp, entry.Ref, err))
		return false
	}
	return delivered
}

// failedDirs lists one session's failed/ directories. They are created
// lazily, on the first refusal, so a session that never refused a message has
// none — absence is a normal state this check must not report as a failure,
// which is why they are read directly with os.ReadDir rather than swept. List
// only, never rename or delete. BOTH directions are enumerated from
// spool.FailedDirNames rather than named here: a refused outbound report is
// exactly as invisible as a refused inbound one if nothing looks at its
// directory.
func (s *spoolBacklogScan) failedDirs(harp, root string) {
	for _, failedName := range spool.FailedDirNames() {
		failedDir := filepath.Join(root, filepath.FromSlash(string(failedName)))
		failedEntries, failedErr := os.ReadDir(failedDir)
		switch {
		case failedErr == nil:
			s.failedDirsSeen++
			for _, fe := range failedEntries {
				if fe.IsDir() {
					continue
				}
				s.failed = append(s.failed, fmt.Sprintf("%s:%s/%s", harp, failedName, fe.Name()))
			}
		case os.IsNotExist(failedErr):
			// Normal: a failed/ directory is created lazily on the first
			// refusal, so a session that has never refused a message has
			// no such directory at all. Absence must not read as an error.
		default:
			s.sweepErrs = append(s.sweepErrs, fmt.Sprintf("%s/%s: %v", harp, failedName, failedErr))
		}
	}
}

// report renders the accumulated findings as the check's verdict.
func (s *spoolBacklogScan) report(sessionsRoot string) DoctorCheck {
	if s.spoolsFound == 0 {
		// ZERO BYTES EXAMINED IS NOT A PASS. Returning DoctorOK here reported
		// success over nothing looked at — the exact defect this check's own
		// doc says it exists in order not to be. No spool directory under the
		// sessions root is SUSPICIOUS: either no delegated run has happened
		// yet, or this process reads a different home than the coordinator
		// did (a container view, a different HOME). Both are worth saying
		// out loud.
		return DoctorCheck{Marker: doctorSpoolBacklogMarker, Status: DoctorWarn,
			Detail: "NO session has a spool directory under " + sessionsRoot +
				" — nothing was examined. Either no delegated run has happened yet, " +
				"or this command resolves a different home than the coordinator does " +
				"(check HOME and any container view)"}
	}
	if len(s.stuck) == 0 && len(s.sweepErrs) == 0 && len(s.malformed) == 0 && len(s.failed) == 0 {
		return DoctorCheck{Marker: doctorSpoolBacklogMarker, Status: DoctorOK, Detail: s.cleanDetail()}
	}

	var parts []string
	if len(s.stuck) > 0 {
		parts = append(parts, fmt.Sprintf(
			"%d spool entr(ies) sat unconsumed past %s (oldest %s): %s — a report or an instruction may not have been delivered",
			len(s.stuck), doctorSpoolStuckAge, s.oldest.Round(time.Second), doctorNamedList(s.stuck, doctorSpoolStuckMaxNamed)))
	}
	if len(s.malformed) > 0 {
		parts = append(parts, fmt.Sprintf(
			"%d spool entr(ies) are malformed (filename does not parse, or content is unreadable/invalid — this is NOT a stuck-but-valid entry, and NOT a recognized message with an unmappable kind): %s",
			len(s.malformed), doctorNamedList(s.malformed, doctorSpoolStuckMaxNamed)))
	}
	if len(s.failed) > 0 {
		parts = append(parts, fmt.Sprintf(
			"%d spool entr(ies) were REFUSED into in/failed/ (parsed as a message but could not be classified or delivered — not stuck-but-valid, not malformed, not a sweep I/O error: ctxloom was GIVEN this message and REFUSED to deliver it, permanently): %s",
			len(s.failed), doctorNamedList(s.failed, doctorSpoolStuckMaxNamed)))
	}
	if len(s.sweepErrs) > 0 {
		parts = append(parts, fmt.Sprintf("%d spool director(ies) could not be swept: %s",
			len(s.sweepErrs), strings.Join(s.sweepErrs, "; ")))
	}
	return DoctorCheck{Marker: doctorSpoolBacklogMarker, Status: DoctorWarn, Detail: strings.Join(parts, "; ")}
}

// cleanDetail words the all-clear, keeping "no failed/ directory exists" and
// "failed/ directories exist and are empty" as two different sentences.
func (s *spoolBacklogScan) cleanDetail() string {
	detail := fmt.Sprintf(
		"%d session spool(s) checked, 0 entries stuck unconsumed past %s, 0 malformed entries",
		s.spoolsFound, doctorSpoolStuckAge)
	if s.failedDirsSeen == 0 {
		return detail + "; no session has a failed/ directory (in/ or out/; created lazily on the first refusal, so its absence is normal)"
	}
	return detail + fmt.Sprintf("; %d failed/ director(ies) checked, all empty", s.failedDirsSeen)
}

// doctorSpoolCountersMarker is the DOCTOR-CHECK-* vocabulary entry for a live
// coordinator's spool counters — see doctorCheckSpoolCounters.
const doctorSpoolCountersMarker = "DOCTOR-CHECK-SPOOL-COUNTERS-w3"

// doctorCheckSpoolCounters reads the spool counters of every LIVE coordinator
// this user can reach and prints them by name. It is the complement of
// doctorCheckSpoolBacklog: that check reads the filesystem substrate, which
// survives the coordinator; this one reads the coordinator PROCESS, which is
// the only place the counters exist — no journal fact records a failed
// delivery or a rejected doorbell, so once the process exits its tallies are
// gone. The read is ConsumerService.SpoolStats over the loopback endpoint
// each coordinator records in endpoint.json (internal/adapters/coordgrpc/discover),
// presenting the read-only consumer credential the same file carries.
//
// Distinguishable outcomes, worded differently on purpose (a success line
// over zero bytes examined is this project's characteristic defect):
//   - no endpoint.json anywhere: nothing has ever served — INFO, not a pass;
//     there was nothing to ask.
//   - endpoints recorded, none answering: the ordinary state between
//     sessions — endpoint.json is kept after exit so a relaunch re-binds the
//     same port — INFO, naming each endpoint, so a stale file is not read
//     as a live coordinator with clean counters.
//   - one or more answered: the state actually observed. Every counter is
//     printed with its name and value. A non-zero `failed` (a message that
//     has not arrived) or `doorbell_rejected` (a ref that did not parse: a
//     broken or hostile sender, never a race) is the fail-loud WARN. A
//     dropped doorbell costs latency until the next sweep, and unpushed mail
//     waits for its recipient's poll — both are shown, neither warns.
//   - an endpoint.json present but unreadable or undecodable is a real
//     problem discovery reports separately, and is surfaced as WARN rather
//     than folded into "none live".
func doctorCheckSpoolCounters(ctx context.Context) DoctorCheck {
	endpoints, skipped := discover.List()
	var problems []string
	for _, err := range skipped {
		problems = append(problems, err.Error())
	}
	if len(endpoints) == 0 && len(problems) == 0 {
		return DoctorCheck{Marker: doctorSpoolCountersMarker, Status: DoctorInfo,
			Detail: "no coordinator endpoint recorded under ~/.ctxloom/coord; the spool counters live only " +
				"in a running coordinator, so there is nothing to query"}
	}

	var live []string
	var dead []string
	faults := 0
	for _, ep := range endpoints {
		stats, err := QueryCoordinatorSpoolStats(ctx, ep)
		if err != nil {
			dead = append(dead, fmt.Sprintf("%s (%v)", ep.URL, err))
			continue
		}
		line, lineFaults := spoolCountersLine(ep.URL, stats)
		faults += lineFaults
		live = append(live, line)
	}

	status, parts := spoolCountersVerdict(live, dead, faults)
	if len(problems) > 0 {
		status = DoctorWarn
		parts = append(parts, fmt.Sprintf("%d endpoint file(s) could not be read: %s", len(problems), strings.Join(problems, "; ")))
	}
	return DoctorCheck{Marker: doctorSpoolCountersMarker, Status: status, Detail: strings.Join(parts, "; ")}
}

// spoolCountersLine prints one live coordinator's counters by name, flagging
// each counter whose non-zero value is a fault, and returns how many were.
func spoolCountersLine(url string, stats *agentcoordpb.SpoolStatsResult) (string, int) {
	faults := 0
	line := fmt.Sprintf("%s: delivered=%d consumed=%d failed=%d doorbell_dropped=%d doorbell_rejected=%d",
		url, stats.GetDelivered(), stats.GetConsumed(), stats.GetFailed(),
		stats.GetDoorbellDropped(), stats.GetDoorbellRejected())
	if stats.GetFailed() > 0 {
		faults++
		line += " — failed>0: each one is a message that has not arrived"
	}
	if stats.GetDoorbellRejected() > 0 {
		faults++
		line += " — doorbell_rejected>0: a doorbell ref that did not validate (a broken or hostile sender, not a race)"
	}
	return line, faults
}

// spoolCountersVerdict words the live and not-live endpoints: INFO when none
// answered, OK when some did cleanly, WARN when any answered with a fault.
func spoolCountersVerdict(live, dead []string, faults int) (DoctorStatus, []string) {
	var parts []string
	status := DoctorInfo
	if len(live) > 0 {
		status = DoctorOK
		if faults > 0 {
			status = DoctorWarn
		}
		parts = append(parts, fmt.Sprintf("%d live coordinator(s) answered: %s", len(live), strings.Join(live, "; ")))
	}
	if len(dead) > 0 {
		if len(live) == 0 {
			parts = append(parts, fmt.Sprintf("%d recorded coordinator endpoint(s), none live (an endpoint.json outlives "+
				"its coordinator by design, so this is the ordinary state between sessions); nothing to query: %s",
				len(dead), strings.Join(dead, ", ")))
		} else {
			parts = append(parts, fmt.Sprintf("%d not live: %s", len(dead), strings.Join(dead, ", ")))
		}
	}
	return status, parts
}
