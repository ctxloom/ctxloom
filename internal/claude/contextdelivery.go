package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// appendFlagDelivery is claude's context-delivery strategy for the
// --append-system-prompt-file launch flag. It frames ctxloom's assembled
// context in the ctxloom envelope (agent.FrameProjectContext — the same
// whole-file form claude loads natively), writes it into an injected Placement
// as <hash>.sysprompt.md, and exposes the written path via Path() so claude's
// buildArgs (a later slice) can point the flag at it. It implements
// agent.ContextDelivery; the Delivered handle it returns stays Cleanup-only per
// the delivery-seam design.
type appendFlagDelivery struct {
	place agent.Placement
	fs    afero.Fs
	// path is the absolute path of the framed file DeliverContext wrote, or ""
	// before delivery (or when the context was empty and nothing was written).
	path string
}

// newAppendFlagDelivery constructs the append-flag context strategy writing into
// place. A nil fs defaults to the OS filesystem (agent.GetFS), matching claude's
// settings/context writers so delivery and cleanup share one fs mechanism.
func newAppendFlagDelivery(place agent.Placement, fs afero.Fs) *appendFlagDelivery {
	return &appendFlagDelivery{place: place, fs: agent.GetFS(fs)}
}

// Path returns the absolute path of the framed context file DeliverContext
// wrote, or "" before delivery (or when the framed context was empty). This is
// how a later slice's buildArgs reads the file for --append-system-prompt-file;
// the Delivered return stays Cleanup-only by design.
func (d *appendFlagDelivery) Path() string { return d.path }

// DeliverContext frames context in the ctxloom envelope
// (agent.FrameProjectContext, reused verbatim), writes it to
// <place.Dir()>/<hash>.sysprompt.md via claude's filesystem, records the written
// path, and returns a handle whose Cleanup removes that file. <hash> is a
// deterministic sha256 prefix over the framed bytes, so identical context yields
// an identical filename. Empty context frames to "" (nothing to deliver): no
// file is written, Path stays "", and a no-op handle is returned.
func (d *appendFlagDelivery) DeliverContext(context string) (agent.Delivered, error) {
	framed := agent.FrameProjectContext(context)
	if framed == "" {
		d.path = ""
		return deliveredFunc(func() error { return nil }), nil
	}

	dir := d.place.Dir()
	if err := d.fs.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create context delivery dir: %w", err)
	}

	sum := sha256.Sum256([]byte(framed))
	// First 8 bytes (16 hex chars), mirroring WriteContextFile's naming scheme.
	name := hex.EncodeToString(sum[:8]) + agent.SCMFramedContextSuffix
	path := filepath.Join(dir, name)

	if err := afero.WriteFile(d.fs, path, []byte(framed), 0o644); err != nil {
		return nil, fmt.Errorf("write framed context file: %w", err)
	}
	d.path = path

	fs := d.fs
	return deliveredFunc(func() error { return fs.Remove(path) }), nil
}

// deliveredFunc adapts a cleanup closure to agent.Delivered so a strategy can
// return its teardown inline without a bespoke handle type.
type deliveredFunc func() error

// Cleanup runs the wrapped cleanup closure.
func (f deliveredFunc) Cleanup() error { return f() }

var (
	_ agent.ContextDelivery = (*appendFlagDelivery)(nil)
	_ agent.Delivered       = deliveredFunc(nil)
)
