package tsuser // want package:"reaches test-only tree via github.com/ctxloom/ctxloom/internal/testsupport"

// tsuser imports the test-only tree from production code.

import "github.com/ctxloom/ctxloom/internal/testsupport" // want `package internal/tsuser imports github.com/ctxloom/ctxloom/internal/testsupport from a production file`

// Use pulls the helper into production.
func Use() { testsupport.Helper() }
