// Package kiro implements importer.VendorAdapter for kiro-cli's REAL
// conversation store: a sqlite database (confirmed on this box at
// ~/.local/share/kiro-cli/data.sqlite3 — see schema.go's package doc for
// exactly why that path, not ~/.kiro/, and not the v1 JSONL path the deleted
// reader assumed), table conversations_v2, one row per conversation keyed by
// (workdir, conversation_id).
//
// kiro is the one importer.VendorAdapter that cannot be located by a bare
// file path, per importer.VendorAdapter's own doc comment: a single sqlite
// db holds MANY conversations, so Convert's src is a COMPOSITE locator,
// "<db-path>#<conversation-id>" (see locatorSep/Locator/parseLocator below).
// EnumerateConversations is the package-local helper that discovers what
// conversation ids exist in a db, for a backfill/discovery caller that wants
// to list candidates before building a locator for any one of them — kept
// separate from Convert, per the fan-out brief's instruction not to touch
// the shared importer.VendorAdapter interface for this.
//
// Two other structural differences from codex.Adapter (the reference
// implementation this package otherwise mirrors as closely as the schema
// allows — see mapping.go's converter doc comment):
//
//  1. kiro's per-turn accounting (request_metadata) is co-located with the
//     turn's own content in a single JSON object, not spread across
//     separate envelope lines the way codex's token_count/task_complete
//     pair is — so there is no cross-line pending-boundary merge to do here.
//  2. The whole conversation is ONE JSON document (the sqlite value
//     column), not a stream of independent JSONL lines — so the
//     "malformed line degrades to partial, never fails the whole file"
//     contract is implemented at per-HISTORY-TURN granularity instead
//     (conversationDoc.History is kept as []json.RawMessage precisely so a
//     single bad turn can be skipped without json.Unmarshal failing the
//     entire document).
package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/transcript/importer"
)

// Adapter implements importer.VendorAdapter for kiro-cli's conversations_v2
// sqlite store. Stateless, like codex.Adapter — every field Convert needs
// lives in the per-call converter it constructs.
type Adapter struct{}

var _ importer.VendorAdapter = Adapter{}

// New returns a kiro Adapter. A plain Adapter{} literal works identically —
// exists only so callers outside the package don't need to know the type is
// a zero-size struct they can construct directly (mirrors codex.New).
func New() Adapter { return Adapter{} }

// locatorSep separates a kiro composite locator's db path from its
// conversation id: "<db-path>#<conversation-id>" (importer.VendorAdapter's
// doc comment). A kiro-cli conversation id is always its own uuid (never
// contains '#' — confirmed against every conversation_id value on a real db
// on this box), so splitting on the LAST occurrence of locatorSep tolerates
// the theoretical, POSIX-legal case of a db PATH that itself contains a '#'
// without needing any escaping convention.
const locatorSep = "#"

// Locator builds the composite src string Convert (and a discovery caller
// walking EnumerateConversations' results) uses to name one conversation
// inside the sqlite db at dbPath.
func Locator(dbPath, conversationID string) string {
	return dbPath + locatorSep + conversationID
}

// parseLocator splits src into its db path and conversation id, per
// Locator's own convention.
func parseLocator(src string) (dbPath, conversationID string, err error) {
	i := strings.LastIndex(src, locatorSep)
	if i < 0 {
		return "", "", fmt.Errorf("kiro: src %q is not a composite locator (want \"<db-path>#<conversation-id>\")", src)
	}
	dbPath, conversationID = src[:i], src[i+1:]
	if dbPath == "" || conversationID == "" {
		return "", "", fmt.Errorf("kiro: src %q has an empty db path or conversation id", src)
	}
	return dbPath, conversationID, nil
}

// Convert reads ONE conversation (the conversation id encoded in src) out of
// the kiro-cli sqlite db (the db path encoded in src) and appends it to rec
// in the conversation's own turn order. See importer.VendorAdapter's doc
// comment for the general contract (a malformed turn is skipped, never
// fatal for the whole conversation; a db-open failure, a missing
// conversation id, a rec.Record failure, or ctx cancellation ARE fatal).
func (Adapter) Convert(ctx context.Context, rec transcript.Recorder, src string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	dbPath, conversationID, err := parseLocator(src)
	if err != nil {
		return err
	}

	db, err := openReadOnly(dbPath)
	if err != nil {
		return fmt.Errorf("kiro: open %s: %w", dbPath, err)
	}
	defer func() { _ = db.Close() }()

	raw, err := queryConversationValue(ctx, db, conversationID)
	if err != nil {
		return fmt.Errorf("kiro: read conversation %s from %s: %w", conversationID, dbPath, err)
	}

	var doc conversationDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return fmt.Errorf("kiro: decode conversation %s from %s: %w", conversationID, dbPath, err)
	}

	if info := sessionInfo(conversationID, &doc); info != nil {
		if err := rec.Record(agent.ChatEvent{Session: info}); err != nil {
			return fmt.Errorf("kiro: record session: %w", err)
		}
	}

	c := &converter{record: importer.RecordFunc(rec, "kiro")}
	for _, turnRaw := range doc.History {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.handleTurn(turnRaw, doc.ModelInfo); err != nil {
			return err
		}
	}
	// U149-F01/F02: a conversation with real history turns that converts to
	// zero canonical entries (every turn's user/assistant content union
	// unrecognized) must not report success — see converter.entries' doc
	// comment for why the per-turn Complete boundary doesn't count. Mirrors
	// claude/codex/antigravity's identically-motivated checkFloor.
	if len(doc.History) > 0 && c.entries == 0 {
		return fmt.Errorf("kiro: conversation %s has %d history turn(s) but converted ZERO transcript entries — the vendor format this build parses no longer matches the document", conversationID, len(doc.History))
	}
	return nil
}
