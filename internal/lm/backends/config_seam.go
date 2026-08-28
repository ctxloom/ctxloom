package backends

import "github.com/ctxloom/ctxloom/internal/config"

// loadConfigFn is the config-load seam, in the idiom this package already uses
// for getBuiltinCommandFn: a package-level function value a test can swap.
//
// It exists because the failure it guards is otherwise UNTESTABLE. config.Load
// resolves ambiently, and a test cannot reliably make it fail — pointing cwd at
// a project whose config.yaml is malformed does not error (it is tolerated),
// and neither does making that file unreadable. Both were tried. Without this
// seam the "a config-load failure must be a finding" behaviour could only be
// asserted by reading the code, which is not a gate.
var loadConfigFn = config.Load
