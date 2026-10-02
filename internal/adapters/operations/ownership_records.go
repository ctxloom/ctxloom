package operations

import (
	"context"
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
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
	return fsstatic.NewRecords(fs, dir)
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
	plan, err := delivery.ProjectPlan(root, items, dir)
	if err != nil {
		return delivery.Delivered{}, delivery.Plan{}, err
	}
	records, err := OwnershipRecordsOn(fs)
	if err != nil {
		return delivery.Delivered{}, delivery.Plan{}, err
	}
	lo := delivery.Loadout{Plan: plan, Package: pkg, Exports: exports, WorkDir: dir}
	d, err := fsstatic.New(fs).Deliver(ctx, lo, root, delivery.ProjectTarget(dir, records))
	return d, plan, err
}

// RemoveProject delivers the EMPTY plan against the project target: the
// record says what the project writer put there, and only that is removed.
func RemoveProject(ctx context.Context, fs afero.Fs, kind engine.Engine, dir string) error {
	records, err := OwnershipRecordsOn(fs)
	if err != nil {
		return err
	}
	_, err = fsstatic.New(fs).Deliver(ctx, delivery.Loadout{WorkDir: dir}, kind.Root(), delivery.ProjectTarget(dir, records))
	return err
}
