package acp

import "github.com/ctxloom/ctxloom/internal/shared/agent"

// This file carries the placeholder launch capabilities the embedded
// agent.LaunchBackend requires. The generic ACP backend's job in this increment
// is the StructuredChat driver (session.go); the setup-side capabilities are
// stubs until config-materialization delegation to the target agent is designed
// (see doc.go's registration TODO).
//
// Session history is NOT one of them: acp passes nil for SessionHistory (see
// NewACP), the same declaration every other engine makes, so a caller is told
// "backend acp has no session history" instead of receiving an empty list it
// cannot distinguish from a workspace that genuinely has none (U011-F02).

// acpCommands is a no-op ContentCommands: a generic ACP client has no native
// command (slash-command) format of its own — that belongs to the target
// agent, so command materialization will delegate there once registration is
// wired.
type acpCommands struct{}

func (s *acpCommands) RegisterFromContent(workDir string, cmds []agent.CommandExport) error {
	return nil
}

