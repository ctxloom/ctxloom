package mcp

import "github.com/ctxloom/ctxloom/internal/adapters/operations"

// The host-relayed tools' contract lives in operations (the application
// services that answer them); these are that contract under this package's
// names, so the relay's handlers decode the shape the runner's relays
// advertise.
type (
	contextStatusInput    = operations.ContextStatusInput
	evaluateTriggersInput = operations.EvaluateTriggersInput
)
