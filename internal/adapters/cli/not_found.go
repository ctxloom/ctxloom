package cli

import (
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// The one next command a miss on a bundle or session name names: the listing
// of the names that do exist.
const (
	bundleListFix  = "`ctxloom bundle list` names the installed bundles"
	sessionListFix = "`ctxloom session list` names this project's sessions (add --all for every project)"
)

// errNoBundle refuses a bundle name nothing installed carries.
func errNoBundle(name string) error {
	return report.Errorf(bundleListFix, "bundle not found: %s", name)
}

// errNoSession refuses a session name no recorded session carries.
func errNoSession(name string) error {
	return report.Errorf(sessionListFix, "no session named %q", name)
}

// The hint an empty content listing ends with. Inside a project the next step
// is adding content to a profile and pulling it; outside one (the config
// resolved to the home fallback) there is no project to add to, and the next
// step is making one.
const (
	addContentListingHint = "Add remote bundles to a profile (ctxloom profile create/modify), then ctxloom deps pull"
	noProjectListingHint  = "No project here: run 'ctxloom init' to set one up with its default content."
)

// emptyListingHint picks the hint for an empty content listing under cfg.
func emptyListingHint(cfg *config.Config) string {
	if cfg.Source() == config.SourceHome {
		return noProjectListingHint
	}
	return addContentListingHint
}
