package operations

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// HarpTranscript is one native conversation log in a harp's lineage. Every
// engine's native history for the session lands under its native/ dir (through
// the session home's link), one file per backend session, so a harp that has
// been /clear'd several times holds several.
type HarpTranscript struct {
	// SessionID is the log's base name without its extension: the engine
	// names each conversation file by its backend session id.
	SessionID string
	Path      string
	ModTime   time.Time
}

// HarpTranscripts lists harp's native conversation logs, NEWEST FIRST.
//
// The harp is the durable identity: a /clear rotates the backend session id
// but never the harp, so the harp's native/ dir is where a session's
// pre-clear history stays addressable. A subagent's interior log
// (<session>/subagents/) is not a conversation of the session and is skipped,
// as LocateTranscript skips it.
func HarpTranscripts(harp string) ([]HarpTranscript, error) {
	root, err := paths.HarpNativeDir(harp)
	if err != nil {
		return nil, err
	}
	var out []HarpTranscript
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if d.Name() == "subagents" {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) != ".jsonl" {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil // vanished between the readdir and the stat
		}
		out = append(out, HarpTranscript{
			SessionID: strings.TrimSuffix(d.Name(), ".jsonl"),
			Path:      p,
			ModTime:   info.ModTime(),
		})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("read native history %q: %w", root, walkErr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}
