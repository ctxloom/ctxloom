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
	// whenever no file stands behind it: before delivery, when the context was
	// empty, and after a delivery that FAILED.
	path string
}

// newAppendFlagDelivery constructs the append-flag context strategy writing into
// place. A nil fs defaults to the OS filesystem (agent.GetFS), matching claude's
// settings/context writers so delivery and cleanup share one fs mechanism.
func newAppendFlagDelivery(place agent.Placement, fs afero.Fs) *appendFlagDelivery {
	return &appendFlagDelivery{place: place, fs: agent.GetFS(fs)}
}

// Path returns the absolute path of the framed context file DeliverContext
// wrote, or "" whenever no file stands behind it — before delivery, when the
// framed context was empty, and after a FAILED delivery. This is how a later
// slice's buildArgs reads the file for --append-system-prompt-file, so the
// empty case is load-bearing: claude must never be handed a flag naming a file
// that was not written. The Delivered return stays Cleanup-only by design.
func (d *appendFlagDelivery) Path() string { return d.path }

// DeliverContext frames context in the ctxloom envelope
// (agent.FrameProjectContext, reused verbatim), writes it to
// <place.Dir()>/<hash>.sysprompt.md via claude's filesystem, records the written
// path, and returns a handle whose Cleanup removes that file. <hash> is a
// deterministic sha256 prefix over the framed bytes, so identical context yields
// an identical filename. Empty context frames to "" (nothing to deliver): no
// file is written, Path stays "", and a no-op handle is returned.
//
// The write itself routes through agent.AtomicWriteFile (unique temp + fsync
// + rename via iox), not a raw afero.WriteFile — this was the one claude
// writer still bypassing it (survey D15 / write-discipline baseline entry
// "contextdelivery.go#appendFlagDelivery.DeliverContext", now removed: see
// tests/arch/write_discipline_test.go). No agent.WithFileLock wraps this:
// the deterministic hash name means two concurrent deliveries of identical
// content write identical bytes (idempotent, no lost update to guard), and
// two DIFFERENT contents land at two DIFFERENT paths, so there is no
// read-modify-write here to serialize — AtomicWriteFile's rename alone is
// enough to make the write itself atomic.
func (d *appendFlagDelivery) DeliverContext(context string) (agent.Delivered, error) {
	framed := agent.FrameProjectContext(context)
	if framed == "" {
		d.path = ""
		return agent.DeliveredFunc(func() error { return nil }), nil
	}

	dir := d.place.Dir()
	if err := d.fs.MkdirAll(dir, 0o755); err != nil {
		d.path = ""
		return nil, fmt.Errorf("create context delivery dir: %w", err)
	}

	sum := sha256.Sum256([]byte(framed))
	// First 8 bytes (16 hex chars), mirroring WriteContextFile's naming scheme.
	name := hex.EncodeToString(sum[:8]) + agent.SCMFramedContextSuffix
	path := filepath.Join(dir, name)

	if err := agent.AtomicWriteFile(d.fs, path, []byte(framed), name); err != nil {
		d.path = ""
		return nil, fmt.Errorf("write framed context file: %w", err)
	}
	d.path = path

	fs := d.fs
	return agent.DeliveredFunc(func() error { return fs.Remove(path) }), nil
}

var (
	_ agent.ContextDelivery = (*appendFlagDelivery)(nil)
)
