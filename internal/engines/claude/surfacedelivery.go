package claude

import (
	"github.com/ctxloom/ctxloom/internal/core/agent"

	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// fileTemplateDelivery is claude's file-template delivery strategy for the
// commands surface (.claude/commands/), which must be materialized where the
// engine already looks, so it holds a cwd placement and writes into
// place.Dir(). DeliverCommands returns a Delivered whose Cleanup reverts
// exactly the manifest-tracked set it wrote.
type fileTemplateDelivery struct {
	place placement
	files safefs.Root
	// selfContainedCommands, when true, makes DeliverCommands skip the
	// GlobalCommandsDir()/WithHomeCommandsDir dedup so every command lands in
	// the target regardless of what happens to exist in the delivering
	// machine's ~/.claude/commands. Only commandsSurface.Deliver ever sets this
	// (from agent.SurfaceInputs.SelfContainedCommands, materialize's opt-out).
	selfContainedCommands bool
	// reporter is where WriteCommandFiles reports the commands it skips.
	reporter report.Sink
}

// newFileTemplateDelivery constructs the file-template strategy writing into
// place through files, so delivery and cleanup share one Root.
func newFileTemplateDelivery(place placement, files safefs.Root) *fileTemplateDelivery {
	return &fileTemplateDelivery{place: place, files: files}
}

// DeliverCommands materializes the commands surface by delegating to
// WriteCommandFiles targeted at place.Dir(): it writes the enabled command
// exports into .claude/commands/ under a ctxloom manifest, sharing the
// directory with the user's own commands. Cleanup reverts via the same
// manifest-scoped writer with no exports (WriteCommandFiles(dir, nil, …)),
// which removes exactly the manifest-tracked set and the manifest, leaving
// user commands untouched.
//
// Claude Code loads ~/.claude/commands alongside place.Dir()'s project scope, so
// a project copy byte-identical to the global one would otherwise surface as a
// duplicate slash-command. GlobalCommandsDir resolves that same user-global dir
// (claude.go), and WriteCommandFiles forwards it to WriteManagedCommandFiles as
// WithDedupHomeDir to dedup against it. When place.Dir() IS the home directory
// itself (a global-scope delivery), the resolved home commands dir equals the
// target commands dir exactly, and WriteManagedCommandFiles's own
// dir==dedupHomeDir check disables the dedup — a directory never dedups against
// itself — so no extra guard is needed here. If the home dir can't be resolved
// (no $HOME), dedup is simply left off, matching "empty disables the dedup".
//
// When d.selfContainedCommands is set (materialize's opt-out), the
// GlobalCommandsDir()/WithHomeCommandsDir step is skipped entirely: the target
// is a PORTABLE artifact whose launch environment is not this host, so deduping
// against the delivering machine's home would silently drop commands that only
// happen to already exist here.
func (d *fileTemplateDelivery) DeliverCommands(commands []agent.CommandExport) (agent.Delivered, error) {
	dir := d.place.Dir()
	opts := []agent.CommandFileOption{agent.WithCommandRoot(d.files), agent.WithReporter(d.reporter)}
	if !d.selfContainedCommands {
		if home, err := GlobalCommandsDir(); err == nil && home != "" {
			opts = append(opts, agent.WithHomeCommandsDir(home))
		}
	}
	if err := WriteCommandFiles(dir, commands, opts...); err != nil {
		return nil, err
	}
	return agent.DeliveredFunc(func() error {
		return WriteCommandFiles(dir, nil, opts...)
	}), nil
}
