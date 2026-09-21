package operations

import (
	"time"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// SessionView is the ONE read model the session listing and show render
// from: what the store records for a session (sessions.Entry) joined with
// the derived facts every renderer used to compute for itself — whether
// the session is distilled and where its essence is, whether that essence
// is stale against the transcript, whether a purge destroyed the transcript,
// and the one clock the listing is ordered by. Built by ViewSession, once
// per entry; the CLI's rows and the MCP session menu project from it and
// never from the entry.
type SessionView struct {
	Harp    string `json:"harp"`
	Project string `json:"project"`
	Engine  string `json:"engine"`
	// NativeSession is the engine's own session key, "" while unbound.
	NativeSession string     `json:"native_session,omitempty"`
	Summary       string     `json:"summary,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	// LastActivity is the one clock (sessions.ActivityTime) the listing
	// ordered this session by.
	LastActivity time.Time `json:"last_activity"`
	// Purged says `session purge` destroyed this session's transcript.
	Purged bool `json:"purged,omitempty"`
	// Distilled says an essence exists; EssencePath is where.
	Distilled   bool   `json:"distilled"`
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
		Summary:       e.Summary,
		StartedAt:     e.StartedAt,
		EndedAt:       e.EndedAt,
		LastActivity:  e.LastActivity,
		Purged:        e.PurgedAt != nil,
	}
	v.EssencePath, v.Distilled = SessionEssenceInfo(e.HarpName, &e)
	v.Stale, v.StaleKnown = e.SourceStale()
	return v
}

// ViewSessions builds the read model for a listing, in the listing's order.
// Never nil: an empty listing renders as an empty table, not as an absent
// field.
func ViewSessions(entries []sessions.Entry) []SessionView {
	views := make([]SessionView, len(entries))
	for i, e := range entries {
		views[i] = ViewSession(e)
	}
	return views
}
