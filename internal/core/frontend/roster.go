package frontend

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/agebound"
)

// The roster is the agent list every frontend shows: the project's session
// index merged with the coordinator's runs into one lineage-ordered tree
// (BuildRoster), then ordered and filtered for display (ArrangeRoster). Both
// live here, below every frontend, so the terminal UI and a VS Code bridge
// show one list by one rule: a frontend draws rows, it does not decide them.

// RosterRow is one agent of the roster.
type RosterRow struct {
	Harp   string
	Agent  string
	Engine string
	// State is the roster vocabulary: the coordinator's coord.State* values
	// for the children it holds a state for; otherwise the session lock's
	// verdict as SessionLive | SessionEnded | SessionUnknown.
	State  string
	Parent string
	Depth  int // lineage indent
	// Hold is the hold parking the child's run (nil when none does). A held
	// run's State stays idle, so the hold is what tells it apart.
	Hold *coord.RunHold
	// Cause and Detail are why an ended child's run ended, and the detail
	// that came with it: the run's, empty until the run ends.
	Cause  string
	Detail string
	// Unread is how many messages wait undelivered in the child's inbox
	// (coord.RunInfo.Unread).
	Unread int
	// Pending is how many approvals the agent waits on the human for: the
	// approval queue's requests whose asker (From) is this harp. ArrangeRoster
	// sets it; BuildRoster leaves it zero.
	Pending int
	// LastActivity is when the coordinator last saw the run change state
	// (coord.RunInfo.LastEventAt); zero for a row it holds no run for.
	LastActivity time.Time
	// SessionActivity is when the session index last saw the session worked
	// (Session.LastActivity); zero for a run the index lacks.
	SessionActivity time.Time
	// ContextTokens of ContextWindow is how full the agent's context is, as
	// its engine last reported (coord.RunInfo); zero when nothing has.
	ContextTokens uint64
	ContextWindow uint64
	// Context marks a row the filters keep only for its place in the tree —
	// the running session, the selection, or a shown row's parent — without
	// passing them itself. A frontend draws it subdued.
	Context bool
}

// Activity is when r was last active: the later of the coordinator's last
// event and the index's last-worked time. The tree is ordered by it.
func (r RosterRow) Activity() time.Time {
	if r.LastActivity.After(r.SessionActivity) {
		return r.LastActivity
	}
	return r.SessionActivity
}

// BuildRoster merges the session index (every session of this project, each
// with its lock's verdict) with the coordinator's runs (children with
// lineage and delivery state) into the tree's rows: the running session
// pinned on top, children lineage-indented directly under their parent, and
// each level ordered by Activity, most recent first (ties by harp). A harp
// present in both keeps the index row's engine and takes the held row's
// richer agent/state; a held child with no index entry (e.g. a containerized
// child whose transcript hasn't landed) still shows.
func BuildRoster(index []Session, held []coord.RunInfo, selfHarp string) []RosterRow {
	return placeLineage(rosterRows(index, held, selfHarp), selfHarp)
}

// heldRow is the roster row the coordinator's run r says.
func heldRow(r coord.RunInfo) RosterRow {
	row := RosterRow{
		State: r.Phase, Parent: r.ParentHarp, Hold: r.Hold, Cause: r.Cause, Detail: r.Detail, Unread: r.Unread,
		LastActivity: r.LastEventAt, ContextTokens: r.ContextTokens, ContextWindow: r.ContextWindow,
	}
	if r.Agent != nil {
		row.Harp, row.Agent = r.Agent.AgentID, r.Agent.Role
	}
	return row
}

// rosterRows is the merge, in display order and before lineage indenting.
func rosterRows(index []Session, held []coord.RunInfo, selfHarp string) []RosterRow {
	heldByHarp := make(map[string]RosterRow, len(held))
	var heldOrder []RosterRow
	for _, r := range held {
		row := heldRow(r)
		if row.Harp == "" {
			continue
		}
		heldByHarp[row.Harp] = row
		heldOrder = append(heldOrder, row)
	}

	var rows []RosterRow
	seen := make(map[string]bool)
	addIndexRow := func(e Session) {
		h, ok := heldByHarp[e.Harp]
		rows = append(rows, indexRow(e, h, ok))
		seen[e.Harp] = true
	}
	for _, e := range index {
		addIndexRow(e)
	}
	for _, h := range heldOrder {
		if !seen[h.Harp] {
			rows = append(rows, h)
			seen[h.Harp] = true
		}
	}
	return selfFirst(rows, selfHarp)
}

