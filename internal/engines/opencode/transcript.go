package opencode

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// The transcript the bridge plugin writes for each root session, after every
// turn: <XDG data base>/opencode/ctxloom/<session id>.jsonl, inside opencode's
// data app dir, which is the session's native/ (TranscriptStoreRel), so the
// file is found BY LOCATION (sessions.LocateTranscript) whether or not a hook
// bound it. Its first line is ctxloom's own (the session id and the
// session_start context, the harp marker among it); every other line is one
// message as opencode's API holds it ({info, parts}).
const (
	transcriptBase = "XDG_DATA_HOME"
	transcriptDir  = "ctxloom"
	transcriptRel  = appDir + "/" + transcriptDir
	transcriptExt  = ".jsonl"
)

// Transcripts are the readers of the bridge plugin's transcripts, handed in
// by the composition root (WithTranscripts).
func (o Opencode) Transcripts() []engine.TranscriptReader { return o.transcripts }

// TranscriptSession is the session a bridge transcript records: its file
// stem. A file that is not one is refused.
func (o Opencode) TranscriptSession(path string) (string, error) {
	base := filepath.Base(path)
	if filepath.Base(filepath.Dir(path)) != transcriptDir || !strings.HasSuffix(base, transcriptExt) || base == transcriptExt {
		return "", fmt.Errorf("%s: %s is not a bridge transcript (<data>/%s/<session>%s)", o.Name, path, transcriptRel, transcriptExt)
	}
	return strings.TrimSuffix(base, transcriptExt), nil
}

// WithTranscripts hands the kind the readers of its transcripts: they are
// adapters, so the composition root supplies them.
func WithTranscripts(readers ...engine.TranscriptReader) Option {
	return func(o *Opencode) { o.transcripts = readers }
}
