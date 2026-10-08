package opencodeengine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/opencode"
)

// vendor names the engine in a conversion's errors.
const vendor = string(opencode.EngineName)

// Adapter converts a bridge transcript (<data>/opencode/ctxloom/<session>.jsonl,
// written by the bridge plugin after every turn): a header line of ctxloom's
// own naming the session, then one message per line as opencode's API holds
// it, mapped by the same part mapping the live stream uses
// (opencode.MessageEvents). No opencode binary is needed to read it.
type Adapter struct{}

var _ vendorreader.VendorAdapter = Adapter{}

// VersionedAdapters is the span the bridge transcript is read for: the
// message shape of opencode's v1 API across the 1.18 line, validated at the
// pinned version (.github/engine-versions.env OPENCODE_CLI_VERSION).
var VersionedAdapters = []vendorreader.VersionedAdapter{{
	Adapter:          Adapter{},
	Range:            vendorreader.VersionRange{MinInclusive: "1.18.0", MaxExclusive: "1.19.0"},
	ValidatedVersion: "1.18.35",
}}

// Transcripts are the readers the composition root hands opencode.Build.
func Transcripts() []engine.TranscriptReader {
	out := make([]engine.TranscriptReader, 0, len(VersionedAdapters))
	for _, a := range VersionedAdapters {
		out = append(out, a)
	}
	return out
}

// header is the bridge's first line.
type header struct {
	Ctxloom *struct {
		Session string `json:"session"`
	} `json:"ctxloom"`
}

// headerOf reads a bridge header line; nil for any other line.
func headerOf(raw []byte) *agent.ChatSessionInfo {
	var h header
	if json.Unmarshal(raw, &h) != nil || h.Ctxloom == nil {
		return nil
	}
	return &agent.ChatSessionInfo{SessionID: h.Ctxloom.Session}
}

// Convert appends the transcript's conversation to rec in its own order. A
// malformed line is skipped; a file that has lines and yields no entry at all
// is refused (the caller replaces the canonical transcript with the result,
// so converting nothing would erase it).
func (Adapter) Convert(ctx context.Context, fsys afero.Fs, rec transcript.Recorder, src string) error {
	lines, err := vendorreader.OpenAndReadJSONLLines(fsys, vendor, src)
	if err != nil {
		return err
	}
	var info *agent.ChatSessionInfo
	if len(lines) > 0 {
		info = headerOf(lines[0])
	}
	record := vendorreader.RecordFunc(rec, vendor)
	entries, skipped := 0, 0
	err = vendorreader.ConvertJSONLLines(ctx, rec, lines, vendor, info, func(raw []byte) error {
		if headerOf(raw) != nil {
			return nil
		}
		evs, err := opencode.MessageEvents(raw)
		if err != nil {
			skipped++
			return nil
		}
		for _, ev := range evs {
			entries++
			if err := record(ev); err != nil {
				return err
			}
		}
		return nil
	}, func() error { return nil })
	if err != nil {
		return err
	}
	if entries == 0 && skipped > 0 {
		return fmt.Errorf("%s: %s: no message among %d lines (%d unreadable): not a bridge transcript this build reads", vendor, src, len(lines), skipped)
	}
	return nil
}