// selfFirst puts the running session's row first, making one when neither
// the index nor the coordinator lists it yet: it is the row a frontend
// starts on, and the parent its children nest under. It is live — it is
// this run.
func selfFirst(rows []RosterRow, selfHarp string) []RosterRow {
	if selfHarp == "" {
		return rows
	}
	i := slices.IndexFunc(rows, func(r RosterRow) bool { return r.Harp == selfHarp })
	if i < 0 {
		return append([]RosterRow{{Harp: selfHarp, State: SessionLive}}, rows...)
	}
	self := rows[i]
	rows = slices.Delete(rows, i, i+1)
	return slices.Insert(rows, 0, self)
}

// indexRow renders one index entry, enriched by the coordinator's held row for
// the same harp when there is one.
func indexRow(e Session, held RosterRow, isHeld bool) RosterRow {
	row := RosterRow{Harp: e.Harp, Engine: e.Engine, SessionActivity: e.LastActivity}
	if !isHeld {
		row.State = e.State
		return row
	}
	// The held row is richer, not authoritative: an absent field is something
	// the coordinator has nothing to say about, so it must not erase what the
	// index already knows.
	if held.Agent != "" {
		row.Agent = held.Agent
	}
	if held.Parent != "" {
		row.Parent = held.Parent
	}
	// The coordinator's state wins when it has one: it runs that child and
	// knows. Without one it has nothing to say, and the lock's verdict stands.
	row.State = held.State
	row.Hold = held.Hold
	row.Cause, row.Detail = held.Cause, held.Detail
	row.Unread = held.Unread
	row.LastActivity = held.LastActivity
	row.ContextTokens, row.ContextWindow = held.ContextTokens, held.ContextWindow
	if row.State == "" {
		row.State = e.State
	}
	return row
}

// placeLineage moves children under their parent, depth-indented, each
// level ordered by activity (byActivity), with self — the running session —
// first among the roots. The child index is built in one
// pass, so the walk visits a node's actual children rather than rescanning
// every row per node.
func placeLineage(rows []RosterRow, self string) []RosterRow {
	known := make(map[string]bool, len(rows))
	for _, r := range rows {
		known[r.Harp] = true
	}
	// Roots are the rows with no parent, their own parent (the coordinator
	// journals a session's own run so: coord.RunRecord.TopLevel), or a parent
	// the roster doesn't know (an orphan); everything else hangs off its
	// parent.
	children := make(map[string][]RosterRow, len(rows))
	var roots []RosterRow
	for _, r := range rows {
		if r.Parent != r.Harp && known[r.Parent] {
			children[r.Parent] = append(children[r.Parent], r)
		} else {
			roots = append(roots, r)
		}
	}
	if len(roots) > 0 && roots[0].Harp == self {
		// rosterRows put the running session first; it stays there.
		byActivity(roots[1:])
	} else {
		byActivity(roots)
	}
	for _, kids := range children {
		byActivity(kids)
	}

	placed := make(map[string]bool, len(rows))
	var out []RosterRow
	var place func(r RosterRow, depth int)
	place = func(r RosterRow, depth int) {
		if placed[r.Harp] {
			return
		}
		placed[r.Harp] = true
		r.Depth = depth
		out = append(out, r)
		for _, c := range children[r.Harp] {
			place(c, depth+1)
		}
	}
	for _, r := range roots {
		place(r, 0)
	}
	// Anything left is in a parent cycle that can't happen in practice — place
	// flat rather than lose it.
	for _, r := range rows {
		place(r, 0)
	}
	return out
}

// byActivity orders sibling rows most recently active first, ties by harp.
func byActivity(rows []RosterRow) {
	slices.SortFunc(rows, func(a, b RosterRow) int {
		if c := b.Activity().Compare(a.Activity()); c != 0 {
			return c
		}
		return strings.Compare(a.Harp, b.Harp)
	})
}

