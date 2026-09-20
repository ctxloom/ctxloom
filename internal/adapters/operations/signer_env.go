package operations

import (
	"os"

	"github.com/ctxloom/ctxloom/internal/adapters/signing/agentkey"
)

// sshAuthSockEnv is ssh-agent's own contract for where its socket is, read
// once here and handed to the signing adapter as a value.
const sshAuthSockEnv = "SSH_AUTH_SOCK"

// SignerDiscoverer composes the local signing-key discoverer over the host's
// facts: the user's home for a "~" in the configured key path and the
// ssh-agent socket. The signing adapter reads neither for itself.
func SignerDiscoverer() (*agentkey.Discoverer, error) {
	host, err := hostFacts()
	if err != nil {
		return nil, err
	}
	return agentkey.NewDiscoverer(agentkey.Env{Home: host.Home, AgentSocket: os.Getenv(sshAuthSockEnv)}), nil
}
