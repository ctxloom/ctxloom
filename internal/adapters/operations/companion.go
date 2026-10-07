package operations

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// ErrCompanionNotRegistered is a remove of a name the config does not
// register.
var ErrCompanionNotRegistered = errors.New("companion not registered")

// CompanionAddResult is what `companion add` registered: the name, and where
// it resolved at add time. Only Name is recorded; Bin and Path are reported
// so a human sees which binary answered, and are resolved afresh at every use.
type CompanionAddResult struct {
	Name string `json:"name" yaml:"name" toml:"name"`
	Bin  string `json:"bin" yaml:"bin" toml:"bin"`
	Path string `json:"path" yaml:"path" toml:"path"`
	// Added is false when the name was already registered.
	Added bool `json:"added" yaml:"added" toml:"added"`
}

// AddCompanion registers name in the config app writes (the home config, for
// `companion add`): it resolves name's binary on PATH and requires it to
// answer the loadout probe (companions.Verify) — the same exec a session runs
// — and records the NAME only. Nothing is written when the check fails.
func AddCompanion(ctx context.Context, app *App, name string) (CompanionAddResult, error) {
	if err := ctx.Err(); err != nil {
		return CompanionAddResult{}, err
	}
	r, err := companions.Verify(name)
	if err != nil {
		return CompanionAddResult{}, err
	}
	res := CompanionAddResult{Name: r.Name, Bin: r.Bin, Path: r.Path}
	_, err = app.Update(ctx, func(d *config.Draft) error {
		if slices.Contains(d.Companions, name) {
			return nil
		}
		d.Companions = append(d.Companions, name)
		res.Added = true
		return nil
	})
	if err != nil {
		return CompanionAddResult{}, err
	}
	return res, nil
}

// RemoveCompanion unregisters name from the config app writes, inside one
// Owner.Update transaction. A name that is not registered there is
// ErrCompanionNotRegistered.
func RemoveCompanion(ctx context.Context, app *App, name string) error {
	_, err := app.Update(ctx, func(d *config.Draft) error {
		i := slices.Index(d.Companions, name)
		if i < 0 {
			return fmt.Errorf("%w: %q (see `ctxloom companion list`)", ErrCompanionNotRegistered, name)
		}
		d.Companions = slices.Delete(d.Companions, i, i+1)
		return nil
	})
	return err
}

// CompanionListing is one registered companion and whether its binary
// resolves on PATH now.
type CompanionListing struct {
	Name     string `json:"name" yaml:"name" toml:"name"`
	Bin      string `json:"bin" yaml:"bin" toml:"bin"`
	Path     string `json:"path,omitempty" yaml:"path,omitempty" toml:"path,omitempty"`
	Resolves bool   `json:"resolves" yaml:"resolves" toml:"resolves"`
}

// ListCompanions resolves each registered name on PATH. It executes nothing.
func ListCompanions(names []string) []CompanionListing {
	out := make([]CompanionListing, 0, len(names))
	for _, name := range names {
		r, err := companions.Resolve(name)
		out = append(out, CompanionListing{Name: name, Bin: companions.BinaryName(name), Path: r.Path, Resolves: err == nil})
	}
	return out
}