// PendingByAsker is how many of the approval queue's requests each harp
// asked; empty without a queue.
func PendingByAsker(src coord.ApprovalSource) map[string]int {
	out := map[string]int{}
	if src == nil {
		return out
	}
	for _, p := range src.Pending() {
		out[p.From.Harp]++
	}
	return out
}

// RosterQuery is how a frontend wants the roster arranged.
type RosterQuery struct {
	// Self is the running session: first among the roots, and always shown.
	Self string
	// Selected is the row the frontend has selected; it always shows, so a
	// filter never pulls the selection out from under the human.
	Selected string
	// Status is the status filter.
	Status StatusFilter
	// Window is the age window: a row last active before Now-Window shows
	// only when it matters now (running, waiting on an approval, holding
	// mail, held). Zero is no window.
	Window time.Duration
	// Now is the time the window counts back from; zero is time.Now.
	Now time.Time
}

// Roster is the roster as a frontend shows it.
type Roster struct {
	// Rows are the rows the query shows, lineage-ordered, each level's rows
	// waiting on an approval first.
	Rows []RosterRow
	// Hidden is how many rows the query left out.
	Hidden int
}

// ArrangeRoster arranges BuildRoster's rows for display under q: each row
// carries the approvals its agent waits on (pending, by asking harp), a
// waiting row goes to the top of its level ahead of the activity order — the
// running session excepted, which stays first — and only the rows the status
// filter and the age window pass show, with the running session, the
// selection, and the parent of every row that shows kept as Context.
func ArrangeRoster(rows []RosterRow, pending map[string]int, q RosterQuery) Roster {
	arranged := make([]RosterRow, len(rows))
	for i, r := range rows {
		r.Pending = pending[r.Harp]
		arranged[i] = r
	}
	since := time.Time{}
	if q.Window > 0 {
		now := q.Now
		if now.IsZero() {
			now = time.Now()
		}
		since = now.Add(-q.Window)
	}
	pass := func(r RosterRow) bool {
		return q.Status.Passes(r) && (!r.Activity().Before(since) || alwaysShown(r))
	}
	keep := func(r RosterRow) bool { return r.Harp == q.Self || r.Harp == q.Selected }
	shown, hidden := filterRows(waitingFirst(arranged, q.Self), pass, keep)
	return Roster{Rows: shown, Hidden: hidden}
}

// ComposeRoster is the roster over p, arranged under q: the session index
// merged with the coordinator's runs, and each agent's waiting approvals
// from p's queue. The runs are enrichment — children's lineage and state —
// and their absence never blanks the roster: a port that cannot list them
// leaves the index rows as they are.
func ComposeRoster(ctx context.Context, p Port, q RosterQuery) (Roster, error) {
	index, err := p.ListSessions(ctx)
	if err != nil {
		return Roster{}, err
	}
	var held []coord.RunInfo
	if runs, err := p.ListRuns(ctx); err == nil {
		held = runs.Runs
	}
	return ArrangeRoster(BuildRoster(index, held, q.Self), PendingByAsker(p.Approvals()), q), nil
}

// waitingFirst reorders each level of a lineage-ordered tree (a row's
// subtree directly after it, one level deeper) so the subtrees whose root
// waits on an approval come first, the order otherwise kept. A first root
// that is self stays first.
func waitingFirst(rows []RosterRow, self string) []RosterRow {
	var subtrees [][]RosterRow
	for i := 0; i < len(rows); {
		j := i + 1
		for j < len(rows) && rows[j].Depth > rows[i].Depth {
			j++
		}
		subtrees = append(subtrees, append([]RosterRow{rows[i]}, waitingFirst(rows[i+1:j], "")...))
		i = j
	}
	pinned := 0
	if len(subtrees) > 0 && subtrees[0][0].Harp == self {
		pinned = 1
	}
	slices.SortStableFunc(subtrees[pinned:], func(a, b []RosterRow) int {
		return cmpWaiting(b[0]) - cmpWaiting(a[0])
	})
	out := make([]RosterRow, 0, len(rows))
	for _, st := range subtrees {
		out = append(out, st...)
	}
	return out
}

// cmpWaiting is 1 for a row waiting on an approval, else 0.
func cmpWaiting(r RosterRow) int {
	if r.Pending > 0 {
		return 1
	}
	return 0
}

