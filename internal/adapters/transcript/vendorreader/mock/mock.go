// Package mock is the DEGENERATE vendor-transcript adapter: the second
// implementation that proves internal/adapters/operations' vendor-reader view is a
// real port rather than a single-implementation seam wearing an interface.
//
// WHY THIS EXISTS AT ALL, since the honest objection is obvious and was
// raised before it was written: mock has no vendor-native transcript store to
// import from, so on the product's own terms it has nothing to convert. It is
// registered anyway, deliberately, because a one-entry registry cannot fail.
// Every mutation to SelectAdapter's version dispatch, to the locate
// indirection, and to the registry lookup itself survived, because with one
// engine there is no branch to take wrongly. A second conforming adapter is
// the cheapest instrument that makes those mutations die — "the second
// adapter proves the port".
//
// DEGENERATE IS A CONTRACT, NOT A STAGE. This adapter converts a trivial
// format and must stay that way. If it ever acquires engine-specific
// behaviour, format negotiation, or a second line type earning its own
// handler, it has stopped being a canary and become a maintenance burden
// invented to satisfy a test — delete it or promote it deliberately, but do
// not let it drift there.
//
// The format is one JSON object per line: {"role":..., "text":..., "ts":...}.
// role is "user" or "assistant"; anything else is administrative and
// contributes no entry. ts is RFC3339 and optional. This mirrors the shape
// every vendorreader adapter reduces to, without imitating any real vendor.
package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// Adapter converts the mock transcript format. Stateless by construction:
// the whole point of a canary is that it has nothing of its own to get wrong.
type Adapter struct{}

var _ vendorreader.VendorAdapter = Adapter{}

// vendorName is the string this adapter reports in wrapped errors: the
// registry name mock is registered under, read from config data rather than
// spelled here.
const vendorName = config.BackendMock

// VersionedAdapters declares the version span this adapter reads.
//
// Unlike every real engine, mock's ValidatedVersion is NOT pinned in
// .github/engine-versions.env and must never be. That file exists so a
// scheduled workflow can compare each pin against the vendor's published
// release feed; mock has no vendor and no feed, so a pin there would be a
// drift check against nothing. The version below is self-declared and
// therefore cannot drift — which is why operations' byEngine pin map covers
// the real engines only.
var VersionedAdapters = []vendorreader.VersionedAdapter{{
	Adapter:          Adapter{},
	Range:            vendorreader.VersionRange{MinInclusive: "1.0.0", MaxExclusive: "2.0.0"},
	ValidatedVersion: "1.0.0",
}}

// line is one mock transcript record. Every field is optional: a line that
// unmarshals but names no known role is administrative, exactly as a real
// vendor's bookkeeping lines are.
type line struct {
	Role string `json:"role"`
	Text string `json:"text"`
	TS   string `json:"ts"`
}

// Convert reads the mock transcript JSONL file at src and appends its
// conversation to rec in the file's own order. Follows
// vendorreader.VendorAdapter's contract: a malformed line is skipped and is
// never fatal; a rec.Record failure or ctx cancellation IS fatal.
func (Adapter) Convert(ctx context.Context, rec transcript.Recorder, src string) error {
	lines, err := vendorreader.OpenAndReadJSONLLines(vendorName, src)
	if err != nil {
		return err
	}

	record := vendorreader.RecordFunc(rec, vendorName)
	var entries, malformed int

	if err := vendorreader.ConvertJSONLLines(ctx, rec, lines, vendorName, nil,
		func(raw []byte) error {
			var l line
			if err := json.Unmarshal(raw, &l); err != nil {
				malformed++
				return nil // malformed: skip, never fatal
			}
			entryType, ok := entryTypes[l.Role]
			if !ok {
				return nil // administrative line: contributes no entry
			}
			entries++
			return record(agent.ChatEvent{Entry: &agent.SessionEntry{
				Timestamp: parseTS(l.TS),
				Type:      entryType,
				Content:   l.Text,
			}})
		},
		func() error { return nil }); err != nil {
		return err
	}
	return checkFloor(entries, malformed, len(lines))
}

// checkFloor REFUSES a conversion that read lines and produced no entries at
// all, mirroring the claude adapter's guard of the same name.
//
// This is not defensive padding. A caller (RefreshVendorTranscript) ATOMICALLY
// REPLACES the canonical transcript with whatever this produces, so an adapter
// that returns nil after converting nothing does not fail — it silently
// destroys the file it was asked to read. Refusing is the difference between a
// loud "this is not the format I parse" and an empty transcript that looks
// like a session nobody said anything in.
func checkFloor(entries, malformed, total int) error {
	if entries > 0 || total == 0 {
		return nil
	}
	if malformed == total {
		return fmt.Errorf("%s: all %d lines failed to parse as JSON — not a transcript this build can read", vendorName, total)
	}
	return fmt.Errorf("%s: read %d lines but converted ZERO transcript entries (%d malformed) — the file is not in the mock vendor format this adapter parses", vendorName, total, malformed)
}

// entryTypes maps mock's two conversational roles onto the canonical entry
// types. Exactly two, and adding a third is the drift this package's doc
// comment forbids.
var entryTypes = map[string]agent.SessionEntryType{
	"user":      agent.EntryTypeUser,
	"assistant": agent.EntryTypeAssistant,
}

// parseTS returns the zero time for an absent or unparseable stamp: a
// timestamp is optional in this format, and the recorder stamps its own when
// one is missing.
func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
