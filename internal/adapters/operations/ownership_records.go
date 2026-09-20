package operations

import (
	"context"
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/confpatch"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// OwnershipRecords is the ONE ownership record every static delivery on
// this host writes under — the runner's session deliveries and a human
// materialize alike — so a session's project-root delivery and a
// materialize that meet on one file keep their own entries in one record.
// Home-rooted (paths.HomeRecordsDir): the targets are foreign files, so
// ctxloom never leaves its state beside them.
func OwnershipRecords() (delivery.Ownership, error) { return OwnershipRecordsOn(afero.NewOsFs()) }

// OwnershipRecordsOn is OwnershipRecords over fs: the record and the
// targets it describes live on one filesystem.
func OwnershipRecordsOn(fs afero.Fs) (delivery.Ownership, error) {
	dir, err := paths.HomeRecordsDir()
	if err != nil {
		return nil, fmt.Errorf("ownership records: %w", err)
	}
	return confpatch.NewRecords(fs, dir)
}

// ProjectPlan routes items for an AT-REST delivery into a project root:
// every kind the engine's approach offers there is selected there; a kind
// the engine does not carry is an accepted loss the caller reports. A kind
// the engine carries but offers nowhere the project target has is
// Unrootable — refused with the remedy, never rerouted. A caller that wants
// a kind left out (the context an engine reads through its session-start
// hook) hands over items without it.
func ProjectPlan(root engine.Base, items engine.Items, dir string) (delivery.Plan, error) {
	pref := delivery.Preference{Root: map[present.Kind]present.RootKind{}, AcceptLoss: map[present.Kind]bool{}}
	surfaces := root.Surfaces()
	for _, kind := range []present.Kind{present.Context, present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills} {
		a, carried := surfaces[kind]
		switch {
		case !carried:
			pref.AcceptLoss[kind] = true
		case a.Traits().Offers(present.RootProjectRoot):
			pref.Root[kind] = present.RootProjectRoot
		}
	}
	return delivery.Route(items, root, pref, present.ProjectOnHost(dir).Paths())
}

// ProjectTarget is the at-rest target: the project root under the project
// writer, recorded in records.
func ProjectTarget(dir string, records delivery.Ownership) delivery.Target {
	return delivery.Target{Root: present.ProjectOnHost(dir), Ownership: records, Writer: delivery.ProjectWriter}
}

// DeliverProject delivers pkg at rest into dir through the ONE static
// writer: materialize, and the harness install, are this call.
func DeliverProject(ctx context.Context, fs afero.Fs, kind engine.Engine, pkg composite.Package, dir string) (delivery.Delivered, delivery.Plan, error) {
	root := kind.Root()
	items := pkg.EngineItems(root.Name)
	exports, err := kind.Exports(items)
	if err != nil {
		return delivery.Delivered{}, delivery.Plan{}, fmt.Errorf("%s exports: %w", root.Name, err)
	}
	plan, err := ProjectPlan(root, items, dir)
	if err != nil {
		return delivery.Delivered{}, delivery.Plan{}, err
	}
	records, err := OwnershipRecordsOn(fs)
	if err != nil {
		return delivery.Delivered{}, delivery.Plan{}, err
	}
	lo := delivery.Loadout{Plan: plan, Package: pkg, Exports: exports, WorkDir: dir}
	d, err := fsstatic.New(fs).Deliver(ctx, lo, root.Surfaces(), ProjectTarget(dir, records))
	return d, plan, err
}

// RemoveProject delivers the EMPTY plan against the project target: the
// record says what the project writer put there, and only that is removed.
func RemoveProject(ctx context.Context, fs afero.Fs, kind engine.Engine, dir string) error {
	records, err := OwnershipRecordsOn(fs)
	if err != nil {
		return err
	}
	_, err = fsstatic.New(fs).Deliver(ctx, delivery.Loadout{WorkDir: dir}, kind.Root().Surfaces(), ProjectTarget(dir, records))
	return err
}
