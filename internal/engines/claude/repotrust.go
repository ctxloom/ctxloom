package claude

import (
	"errors"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// Trust is claude's repository-trust verdict: claude's own record.
func (c Claude) Trust() engine.Declared[engine.RepoTrust] {
	return engine.Provide[engine.RepoTrust](claudeRepoTrust{})
}

type claudeRepoTrust struct{}

func (claudeRepoTrust) Verdict(fs afero.Fs, q engine.TrustQuery) (engine.WorkspaceTrust, error) {
	return engine.TrustUntrusted, nil
}

var errUntrustedSettingsPresented = errors.New("claude: untrusted")
