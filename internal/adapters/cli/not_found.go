package cli

import "github.com/ctxloom/ctxloom/internal/shared/report"

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
