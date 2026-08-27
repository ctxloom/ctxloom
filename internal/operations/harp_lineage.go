package operations

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// HarpTranscript is one vendor log in a harp's lineage — the harp dir holds
// one immutable paths.EngineTranscriptLinkPrefix symlink per binding it has
// ever had, so a harp that has been /clear'd several times has several.
type HarpTranscript struct {
	// SessionID is read from the RESOLVED TARGET's base name, never parsed out
	// of the link name. The link leaf is engine-transcript-<engine>-<id>, and
	// both halves contain dashes ("claude-code", a UUID), so splitting the leaf
	// cannot be done unambiguously without knowing the engine registry here.
	// The target is <projects>/<sessionID>.jsonl, which can.
	SessionID string
	Path      string
	ModTime   time.Time
}

// HarpTranscripts lists harp's vendor-log lineage, NEWEST TARGET FIRST.
//
// This is the "lineage-listing reader" paths.EngineTranscriptLinkPrefix's doc
// reserves a naming scheme for. The harp is the durable identity: a /clear
// rotates the backend session id but never the harp, so the harp dir's own
// listing is the only place a session's pre-clear history is addressable.
//
// Links whose target no longer resolves are SKIPPED rather than reported: on a
// real box the overwhelming majority of these links dangle (they point into
// vendor storage that is pruned independently), so a dangling link is the
// normal case, not a fault worth failing a recovery over.
func HarpTranscripts(harp string) ([]HarpTranscript, error) {
	dir, err := paths.HarpDir(harp)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read harp dir %q: %w", dir, err)
	}
	var out []HarpTranscript
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, paths.EngineTranscriptLinkPrefix) {
			continue
		}
		target, rerr := filepath.EvalSymlinks(filepath.Join(dir, name))
		if rerr != nil {
			continue // dangling: the ordinary case
		}
		info, serr := os.Stat(target)
		if serr != nil || info.IsDir() {
			continue
		}
		out = append(out, HarpTranscript{
			SessionID: strings.TrimSuffix(filepath.Base(target), ".jsonl"),
			Path:      target,
			ModTime:   info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}
