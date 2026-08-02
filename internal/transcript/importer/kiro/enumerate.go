package kiro

import (
	"context"
	"fmt"
	"time"
)

// ConversationRef is one conversation EnumerateConversations discovers:
// enough to build a Convert-ready composite locator (Locator(dbPath,
// ConversationID)) without decoding that conversation's own (potentially
// tens-of-KB) value blob first. A deliberately lightweight, package-local
// helper — separate from Convert, per this package's brief: a
// backfill/discovery caller wants to list what conversations exist in a db
// before deciding which (if any) to actually import, and should not have to
// pay Convert's full decode cost just to enumerate.
type ConversationRef struct {
	ConversationID string
	// WorkDir is conversations_v2.key — kiro-cli's own launch cwd for this
	// conversation (confirmed on this box: every key value observed was an
	// absolute directory path kiro-cli was started in, never any other kind
	// of namespace string).
	WorkDir   string
	UpdatedAt time.Time
}

// EnumerateConversations lists every conversation in the kiro-cli sqlite db
// at dbPath, oldest UpdatedAt first — the natural order for a sequential
// backfill import (oldest conversations imported before newer ones, so a
// partially-completed backfill run has already captured the older, more
// likely-to-be-relevant history first). It opens and closes its own
// connection: unlike Convert, which a caller might invoke many times in a
// row against the same db (once per discovered conversation), this is meant
// for an occasional one-shot discovery pass, so the extra open/close per
// call is not a real cost.
func EnumerateConversations(ctx context.Context, dbPath string) ([]ConversationRef, error) {
	db, err := openReadOnly(ctx, dbPath)
	if err != nil {
		return nil, fmt.Errorf("kiro: open %s: %w", dbPath, err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(ctx,
		`SELECT conversation_id, key, created_at, updated_at FROM conversations_v2 ORDER BY updated_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("kiro: enumerate %s: %w", dbPath, err)
	}
	defer func() { _ = rows.Close() }()

	var out []ConversationRef
	for rows.Next() {
		var ref ConversationRef
		// created_at is selected (it's part of conversations_v2's row shape)
		// but not scanned into ConversationRef: nothing reads it.
		var createdMs, updatedMs int64
		if err := rows.Scan(&ref.ConversationID, &ref.WorkDir, &createdMs, &updatedMs); err != nil {
			return nil, fmt.Errorf("kiro: scan row in %s: %w", dbPath, err)
		}
		ref.UpdatedAt = time.UnixMilli(updatedMs).UTC()
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kiro: iterate %s: %w", dbPath, err)
	}
	return out, nil
}
