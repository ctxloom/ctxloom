package operations

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/shellenv"
)

// This file is the operations' read of the composed engine registry
// (engines.Registry) for the questions the CLI asks by NAME: does an engine
// exist, is it installed, which ships by default, what version is it.

// EngineExists reports whether name is a composed engine, by EXACT match.
func EngineExists(name string) bool {
	_, ok := engines.Registry().Lookup(engine.Name(name))
	return ok
}

// EngineNames lists every composed engine's name, sorted.
func EngineNames() []string {
	return namesOf(engines.Registry().Names(nil))
}

// EngineNamesWhere lists, sorted, the composed engines keep accepts.
func EngineNamesWhere(keep func(engine.Definition) bool) []string {
	return namesOf(engines.Registry().Names(keep))
}

func namesOf(names []engine.Name) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, string(n))
	}
	return out
}

// DefaultEngineName is the name of the one engine shipped by default — what
// an untyped llm entry, an init with no choice made, or a scaffold records.
// "" when no engine ships by default, which the composition refuses
// upstream.
func DefaultEngineName() string {
	def, err := engines.Registry().Default()
	if err != nil {
		return ""
	}
	return string(def.Root().Name)
}

// IsTestOnlyEngine reports whether name is a composed test/development
// double rather than a shippable engine. Every user-facing enumeration
// filters through this, so a new double hides everywhere at once. An
// unknown name is NOT test-only: callers distinguish "unknown engine" from
// "engine you may not pick" separately, and folding the two here would turn
// a typo into a silent omission.
func IsTestOnlyEngine(name string) bool {
	e, ok := engines.Registry().Lookup(engine.Name(name))
	return ok && e.Root().Distribution == engine.DistributionTestOnly
}

// EnginePermissionFacts reads the named engine's declared permission facts
// off its Definition. An unregistered name has the zero facts, which
// resolve to prompt-per-call and collapse plan.
func EnginePermissionFacts(name string) engine.PermissionFacts {
	e, ok := engines.Registry().Lookup(engine.Name(name))
	if !ok {
		return engine.PermissionFacts{}
	}
	return e.Root().Permissions
}

// EngineBinary is the native client binary the named engine's interactive
// grammar launches, "" for an engine with no declared grammar for it (a
// double, or an unregistered name).
func EngineBinary(name string) string {
	e, ok := engines.Registry().Lookup(engine.Name(name))
	if !ok {
		return ""
	}
	g, ok := engine.CLIFor(e.Root().CLI, engine.Interactive)
	if !ok {
		return ""
	}
	return g.Binary
}

// EngineAvailability resolves the named engine's binary and reports where it
// was found on PATH (or the login-shell PATH fallback, so a GUI-launched
// ctxloom with a minimal inherited PATH reports the same availability a
// terminal-launched one would), or the reason it could not be: "unregistered
// engine", "engine has no binary" and "binary not on PATH" are three
// different answers to `ctxloom init`'s "which engines can I offer".
func EngineAvailability(name string) (string, error) {
	binary := EngineBinary(name)
	if binary == "" {
		return "", fmt.Errorf("engine %q has no binary to resolve", name)
	}
	return shellenv.Resolve(binary)
}

// EngineAvailable is EngineAvailability's boolean; use that directly when
// the reason for unavailability matters.
func EngineAvailable(name string) bool {
	_, err := EngineAvailability(name)
	return err == nil
}

// engineVersionProber is the process-wide probe cache. One instance, so the
// fingerprint cache is shared across everything in a `ctxloom run` that
// might ask; per-call Probers would each re-exec the engine. It pairs the
// engine's resolved binary (EngineAvailability) with the version command its
// Definition declares (engine.Definition.Version). An unresolvable binary
// is *engineversion.BinaryAbsentError so a caller can tell "not installed"
// (ordinary) from "installed and misbehaved" (worth saying out loud).
var engineVersionProber = engineversion.NewProber(func(name string) (string, engineversion.Command, error) {
	e, ok := engines.Registry().Lookup(engine.Name(name))
	if !ok || !e.Root().Version.Declared() {
		return "", engineversion.Command{}, &engineversion.NoVersionCommandError{Engine: name}
	}
	binary, err := EngineAvailability(name)
	if err != nil {
		return "", engineversion.Command{}, &engineversion.BinaryAbsentError{Engine: name, Err: err}
	}
	v := e.Root().Version
	return binary, engineversion.Command{Args: v.Args, Parse: v.Parse}, nil
})

// ProbeEngineVersion reports the version the named engine's installed CLI
// says it is, through the shared cached prober. Every error is one of
// engineversion's typed refusals; there is no fallback value.
func ProbeEngineVersion(ctx context.Context, name string) (string, error) {
	return engineVersionProber.Probe(ctx, name)
}
