package operations

import (
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
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
