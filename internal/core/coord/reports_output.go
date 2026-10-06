package coord

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// reportsDirName is the folder of a session's output dir that holds its
// tree's FINAL reports, one subfolder per reporting agent, named by its harp.
const reportsDirName = "reports"

// finalReportFileName is the file a FINAL report's own text is saved as.
const finalReportFileName = "report.md"

// reportFileMode and reportDirMode are a saved report's modes: the output dir is the
// human's, and holds documents, not secrets.
const (
	reportFileMode = 0o644
	reportDirMode  = 0o755
)

// outputDirOf resolves a session's recorded output dir (sessions.OutputDir);
// a package var so a test can point it at a temp dir.
var outputDirOf = sessions.OutputDir

// saveFinalReport writes harp's FINAL report where the human reads: into the
// tree root's output dir, under reports/<harp>/ — each artifact the report
// names at its latest revision, by its base name, and the report's text as
// finalReportFileName. Rewritten whole by a later FINAL, so the folder holds
// the latest of each. Best-effort: the report is already journaled, so a
// failure warns and costs only the readable copy.
func (c *Coordinator) saveFinalReport(harp string, s Summary) {
	if s.Scope != ScopeFinal {
		return
	}
	out, err := outputDirOf(c.rootHarp)
	if errors.Is(err, sessions.ErrNotFound) {
		return
	}
	if err == nil {
		err = c.writeFinalReport(filepath.Join(out, reportsDirName, harp), harp, s)
	}
	if err != nil {
		c.rep.Warnf("coordinator: %s's FINAL report is journaled but could not be saved to the output dir: %v", harp, err)
	}
}

// writeFinalReport writes s and its artifacts into dir.
func (c *Coordinator) writeFinalReport(dir, harp string, s Summary) error {
	fsys := c.root.Fs
	if err := fsys.MkdirAll(dir, reportDirMode); err != nil {
		return err
	}
	used := map[string]bool{finalReportFileName: true}
	var errs error
	for _, id := range s.ArtifactIDs {
		rec, ok := c.artifactRecord(harp, id)
		if !ok {
			errs = errors.Join(errs, fmt.Errorf("artifact %q has no manifest", id))
			continue
		}
		name := reportFileName(rec, used)
		used[name] = true
		b, err := c.artifactBytes(rec)
		if err == nil {
			err = safefs.WriteFile(fsys, filepath.Join(dir, name), b, reportFileMode)
		}
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("artifact %q: %w", id, err))
		}
	}
	if err := safefs.WriteFile(fsys, filepath.Join(dir, finalReportFileName), []byte(s.Text), reportFileMode); err != nil {
		errs = errors.Join(errs, err)
	}
	return errs
}

// reportFileName is the file rec is saved as: its name's base — never a path
// out of the folder — or its artifact id when the name has none, prefixed
// with the artifact id when another file of the report already took it.
func reportFileName(rec ArtifactRecord, used map[string]bool) string {
	name := filepath.Base(filepath.FromSlash(strings.ReplaceAll(rec.Name, `\`, "/")))
	if name == "." || name == ".." || name == string(filepath.Separator) || name == "" {
		name = filepath.Base(rec.ArtifactID)
	}
	if used[name] {
		name = filepath.Base(rec.ArtifactID) + "-" + name
	}
	return name
}

// artifactBytes reads rec's stored content whole.
func (c *Coordinator) artifactBytes(rec ArtifactRecord) ([]byte, error) {
	f, err := c.openArtifactAt(rec, rec.ArtifactID, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}
