// Package enginefixture builds COMPLETE synthetic engines for tests: a
// hosting record with every Declared slot decided absent and a stub backend
// that runs nothing, paired with a mock KIND under the same name. A test
// that needs one capability provides it on top. Test support only — never
// linked into a binary (tests/arch gates the import).
package enginefixture

import (
	"context"
	"io"

	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/lm/hosting"
)

// Kind returns the synthetic engine's kind: the mock kind under name, so
// its Definition is complete and valid; opts adjust it.
func Kind(name string, opts ...mock.Option) engine.Engine {
	return mock.NewNamed(engine.Name(name), opts...)
}

// Registry composes a Kind for every hosting record handed to it, so a test
// can register fixtures with backends.Register(Registry(h...), h...).
func Registry(hostings ...hosting.Hosting) engine.Registry {
	kinds := make([]engine.Engine, 0, len(hostings))
	for _, h := range hostings {
		kinds = append(kinds, Kind(string(h.Engine)))
	}
	return RegistryOf(kinds...)
}

// RegistryOf composes the kinds into a Registry; a duplicate name is a
// fixture bug and panics.
func RegistryOf(kinds ...engine.Engine) engine.Registry {
	reg, err := engine.NewRegistry(kinds...)
	if err != nil {
		panic("enginefixture: " + err.Error())
	}
	return reg
}

// Hosting returns a valid hosting record for a synthetic engine named name.
func Hosting(name string) hosting.Hosting {
	return hosting.Hosting{
		Engine:     engine.Name(name),
		NewBackend: func(agent.Launcher) agent.Backend { return &stubBackend{name: name} },
		NewConfig:  func() agent.BackendConfig { return &stubConfig{name: name} },
		Surfaces:   agent.Declaration{},
		SettingsWriter: agent.Absent[func(agent.SettingsOptions) agent.SettingsWriter](
			name + " (fixture) writes no settings"),
		InstanceConfig: agent.Absent[func(agent.SettingsOptions) agent.InstanceConfigWriter](
			name + " (fixture) generates no instance config"),
		CommandExports:  agent.Absent[func([]*bundles.LoadedContent) []agent.CommandExport](name + " (fixture) exports no commands"),
		SkillExports:    agent.Absent[func([]*bundles.LoadedSkill) []agent.SkillExport](name + " (fixture) exports no skills"),
		HookGlobalScope: agent.Absent[hosting.HookGlobalScope](name + " (fixture) has no global settings path"),
		VersionCommand:  agent.Absent[engineversion.Command](name + " (fixture) has no binary to ask"),
		Home:            agent.Absent[agent.EngineHome](name + " (fixture) keeps no global state"),
		Provisioning: agent.Absent[agent.ProvisioningPolicy](
			name + " (fixture) has no credential material to provision"),
		Container:         agent.Absent[agent.EngineContainer](name + " (fixture) has no container story"),
		TranscriptReaders: agent.Absent[[]vendorreader.VersionedAdapter](name + " (fixture) keeps no transcripts"),
	}
}

type stubConfig struct{ name string }

func (c *stubConfig) BackendType() string { return c.name }

// stubBackend is the least agent.Backend that satisfies the contract.
type stubBackend struct{ name string }

func (b *stubBackend) Name() string                                     { return b.name }
func (b *stubBackend) Version() string                                  { return "0" }
func (b *stubBackend) SupportedModes() []agent.ExecutionMode            { return nil }
func (b *stubBackend) History() agent.SessionHistory                    { return nil }
func (b *stubBackend) Setup(context.Context, *agent.SetupRequest) error { return nil }
func (b *stubBackend) Execute(context.Context, *agent.ExecuteRequest, io.Writer, io.Writer) (*agent.ExecuteResult, error) {
	return &agent.ExecuteResult{}, nil
}
func (b *stubBackend) Cleanup(context.Context) error { return nil }
