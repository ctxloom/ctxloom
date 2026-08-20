package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// StoreRel is claude-code's transcript store, relative to HOME.
//
// It matches isolation.ContainerTranscriptStoreRelFor("claude-code"), which
// declares the same path for container mounts. Two spellings of one fact is a
// drift risk; TestArch_ClaudeStoreRel_MatchesEngineSpec binds them.
const StoreRel = ".claude/projects"

// Locator enumerates claude-code's own transcript store.
type Locator struct {
	// FS is the filesystem to read (nil means the OS filesystem).
	FS afero.Fs
	// Home overrides the user home directory; empty uses os.UserHomeDir.
	Home string
}

// Discover implements vendorreader.Locator for claude-code.
//
// # The layout, and what is trusted about it
//
// ~/.claude/projects/<per-project-dir>/<session-id>.jsonl. The directory name
// encodes the project path with separators replaced, but that encoding is NOT
// relied on: it is lossy (a path containing the replacement character is
// ambiguous) and it is a private convention that can change. Every record in
// the transcript carries `cwd`, so the project a session belongs to is READ
// rather than decoded, and the directory name is used only to enumerate.
//
// A project directory holding no .jsonl at all is skipped, not refused: claude
// writes other state there and an empty project is ordinary.
func (l Locator) Discover(workDir string) ([]vendorreader.Located, error) {
	fs := l.FS
	if fs == nil {
		fs = afero.NewOsFs()
	}
	home := l.Home
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = h
	}
	root := filepath.Join(home, filepath.FromSlash(StoreRel))

	entries, err := afero.ReadDir(fs, root)
	if err != nil {
		if os.IsNotExist(err) {
			// The engine is not installed, or has never run. Legitimately
			// empty — not a store we failed to understand.
			return nil, nil
		}
		return nil, err
	}

	var out []vendorreader.Located
	sawProjectDir := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sawProjectDir = true
		dir := filepath.Join(root, e.Name())
		files, err := afero.ReadDir(fs, dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			path := filepath.Join(dir, f.Name())
			cwd, ok := recordedCwd(fs, path)
			if !ok {
				continue
			}
			if workDir != "" && filepath.Clean(cwd) != filepath.Clean(workDir) {
				continue
			}
			out = append(out, vendorreader.Located{
				Engine:     "claude-code",
				SessionID:  strings.TrimSuffix(f.Name(), ".jsonl"),
				Path:       path,
				WorkDir:    cwd,
				ModifiedAt: f.ModTime(),
			})
		}
	}

	// The store exists but holds no project directories at all. That is not
	// "no sessions" — it is a shape this Locator does not recognise, and
	// reporting zero would be indistinguishable from having none.
	if !sawProjectDir && len(entries) > 0 {
		return nil, &vendorreader.UnrecognizedStoreError{
			Engine:   "claude-code",
			Root:     root,
			Expected: "per-project directories holding <session-id>.jsonl",
			Found:    "only files",
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ModifiedAt.After(out[j].ModifiedAt) })
	return out, nil
}

// recordedCwd returns the project path the transcript itself records.
//
// It reads only as far as the first record carrying a cwd: claude writes it on
// nearly every line, so this is a first-line read in practice, and a file whose
// records carry none is skipped rather than guessed at.
func recordedCwd(fs afero.Fs, path string) (string, bool) {
	f, err := fs.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()

	dec := json.NewDecoder(f)
	for i := 0; i < 200; i++ {
		var rec struct {
			Cwd string `json:"cwd"`
		}
		if err := dec.Decode(&rec); err != nil {
			return "", false
		}
		if rec.Cwd != "" {
			return rec.Cwd, true
		}
	}
	return "", false
}
