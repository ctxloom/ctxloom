package operations

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// buildCountersignRecords resolves the two countersignature stores for cfg
// and pairs them with the trust root their records verify against
// (countersign.NewRecords). Injected stores and root (test seams, or a
// caller that already resolved them) win outright; production builds the
// on-disk stores from cfg — the same pair config.Sources.TrustPorts builds
// for the generation, so a mutation reads back exactly what the gate reads.
func buildCountersignRecords(cfg *config.Config, fs afero.Fs, injectedUser, injectedProject *countersign.Store, injectedRoot trust.TrustRoot) countersign.Records {
	f := getFS(fs)
	user := injectedUser
	var fault error
	if user == nil {
		userDir, err := countersign.HomeDir()
		if err != nil {
			clidiag.Warn("ctxloom", "cannot locate the user approvals store (%v) — every personal approval and rejection is unreadable this session", err)
			fault = err
			userDir = ""
		}
		user = countersign.NewStore(userDir, f)
	}
	project := injectedProject
	if project == nil {
		project = countersign.NewStore(paths.ApprovalsPath(getBaseDir(cfg)), f)
	}
	return countersign.NewRecords(user, project, reviewTrustRoot(cfg, injectedRoot), fault)
}
