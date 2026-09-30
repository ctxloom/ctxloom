package operations

import (
	"context"
	"fmt"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"maps"
	"slices"

	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/shellenv"
)

// This file is the operations' read of the composed engine registry
// (the App's engine.Registry) for the questions the CLI asks by NAME: does an engine
// exist, is it installed, which ships by default, what version is it.

// EngineExists reports whether name is a composed engine, by EXACT match.
func EngineExists(reg engine.Registry, name string) bool {
	_, ok := reg.Lookup(engine.Name(name))
	return ok
}

// EngineNames lists every composed engine's name, sorted.
func EngineNames(reg engine.Registry) []string {
	return namesOf(reg.Names(nil))
}

// EngineNamesWhere lists, sorted, the composed engines keep accepts.
func EngineNamesWhere(reg engine.Registry, keep func(engine.Definition) bool) []string {
	return namesOf(reg.Names(keep))
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
func DefaultEngineName(reg engine.Registry) string {
	def, err := reg.Default()
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
func IsTestOnlyEngine(reg engine.Registry, name string) bool {
	e, ok := reg.Lookup(engine.Name(name))
	return ok && e.Root().Distribution == engine.DistributionTestOnly
}

// EffectivePosture names the posture an unflagged run of backend resolves
// to over a binding's and a label's declarations, in the engine's own
// vocabulary (its PermissionModel resolves and names it); "" when the
// engine has no permission model, or the declarations are ones the launch
// refuses.
func EffectivePosture(reg engine.Registry, backend string, binding agents.Permissions, label agents.LabelPermissions) string {
	kind, ok := reg.Lookup(engine.Name(backend))
	if !ok {
		return ""
	}
	model, ok := kind.Permissions().Get()
	if !ok {
		return ""
	}
	var decls []engine.Declaration
	if block, ok := binding.Engines[backend]; ok {
		decls = append(decls, engine.Declaration{Document: block, From: "the agent"})
	} else if len(binding.Engines) > 0 {
		return ""
	}
	if len(label.Engine) > 0 {
		decls = append(decls, engine.Declaration{Document: label.Engine, From: "the llm label"})
	}
	doc, err := model.Resolve(engine.PostureRequest{Declared: decls})
	if err != nil {
		return ""
	}
	return PostureName(reg, engine.Posture{Engine: engine.Name(backend), Document: doc})
}

// PostureName names a resolved posture in its engine's own words; "" when
// the engine has no permission model or cannot read the document.
func PostureName(reg engine.Registry, p engine.Posture) string {
	kind, ok := reg.Lookup(p.Engine)
	if !ok {
		return ""
	}
	model, ok := kind.Permissions().Get()
	if !ok {
		return ""
	}
	name, err := model.Decode(p.Document)
	if err != nil {
		return ""
	}
	return name
}

// PostureNames are every registered engine's mode vocabulary, deduplicated
// and sorted: what `run --permissions` may name before the engine is known.
func PostureNames(reg engine.Registry) []string {
	seen := map[string]bool{}
	for _, n := range reg.Names(nil) {
		kind, _ := reg.Lookup(n)
		if model, ok := kind.Permissions().Get(); ok {
			for _, p := range model.Postures() {
				seen[p] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// EngineBinary is the native client binary the named engine's interactive
// grammar launches, "" for an engine with no declared grammar for it (a
// double, or an unregistered name).
func EngineBinary(reg engine.Registry, name string) string {
	e, ok := reg.Lookup(engine.Name(name))
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
func EngineAvailability(reg engine.Registry, name string) (string, error) {
	binary := EngineBinary(reg, name)
	if binary == "" {
		return "", fmt.Errorf("engine %q has no binary to resolve", name)
	}
	return shellenv.Resolve(binary)
}

// EngineAvailable is EngineAvailability's boolean; use that directly when
// the reason for unavailability matters.
func EngineAvailable(reg engine.Registry, name string) bool {
	_, err := EngineAvailability(reg, name)
	return err == nil
}

// newEngineVersionProber is the probe cache over reg. The App holds one
// (App.ProbeEngineVersion), so the fingerprint cache is shared across
// everything in a `ctxloom run` that might ask; per-call Probers would each
// re-exec the engine. It pairs the engine's resolved binary
// (EngineAvailability) with the version command its Definition declares
// (engine.Definition.Version). An unresolvable binary is
// *engineversion.BinaryAbsentError so a caller can tell "not installed"
// (ordinary) from "installed and misbehaved" (worth saying out loud).
func newEngineVersionProber(reg engine.Registry) *engineversion.Prober {
	return engineversion.NewProber(func(name string) (string, engineversion.Command, error) {
		e, ok := reg.Lookup(engine.Name(name))
		if !ok || !e.Root().Version.Declared() {
			return "", engineversion.Command{}, &engineversion.NoVersionCommandError{Engine: name}
		}
		binary, err := EngineAvailability(reg, name)
		if err != nil {
			return "", engineversion.Command{}, &engineversion.BinaryAbsentError{Engine: name, Err: err}
		}
		v := e.Root().Version
		return binary, engineversion.Command{Args: v.Args, Parse: v.Parse}, nil
	})
}

// ProbeEngineVersion reports the version the named engine's installed CLI
// says it is, through this App's shared cached prober. Every error is one of
// engineversion's typed refusals; there is no fallback value.
func (a *App) ProbeEngineVersion(ctx context.Context, name string) (string, error) {
	a.proberOnce.Do(func() { a.prober = newEngineVersionProber(a.engines) })
	return a.prober.Probe(ctx, name)
}
