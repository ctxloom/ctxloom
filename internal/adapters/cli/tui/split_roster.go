package tui

import (
	"fmt"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// The panel's roster rides the port's WatchRuns: its snapshot is the first
// roster, and every event after it — a loss marker included — schedules one
// fresh read, debounced, because what the tree shows (phase, hold, unread)
// is carried by no event: only ListRuns states it. A slow
// tick re-reads behind that, for a change no event announces.

const (
	// rosterDebounce gathers a burst of events into one roster read.
	rosterDebounce = 250 * time.Millisecond
	// rosterSafetyEvery is the re-read no event triggers.
	rosterSafetyEvery = 5 * time.Second
)

type rosterWatchMsg struct {
	watch frontend.RunWatch
	err   error
}

// rosterEventsMsg is one or more run events received; closed: the watch
// ended.
type rosterEventsMsg struct{ closed bool }

// rosterRefreshMsg is a scheduled roster read coming due: the debounce, or
// the safety tick.
type rosterRefreshMsg struct{ debounced bool }

// openRosterCmd opens the run watch.
func (s *Split) openRosterCmd() tea.Cmd {
	port, ctx := s.port, s.ctx
	if port == nil {
		return nil
	}
	return func() tea.Msg {
		w, err := port.WatchRuns(ctx, nil)
		return rosterWatchMsg{watch: w, err: err}
	}
}

// loadRosterCmd reads the roster afresh: the session index merged with the
// coordinator's runs.
func (s *Split) loadRosterCmd() tea.Cmd {
	port, ctx, self := s.port, s.ctx, s.self
	return func() tea.Msg {
		rows, err := LoadRoster(ctx, port, self)
		if err != nil {
			return rosterErrMsg{err}
		}
		return rosterMsg{rows}
	}
}

// snapshotRosterCmd builds the first roster from the watch's snapshot and
// the session index.
func (s *Split) snapshotRosterCmd(snap coord.RunsSnapshot) tea.Cmd {
	port, ctx, self := s.port, s.ctx, s.self
	return func() tea.Msg {
		index, err := port.ListSessions(ctx)
		if err != nil {
			return rosterErrMsg{err}
		}
		return rosterMsg{frontend.BuildRoster(index, snap.Runs, self)}
	}
}

// waitRunEvents receives the next run event and every one already queued
// behind it: a burst is one message.
func (s *Split) waitRunEvents() tea.Cmd {
	ch := s.runEvents
	if s.driven || ch == nil {
		return nil
	}
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return rosterEventsMsg{closed: true}
		}
		for {
			select {
			case _, ok := <-ch:
				if !ok {
					return rosterEventsMsg{closed: true}
				}
			default:
				return rosterEventsMsg{}
			}
		}
	}
}

func (s *Split) applyRosterMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case rosterWatchMsg:
		s.armSafetyTick()
		if msg.err != nil {
			// No coordinator: the session index alone, on the slow tick.
			return s.loadRosterCmd()
		}
		s.runEvents = msg.watch.Events
		return tea.Batch(s.snapshotRosterCmd(msg.watch.Snapshot), s.waitRunEvents())
	case rosterEventsMsg:
		if msg.closed {
			s.runEvents = nil
			return nil
		}
		if !s.refreshArmed {
			s.refreshArmed = true
			s.clock.AfterFunc(rosterDebounce, func() { s.post(rosterRefreshMsg{debounced: true}) })
		}
		return s.waitRunEvents()
	case rosterRefreshMsg:
		if msg.debounced {
			s.refreshArmed = false
		} else {
			s.armSafetyTick()
		}
		return s.loadRosterCmd()
	case rosterMsg:
		s.applyRoster(msg.rows)
	case rosterErrMsg:
		s.rosterErr = fmt.Sprintf("roster: %v", msg.err)
	}
	return nil
}

// armSafetyTick schedules the next unprompted roster read.
func (s *Split) armSafetyTick() {
	if s.ctx.Err() != nil {
		return
	}
	s.clock.AfterFunc(rosterSafetyEvery, func() { s.post(rosterRefreshMsg{}) })
}

// applyRoster adopts a fresh roster. The selection stays on its harp; a harp
// that left the roster hands the selection back to the session's own row,
// and its feed goes with it.
func (s *Split) applyRoster(rows []RosterRow) {
	s.roster = rows
	s.rosterErr = ""
	s.arrangeRows()
	if s.showingTerminal() || slices.ContainsFunc(s.rows, func(r RosterRow) bool { return r.Harp == s.sel }) {
		return
	}
	s.sel = s.self
	s.child.close()
}

// openFeedCmd opens harp's feed over the port.
func (s *Split) openFeedCmd(harp string) tea.Cmd {
	port, ctx := s.port, s.ctx
	return func() tea.Msg {
		f, err := openFeed(ctx, port, harp)
		if err != nil {
			return feedErrMsg{harp: harp, err: err}
		}
		return feedOpenedMsg{harp: harp, feed: f}
	}
}

// waitFeedCmd receives the child's next feed event.
func (s *Split) waitFeedCmd() tea.Cmd {
	if s.driven || s.child.feed == nil {
		return nil
	}
	return waitEventCmd(s.child.harp, s.child.feed)
}

// arrangeRows lays the roster out for the panel under the panel's filters
// (frontend.ArrangeRoster): the rule every frontend shows its list by.
func (s *Split) arrangeRows() {
	r := frontend.ArrangeRoster(s.roster, s.pendingBy, frontend.RosterQuery{
		Self: s.self, Selected: s.sel, Status: s.statusFilter, Window: s.listWindow, Now: s.clock.Now(),
	})
	s.rows, s.hiddenRows = r.Rows, r.Hidden
}
