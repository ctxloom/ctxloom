package projectid

import (
	"errors"
	"fmt"
	"os"
)

// Action describes what Resolve did to establish the project's identity.
type Action string

const (
	// ActionNormal: the project resolved to an existing identity in place
	// (registered path, healed skew, or an adopted marker).
	ActionNormal Action = "normal"
	// ActionMoved: the tree moved or was renamed; the registry was re-pointed.
	ActionMoved Action = "moved"
	// ActionForked: a copy, or an unreadable original; a fresh identity was
	// minted into this tree rather than risk two trees sharing one log.
	ActionForked Action = "forked"
	// ActionNewProject: no prior identity; a fresh one was minted.
	ActionNewProject Action = "new"
)

// Resolution is the outcome of Resolve.
type Resolution struct {
	ProjectID string
	Action    Action
	// Warning is a human-facing message for Moved/Forked outcomes, empty
	// otherwise. The caller prints it; it never blocks startup.
	Warning string
}

// Resolve establishes the stable project-id for projectDir: registry-by-path
// first, then the in-tree marker, healing a move and forking a copy or an
// unreadable original. It never blocks on ambiguity — every case resolves to a
// concrete id, with Moved/Forked carrying a warning for the caller to surface.
// See ADR 0025.
func (m *Manager) Resolve(projectDir string) (Resolution, error) {
	// 1. Fast path: the launch path is already registered.
	if e, err := m.ResolveByPath(projectDir); err != nil {
		return Resolution{}, err
	} else if e != nil {
		return Resolution{ProjectID: e.ProjectID, Action: ActionNormal}, nil
	}

	// 2. Path miss: consult the in-tree marker.
	marker, err := ReadMarker(projectDir)
	if err != nil {
		return Resolution{}, err
	}
	if marker == "" {
		// 4. No marker, no registry entry: a brand-new project.
		return m.mintInto(projectDir, ActionNewProject)
	}

	// Marker present: resolve the id it names.
	e, err := m.ResolveByID(marker)
	if err != nil {
		return Resolution{}, err
	}
	if e == nil {
		// The marker's id is unknown here (fresh machine / lost registry).
		// Adopt it at this path rather than mint a new identity.
		ad, err := m.Adopt(marker, projectDir)
		if err != nil {
			return Resolution{}, err
		}
		return Resolution{ProjectID: ad.ProjectID, Action: ActionNormal}, nil
	}
	if cleanPath(e.Path) == cleanPath(projectDir) {
		// Registry/path skew (path lookup missed but the id maps here). Heal silently.
		return Resolution{ProjectID: e.ProjectID, Action: ActionNormal}, nil
	}

	// 3. The id maps to a different path: move vs copy.
	return m.moveOrFork(e, projectDir)
}

// moveOrFork decides between re-pointing (a proven move) and forking (a live
// copy or an unreadable original).
func (m *Manager) moveOrFork(e *Entry, projectDir string) (Resolution, error) {
	gone, probeErr := oldTreeGone(e.Path, e.ProjectID)
	if probeErr == nil && gone {
		if err := m.Repoint(e.ProjectID, projectDir); err != nil {
			return Resolution{}, err
		}
		return Resolution{
			ProjectID: e.ProjectID,
			Action:    ActionMoved,
			Warning:   fmt.Sprintf("project %s moved from %s; re-pointed to %s", e.ProjectID, e.Path, projectDir),
		}, nil
	}
	// A live copy, or an inconclusive probe: fork rather than share one log.
	res, err := m.mintInto(projectDir, ActionForked)
	if err != nil {
		return Resolution{}, err
	}
	if probeErr != nil {
		res.Warning = fmt.Sprintf("project %s: could not read original at %s (%v); forked new identity %s", e.ProjectID, e.Path, probeErr, res.ProjectID)
	} else {
		res.Warning = fmt.Sprintf("project %s appears copied from %s; forked new identity %s", e.ProjectID, e.Path, res.ProjectID)
	}
	return res, nil
}

// mintInto mints a fresh id, writes it into the tree's marker, and returns a
// Resolution with the given action.
func (m *Manager) mintInto(projectDir string, action Action) (Resolution, error) {
	e, err := m.Mint(projectDir)
	if err != nil {
		return Resolution{}, err
	}
	if err := WriteMarker(projectDir, e.ProjectID); err != nil {
		return Resolution{}, err
	}
	return Resolution{ProjectID: e.ProjectID, Action: action}, nil
}

// oldTreeGone reports whether the previously-registered path no longer holds
// this project: true when the path is absent, not a directory, or its marker
// is missing or changed (all "move" signals). A stat or marker-read error
// returns a non-nil error — an inconclusive probe, on which the caller forks.
func oldTreeGone(oldPath, id string) (gone bool, err error) {
	info, statErr := os.Stat(oldPath)
	if errors.Is(statErr, os.ErrNotExist) {
		return true, nil
	}
	if statErr != nil {
		return false, statErr
	}
	if !info.IsDir() {
		return true, nil
	}
	marker, readErr := ReadMarker(oldPath)
	if readErr != nil {
		return false, readErr
	}
	return marker != id, nil
}
