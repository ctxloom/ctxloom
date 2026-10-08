package kit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// FramedFile is the context delivered as a whole file the approach owns
// under the session home: ctxloom's assembled context framed in the ctxloom
// envelope (agent.FrameProjectContext), written there and declared, and
// named on the engine's channel. The engine supplies only the file's leaf
// (Name) and the channel (Announce).
type FramedFile struct {
	// Name is the file's leaf for the framed bytes (HashedName, FixedName).
	Name func(framed []byte) string
	// Announce names the file, or the root holding it, on the channel the
	// engine reads it from; nil announces nothing.
	Announce func(present.Rooted) present.Rooted
}

// Deliver writes the framed text beneath the session home, behind a's
// rooting rule (a run without a session home is refused, never served from
// the user's own home), and declares it. Empty text frames to nothing: no
// file is written and nothing is presented, so no channel names a file that
// was not written. The write keeps an existing file's mode
// (safefs.WriteFileKeepMode), atomically: identical text lands at an
// identical HashedName with identical bytes, so no lock is needed.
func (f FramedFile) Deliver(a Approach, start present.Start, text []byte, fs afero.Fs) (present.Delivered, error) {
	framed := []byte(agent.FrameProjectContext(string(text)))
	leaf := ""
	if len(framed) > 0 {
		leaf = f.Name(framed)
	}
	r, err := a.Rooted(start, present.RootSessionHome, leaf, "")
	if err != nil || len(framed) == 0 {
		return present.Delivered{}, err
	}
	if f.Announce != nil {
		r = f.Announce(r)
	}
	p := r.Build()
	fs = agent.GetFS(fs)
	if err := fs.MkdirAll(filepath.Dir(p.HostPath), 0o755); err != nil {
		return present.Delivered{}, fmt.Errorf("%s/%s: create the context dir: %w", a.Engine, a.ApproachName, err)
	}
	if err := safefs.WriteFileKeepMode(fs, p.HostPath, framed, leaf); err != nil {
		return present.Delivered{}, fmt.Errorf("%s/%s: write the framed context: %w", a.Engine, a.ApproachName, err)
	}
	return present.Delivered{Presented: p, Files: []string{p.HostPath}}, nil
}

// HashedName names the file by its content: a sha256 prefix of the framed
// bytes (16 hex chars) and suffix, so identical context is one file.
func HashedName(suffix string) func([]byte) string {
	return func(framed []byte) string {
		sum := sha256.Sum256(framed)
		return hex.EncodeToString(sum[:8]) + suffix
	}
}

// FixedName names the file leaf whatever it holds.
func FixedName(leaf string) func([]byte) string { return func([]byte) string { return leaf } }
