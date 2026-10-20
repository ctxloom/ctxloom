package operations

import (
	"time"

	"github.com/ctxloom/ctxloom/internal/core/frontend"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// SessionView is the ONE read model the session listing and show render
// from: what the store records for a session (sessions.Entry) joined with
// the derived facts every renderer used to compute for itself — its title
// (the first user prompt), whether a purge destroyed the transcript,
// whether it is marked keep, and the one clock the listing is ordered by.
// Built by ViewSession, once per entry; the CLI's rows project from it and
// never from the entry.
type SessionView struct {
	Harp    string `json:"harp"`
	Project string `json:"project"`
	// State is the session's liveness now — SessionLive, SessionEnded or
	// SessionUnknown — from its liveness lock through the listing's probe;
	// "" (and absent from JSON) when the caller passed none.
	State  string `json:"state,omitempty"`
	Engine string `json:"engine"`
	// NativeSession is the engine's own session key, "" while unbound.
	NativeSession string `json:"native_session,omitempty"`
	// Title is the session's first user prompt (SessionTitle), "" when its
	// canonical transcript holds none.
	Title     string    `json:"title,omitempty"`
	StartedAt time.Time `json:"started_at"`
	// EndedAt is when the session's LAST RUN ended. A resumed session keeps
	// it while it runs again, so it says nothing about now: State does.
	EndedAt *time.Time `json:"ended_at,omitempty"`
	// EndReason is why the session ended before its engine launched; nil for
	// every other session (sessions.Entry.EndReason).
	EndReason *sessions.EndReason `json:"end_reason,omitempty"`
	// LastActivity is the one clock (sessions.ActivityTime) the listing
	// ordered this session by.
	LastActivity time.Time `json:"last_activity"`
	// Purged says `session purge` destroyed this session's transcript.
	Purged bool `json:"purged,omitempty"`
	// Kept says the session is marked keep (`ctxloom session keep`): its
	// transcript is kept at any age.
	Kept bool `json:"kept,omitempty"`
}

// ViewSession builds the read model for one entry.
func ViewSession(e sessions.Entry) SessionView {
	v := SessionView{
		Harp:          e.HarpName,
		Project:       e.ProjectDir,
		Engine:        e.Backend,
		NativeSession: e.SessionID,
		Title:         SessionTitle(e),
		StartedAt:     e.StartedAt,
		EndedAt:       e.EndedAt,
		EndReason:     e.EndReason,
		LastActivity:  e.LastActivity,
		Purged:        e.PurgedAt != nil,
		Kept:          e.Keep,
	}
	return v
}

// The three states a session can be in, one per verdict of its liveness lock
// (sessionlock): held is live, free is ended, and anything the lock cannot
// prove — no lock file, an untrusted filesystem, a failed probe — is unknown,
// never rounded to a guess.
//
// The vocabulary is the frontend port's (frontend.Session.State), which owns
// the words.
const (
	SessionLive    = frontend.SessionLive
	SessionEnded   = frontend.SessionEnded
	SessionUnknown = frontend.SessionUnknown
)

// SessionStates is every state, in the order a flag lists them.
var SessionStates = []string{SessionLive, SessionEnded, SessionUnknown}

// SessionStateProbe is the port a listing asks a session's state through: one
// of SessionLive, SessionEnded, SessionUnknown for a harp. LockSessionState is
// the production probe.
type SessionStateProbe func(harp string) string

// ViewSessions builds the read model for a listing, in the listing's order,
// asking state for each row's State. A nil state leaves State empty and
// probes nothing: a caller in a process that may itself hold a listed
// session's lock passes none. Never nil: an empty listing
// renders as an empty table, not as an absent field.
//
// refresh is asked of a row with no title only, and the row is titled from
// what it returns: a title is the session's first prompt, which a copy that
// has one already holds, so a listing refreshes only the sessions whose last
// capture had none (one never captured — its run killed before ctxloom saw
// it exit — or captured before its user spoke). A nil refresh refreshes
// nothing.
func ViewSessions(entries []sessions.Entry, state SessionStateProbe, refresh SessionRefresh) []SessionView {
	views := make([]SessionView, len(entries))
	for i, e := range entries {
		views[i] = ViewSession(e)
		if views[i].Title == "" && refresh != nil {
			views[i] = ViewSession(refresh(e))
		}
		if state != nil {
			views[i].State = state(e.HarpName)
		}
	}
	return views
}
