package mcp

import "github.com/ctxloom/ctxloom/internal/adapters/operations"

// The host-relayed tools' contract lives in operations (the application
// services that answer them); these are that contract under this package's
// names, so the stdio handlers and the runner's relays advertise one shape.
type (
	compactSessionInput     = operations.CompactSessionInput
	loadSessionInput        = operations.LoadSessionInput
	recoverSessionInput     = operations.RecoverSessionInput
	getPreviousSessionInput = operations.GetPreviousSessionInput
	listSessionsInput       = operations.ListSessionsInput
	contextStatusInput      = operations.ContextStatusInput
	evaluateTriggersInput   = operations.EvaluateTriggersInput
)

const (
	compactSessionDesc     = operations.CompactSessionDesc
	listSessionsDesc       = operations.ListSessionsDesc
	loadSessionDesc        = operations.LoadSessionDesc
	recoverSessionDesc     = operations.RecoverSessionDesc
	getPreviousSessionDesc = operations.GetPreviousSessionDesc
	contextStatusDesc      = operations.ContextStatusDesc
	evaluateTriggersDesc   = operations.EvaluateTriggersDesc
)
