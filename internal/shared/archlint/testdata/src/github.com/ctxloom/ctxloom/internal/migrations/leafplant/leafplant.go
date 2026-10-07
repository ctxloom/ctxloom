// Package leafplant is a migration step that reaches past schemaver and the
// yamlx node helpers, which the production layering table forbids.
package leafplant

import (
	"github.com/ctxloom/ctxloom/internal/shared/safefs" // want `imports github.com/ctxloom/ctxloom/internal/shared/safefs, which layering rule "migrations-are-leaves" forbids`
)

// Write uses the import.
var Write = safefs.WriteFile
