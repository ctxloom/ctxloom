package tasks

import (
	"regexp"
	"strings"
)

// SnapshotItem is one entry from a Claude Code TodoWrite tool_use payload,
// already normalized: Text is the verbatim content (may include a
// backtick-quoted harp ID prefix that round-tripped through the LLM);
// Status is a ctxloom status name (use MapTodoStatus to translate from
// Claude's "pending"/"in_progress"/"completed").
type SnapshotItem struct {
	Text   string
	Status string
}

// Diff describes the result of a Reconcile call.
type Diff struct {
	Added    []Task // novel items that got new harp IDs
	Updated  []Task // matched items whose text or status changed
	Archived []Task // items absent from the snapshot, moved to StatusArchived
}

// MapTodoStatus translates Claude Code TodoWrite status values to ctxloom
// task statuses. Unknown values fall through to StatusToDo, which is the
// safest default for a "thing the LLM is tracking but we don't recognize".
func MapTodoStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "in_progress", "in progress":
		return StatusInProgress
	case "completed", "done":
		return StatusDone
	case "cancelled", "canceled", "archived":
		return StatusArchived
	case "pending", "to do", "todo", "":
		return StatusToDo
	default:
		return StatusToDo
	}
}

// harpIDPrefix matches a leading backtick-quoted harp ID followed by text:
// "`swift-amber-falcon` do the thing".
var harpIDPrefix = regexp.MustCompile("^`([a-z][a-z0-9-]*)`[[:space:]]+(.*)$")

// stripHarpID peels a leading `harp-id` prefix off a TodoWrite text. When
// absent, the original text (trimmed) is returned as `rest` with `id` empty.
func stripHarpID(text string) (id, rest string) {
	trimmed := strings.TrimSpace(text)
	if m := harpIDPrefix.FindStringSubmatch(trimmed); m != nil {
		return m[1], strings.TrimSpace(m[2])
	}
	return "", trimmed
}

// Reconcile applies a TodoWrite snapshot to the store using mirror-snapshot
// semantics: items present in the snapshot are matched by harp-id-in-text
// first, then by text hash; novel items get new harp IDs and are added.
// Existing tasks whose harp ID does not appear in the snapshot are moved to
// StatusArchived (already-Archived tasks are left alone). The store is
// atomically rewritten with the new state.
//
// Designed for invocation from the PostToolUse(TodoWrite) capture hook;
// safe to call from anywhere.
func (s *Store) Reconcile(items []SnapshotItem) (Diff, error) {
	all, err := s.snapshot()
	if err != nil {
		return Diff{}, err
	}
	byHarpID := make(map[string]int, len(all))
	byHash := make(map[string]int, len(all))
	for i, t := range all {
		byHarpID[t.HarpID] = i
		byHash[t.TextHash] = i
	}

	seen := make(map[string]bool, len(items))
	var diff Diff

	for _, item := range items {
		proposedID, strippedText := stripHarpID(item.Text)
		hash := hashText(strippedText)

		idx := -1
		if proposedID != "" {
			if i, ok := byHarpID[proposedID]; ok {
				idx = i
			}
		}
		if idx < 0 {
			if i, ok := byHash[hash]; ok {
				idx = i
			}
		}

		if idx < 0 {
			// Novel item — generate a fresh harp ID.
			task := Task{
				HarpID:   uniqueHarpID(all),
				Text:     strippedText,
				Status:   item.Status,
				Checked:  statusIsDone(item.Status),
				TextHash: hash,
			}
			all = append(all, task)
			newIdx := len(all) - 1
			byHarpID[task.HarpID] = newIdx
			byHash[task.TextHash] = newIdx
			seen[task.HarpID] = true
			diff.Added = append(diff.Added, task)
			continue
		}

		seen[all[idx].HarpID] = true
		changed := false
		if all[idx].Text != strippedText {
			// Drop the old hash binding before mutating; rebuild after.
			delete(byHash, all[idx].TextHash)
			all[idx].Text = strippedText
			all[idx].TextHash = hash
			byHash[hash] = idx
			changed = true
		}
		if all[idx].Status != item.Status {
			all[idx].Status = item.Status
			all[idx].Checked = statusIsDone(item.Status)
			changed = true
		}
		if changed {
			diff.Updated = append(diff.Updated, all[idx])
		}
	}

	// Archive anything not seen, except items already Archived.
	for i := range all {
		if seen[all[i].HarpID] || all[i].Status == StatusArchived {
			continue
		}
		all[i].Status = StatusArchived
		all[i].Checked = true
		diff.Archived = append(diff.Archived, all[i])
	}

	if err := s.write(all); err != nil {
		return Diff{}, err
	}
	return diff, nil
}
