// This file is the registry half of the version probe: it pairs an engine's
// resolved binary with the version command its descriptor declares. The
// mechanism — lazy probing, fingerprint-keyed caching, the typed refusals —
// lives in internal/adapters/engineversion; the per-engine facts (which flag, how to
// read the answer — vendors print three different shapes, so one shared
// regex would be a guess) live in each engine's own descriptor.
package backends

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
)

// engineVersionProber is the process-wide probe cache. One instance, so the
// fingerprint cache is actually shared across everything in a `ctxloom run`
// that might ask; per-call Probers would each re-exec the engine.
var engineVersionProber = engineversion.NewProber(ResolveEngineVersionCommand)

// VersionCommandFor returns the named backend's declared version command, and
// whether it declares one at all. mock deliberately does not: it has no binary,
// so there is no single binary whose version would mean anything.
func VersionCommandFor(name string) (engineversion.Command, bool) {
	d, ok := lookup(name)
	if !ok {
		return engineversion.Command{}, false
	}
	return d.VersionCommand.Get()
}

// ResolveEngineVersionCommand is this registry's engineversion.Resolver: it
// pairs the engine's resolved binary (AvailabilityOf — the same PATH and
// login-shell-PATH resolution every other availability check uses) with the
// version command its descriptor declares.
//
// An unresolvable binary is reported as *engineversion.BinaryAbsentError so a
// caller can tell "this engine is not installed" (ordinary) from "this engine
// is installed and misbehaved" (worth saying out loud).
func ResolveEngineVersionCommand(engine string) (string, engineversion.Command, error) {
	cmd, ok := VersionCommandFor(engine)
	if !ok {
		return "", engineversion.Command{}, &engineversion.NoVersionCommandError{Engine: engine}
	}
	binary, err := AvailabilityOf(engine)
	if err != nil {
		return "", engineversion.Command{}, &engineversion.BinaryAbsentError{Engine: engine, Err: err}
	}
	return binary, cmd, nil
}

// ProbeEngineVersion reports the version the named engine's installed CLI says
// it is, through the shared cached prober. Every error is one of
// internal/adapters/engineversion's typed refusals; there is no fallback value.
func ProbeEngineVersion(ctx context.Context, engine string) (string, error) {
	return engineVersionProber.Probe(ctx, engine)
}