// alwaysShown reports whether r shows whatever its age: a running agent, or
// one waiting on an approval, holding unread mail or held.
func alwaysShown(r RosterRow) bool {
	switch {
	case r.Pending > 0, r.Unread > 0, r.Hold != nil:
		return true
	}
	switch r.State {
	case SessionLive, coord.StateExecuting, coord.StateQueued:
		return true
	}
	return false
}

// filterRows is rows, lineage-ordered, less every row that neither passes
// nor is kept and has no shown descendant, with every row shown without
// passing marked Context; and how many rows it left out.
func filterRows(rows []RosterRow, pass, keep func(RosterRow) bool) ([]RosterRow, int) {
	shown := make([]bool, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		passes := pass(rows[i])
		if !shown[i] && !passes && !keep(rows[i]) {
			continue
		}
		shown[i] = true
		rows[i].Context = !passes
		// The nearest row above at a lesser depth is the parent.
		for j := i - 1; j >= 0; j-- {
			if rows[j].Depth < rows[i].Depth {
				shown[j] = true
				break
			}
		}
	}
	out := make([]RosterRow, 0, len(rows))
	for i, r := range rows {
		if shown[i] {
			out = append(out, r)
		}
	}
	return out, len(rows) - len(out)
}

// StatusFilter is which rows the roster shows by status: every row, the
// active ones, the ones waiting on the human, or the ended ones.
type StatusFilter int

const (
	StatusAll StatusFilter = iota
	StatusActive
	StatusWaiting
	StatusEnded
	statusFilterCount
)

// Next is the filter after f, from the last back to StatusAll: the order a
// frontend cycles them in.
func (f StatusFilter) Next() StatusFilter { return (f + 1) % statusFilterCount }

func (f StatusFilter) String() string {
	switch f {
	case StatusActive:
		return "active"
	case StatusWaiting:
		return "waiting"
	case StatusEnded:
		return "ended"
	}
	return "all"
}

// Passes reports whether r's status is one f shows. Active is a run that
// has not ended — queued, working, idle or held — or a session whose lock
// says it lives; waiting is one with an approval waiting on the human.
func (f StatusFilter) Passes(r RosterRow) bool {
	switch f {
	case StatusActive:
		switch r.State {
		case SessionLive, coord.StateQueued, coord.StateExecuting, coord.StateIdle:
			return true
		}
		return false
	case StatusWaiting:
		return r.Pending > 0
	case StatusEnded:
		return r.State == SessionEnded
	}
	return true
}

// listWindowAll is the ui.list_window word for no window.
const listWindowAll = "all"

// ErrInvalidListWindow refuses a ui.list_window that is neither a duration
// nor "all".
var ErrInvalidListWindow = errors.New(`ui.list_window must be a duration (7d, 2w, 12h, 90m), or 0 or "all" to show every row`)

// ParseListWindow reads ui.list_window: a duration in the age grammar, or
// "0" or "all" for no window (0).
func ParseListWindow(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == listWindowAll {
		return 0, nil
	}
	d, ok := agebound.Offset(s)
	if !ok || d < 0 {
		return 0, fmt.Errorf("%w: got %q", ErrInvalidListWindow, raw)
	}
	return d, nil
}

// listWindowSteps are the windows NextListWindow cycles through, ending on
// no window.
var listWindowSteps = []time.Duration{time.Hour, 24 * time.Hour, 7 * 24 * time.Hour, 0}

// NextListWindow is the window after d: the next step (1h, 1d, 7d, none)
// longer than d, from no window back to the shortest.
func NextListWindow(d time.Duration) time.Duration {
	if d == 0 {
		return listWindowSteps[0]
	}
	for _, step := range listWindowSteps {
		if step > d {
			return step
		}
	}
	return 0
}

// WindowLabel names a window in the largest unit that divides it: 1h, 7d,
// 90m; "any age" for none.
func WindowLabel(d time.Duration) string {
	const day = 24 * time.Hour
	switch {
	case d <= 0:
		return "any age"
	case d%day == 0:
		return fmt.Sprintf("%dd", d/day)
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return fmt.Sprintf("%dm", d/time.Minute)
}
