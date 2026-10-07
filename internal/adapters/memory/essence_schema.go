package memory

import (
	"fmt"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
)

const (
	frontMatterOpen  = "---\n"
	frontMatterClose = "\n---\n"
)

// essenceKind versions an essence file's front-matter — the YAML block the
// file opens with, never its markdown body.
var essenceKind = schemaver.Define("session essence", 2)

// splitEssence separates an essence file into its front-matter YAML (ending
// in a newline) and everything after the closing delimiter.
func splitEssence(data []byte) (frontMatter, after string, err error) {
	text := string(data)
	if !strings.HasPrefix(text, frontMatterOpen) {
		return "", "", fmt.Errorf("compacted file missing front-matter")
	}
	rest := text[len(frontMatterOpen):]
	end := strings.Index(rest, frontMatterClose)
	if end < 0 {
		return "", "", fmt.Errorf("compacted file has unterminated front-matter")
	}
	return rest[:end+1], rest[end+len(frontMatterClose):], nil
}

// upgradeEssence migrates data's front-matter to essenceKind's current
// generation in memory. The returned Result's Data is the WHOLE file, body
// included, so it is what a write-back persists. A front-matter block from a
// newer generation is refused (schemaver.ErrNewer).
func upgradeEssence(data []byte) (schemaver.Result, error) {
	fm, after, err := splitEssence(data)
	if err != nil {
		return schemaver.Result{}, err
	}
	res, err := essenceKind.Upgrade([]byte(fm))
	if err != nil {
		return schemaver.Result{}, err
	}
	res.Data = []byte(frontMatterOpen + string(res.Data) + strings.TrimPrefix(frontMatterClose, "\n") + after)
	return res, nil
}

// readEssence reads the essence at path at the current generation. Reading
// never writes, except under --write-upgrades (schemaver.WriteUpgrades), which
// persists a migration with no backup: an essence is derived from its
// transcript and the migration renames keys without losing any value.
func readEssence(fsys afero.Fs, path string) ([]byte, error) {
	data, err := afero.ReadFile(fsys, path)
	if err != nil {
		return nil, err
	}
	res, err := upgradeEssence(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(res.Applied) > 0 && schemaver.WriteUpgrades() {
		if err := schemaver.WriteBack(fsys, path, res, schemaver.NoBackup); err != nil {
			return nil, err
		}
	}
	return res.Data, nil
}
