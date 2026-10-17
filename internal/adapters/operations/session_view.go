package operations

import (
	"time"

	"github.com/ctxloom/ctxloom/internal/core/frontend"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// SessionView is the ONE read model the session listing and show render
// from: what the store records for a session (sessions.Entry) joined with
// the derived facts every renderer used to compute for itself — whether
// the session is compacted and where its essence is, whether that essence
// is stale against the transcript, whether a purge destroyed the transcript,
// and the one clock the listing is ordered by. Built by ViewSession, once
// per entry; the CLI's rows and the MCP session menu project from it and
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
	// LastActivity is the one clock (sessions.ActivityTime) the listing
	// ordered this session by.
	LastActivity time.Time `json:"last_activity"`
	// Purged says `session purge` destroyed this session's transcript.
	Purged bool `json:"purged,omitempty"`
	// Compacted says an essence exists; EssencePath is where.
	Compacted   bool   `json:"compacted"`
	EssencePath string `json:"essence_path,omitempty"`
	// Stale says the essence predates the live transcript; StaleKnown says
	// whether that could be determined at all (sessions.Entry.SourceStale).
	Stale      bool `json:"stale,omitempty"`
	StaleKnown bool `json:"stale_known,omitempty"`
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
		LastActivity:  e.LastActivity,
		Purged:        e.PurgedAt != nil,
	}
	v.EssencePath, v.Compacted = SessionEssenceInfo(e.HarpName, &e)
	v.Stale, v.StaleKnown = e.SourceStale()
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
// probes nothing: the caller for a process that may itself hold a listed
// session's lock (MCP list_sessions) passes none. Never nil: an empty listing
// renders as an empty table, not as an absent field.
func ViewSessions(entries []sessions.Entry, state SessionStateProbe) []SessionView {
	views := make([]SessionView, len(entries))
	for i, e := range entries {
		views[i] = ViewSession(e)
		if state != nil {
			views[i].State = state(e.HarpName)
		}
	}
	return views
}
