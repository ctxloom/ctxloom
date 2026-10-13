package kit

import "github.com/ctxloom/ctxloom/internal/core/engine"

// ResolveDegraded is a permission model's Resolve over its own resolution:
// a posture that does not resolve is the error, unless the request runs
// --degraded, which drops it to floor with a warning naming why.
func ResolveDegraded(req engine.PostureRequest, resolve func(engine.PostureRequest) (map[string]any, error), floor map[string]any) (map[string]any, error) {
	out, err := resolve(req)
	if err != nil && req.Degraded {
		if req.Warn != nil {
			req.Warn("--degraded: %v, so this run drops to the plan floor", err)
		}
		return floor, nil
	}
	return out, err
}
