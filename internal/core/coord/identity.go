package coord

import "github.com/ctxloom/ctxloom/internal/core/sessions"

// The env keys of the per-spawn seam are sessions' constants: declared once
// there, carried here under this package's established names. The credential
// is read from the HARNESS-INHERITED process env only — it is never written
// into any MCP config structure, file, or Env map.
const (
	EnvCoordURL    = sessions.EnvCoordURL
	EnvCoordCred   = sessions.EnvCoordCred
	EnvRunID       = sessions.EnvRunID
	EnvMCPSocket   = sessions.EnvMCPSocket
	EnvRunDepth    = sessions.EnvRunDepth
	EnvRunOneShot  = sessions.EnvRunOneShot
	EnvCellWorkDir = sessions.EnvCellWorkDir
)

// Identity is sessions.Identity: what a credential authenticates AND
// identifies. The type is declared once, in core/sessions; this alias carries
// the coordinator's established name forward for its callers.
type Identity = sessions.Identity
