package opencode

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// tuiConfigNames are the TUI preference files opencode reads in a config dir.
var tuiConfigNames = []string{"tui.json", "tui.jsonc"}

// tuiConfigCarry is the instance config (Q-I): the user's own TUI
// preferences (theme, keybinds), read from <XDG_CONFIG_HOME>/opencode,
// copied verbatim into the session home H. The relocated config base owns
// its opencode dir, so without the carry the TUI under ctxloom silently
// loses them; H is OPENCODE_CONFIG_DIR, the TUI config source opencode reads
// last. A copy the user has since removed is removed too. A `plugin` list in
// the user's file makes opencode install it at startup, as it would outside
// ctxloom. getenv is the test seam (nil = os.Getenv).
type tuiConfigCarry struct{ getenv func(string) string }

func (c tuiConfigCarry) WriteInstanceConfig(req engine.InstanceConfigRequest, root safefs.Root) (engine.InstanceConfigReport, error) {
	var rep engine.InstanceConfigReport
	if req.InstanceHome == "" {
		return rep, fmt.Errorf("%s instance config: no session home to carry the TUI preferences into", EngineName)
	}
	getenv := c.getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	base, ok := engine.UserXDGBase("XDG_CONFIG_HOME", getenv, req.HostHome)
	if !ok {
		rep.Warnings = append(rep.Warnings, "the user's config dir could not be resolved, so the TUI starts with opencode's default preferences")
		return rep, nil
	}
	for _, name := range tuiConfigNames {
		src, dest := filepath.Join(base, appDir, name), filepath.Join(req.InstanceHome, name)
		data, err := afero.ReadFile(root.Fs, src)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := root.Fs.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return rep, fmt.Errorf("%s instance config: remove the stale %s: %w", EngineName, dest, err)
			}
			continue
		case err != nil:
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("cannot read %s (%v), so the TUI starts without it", src, err))
			continue
		}
		if err := safefs.WriteFile(root.Fs, dest, data, safefs.PrivateFileMode, safefs.AllowEmpty()); err != nil {
			return rep, fmt.Errorf("%s instance config: write %s: %w", EngineName, dest, err)
		}
		rep.Wrote = append(rep.Wrote, dest)
	}
	return rep, nil
}
