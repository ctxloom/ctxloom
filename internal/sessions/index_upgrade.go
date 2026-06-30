package sessions

import (
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// indexUpgrades is the ordered set of session-index schema upgrades. loadLocked
// runs it over the raw index bytes before parsing; upgrades apply in memory and
// are persisted only after the user confirms (see Manager.CommitUpgrade), so a
// read-only command never silently rewrites the index.
var indexUpgrades = upgrade.Pipeline{
	tsNormalizeUpgrade{},
}

// tsNormalizeUpgrade rewrites every entry's started_at/ended_at to canonical
// RFC3339Nano. Older ctxloom state — and a lossy external conversion — left some
// timestamps in non-Go encodings, e.g. "2026-05-28 16:57:20.781317+00:00"
// (space-separated, microseconds, "+00:00"), which Go's RFC3339-only time
// decoder rejects; that previously broke session resume. Normalizing on load
// lets the rest of the package work with plain time.Time.
type tsNormalizeUpgrade struct{}

// Name identifies the upgrade in logs and the rewrite prompt.
func (tsNormalizeUpgrade) Name() string { return "session timestamp normalize" }

// tsLayouts are tried in order when decoding a stored timestamp. "Z07:00"
// accepts both "Z" and a numeric "+00:00"-style offset.
var tsLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999Z07:00", // isoformat(sep=' ') with offset
	"2006-01-02 15:04:05Z07:00",           // space-separated, whole seconds, with offset
	"2006-01-02 15:04:05",                 // space-separated, no offset (assumed UTC)
	"2006-01-02",                          // date only
}

// Apply walks the sessions sequence and normalizes each timestamp scalar.
func (tsNormalizeUpgrade) Apply(root *yaml.Node) (changed bool) {
	sessions := upgrade.MapValue(root, "sessions")
	if sessions == nil || sessions.Kind != yaml.SequenceNode {
		return false
	}
	for _, entry := range sessions.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		for _, key := range []string{"started_at", "ended_at"} {
			if normalizeTimestampNode(upgrade.MapValue(entry, key)) {
				changed = true
			}
		}
	}
	return changed
}

// normalizeTimestampNode rewrites a timestamp scalar to canonical RFC3339Nano,
// returning whether it changed. It re-encodes from the parsed time.Time so yaml
// emits its own native (unquoted) timestamp scalar, guaranteeing the value
// round-trips cleanly back into a time.Time on the next load. An unparseable
// value degrades to the zero time so the plain decoder never chokes on it
// (fault tolerance). A nil, empty, or already-canonical node is left untouched.
func normalizeTimestampNode(n *yaml.Node) bool {
	if n == nil || n.Kind != yaml.ScalarNode || n.Value == "" {
		return false
	}
	t, ok := parseTimestamp(n.Value)
	if !ok {
		// Degrade an unparseable timestamp to NOW, not the zero time. The zero time
		// (year 1) sorts a session to the bottom of the picker AND falls before its
		// day-horizon cutoff, so the session silently vanishes — the opposite of
		// fault tolerance. "now" keeps the row visible (sorts as recent) while still
		// canonicalizing the value so the plain decoder won't choke on it.
		t = time.Now()
	}
	t = t.UTC()
	if n.Value == t.Format(time.RFC3339Nano) {
		return false
	}
	if err := n.Encode(t); err != nil {
		return false
	}
	return true
}

// parseTimestamp tries each known layout, returning the parsed time and whether
// any matched.
func parseTimestamp(s string) (time.Time, bool) {
	for _, layout := range tsLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
