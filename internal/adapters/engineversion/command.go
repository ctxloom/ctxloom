package engineversion

import "github.com/ctxloom/ctxloom/internal/core/engine"

// LeadingToken is the version reading of an engine CLI that answers
// `<binary> --version` with its bare version first (TokenAt position 0),
// held to floor: the minimum version ctxloom drives. An engine's root-only
// adapter package hands it to the kind (Definition.Version).
func LeadingToken(floor string) engine.VersionCommand {
	return engine.VersionCommand{
		Args:  []string{"--version"},
		Parse: func(output string) (string, error) { return TokenAt(output, 0) },
		Floor: floor,
	}
}
