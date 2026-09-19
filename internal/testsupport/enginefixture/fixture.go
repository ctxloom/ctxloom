// Package enginefixture builds COMPLETE engine descriptors for synthetic
// engines in tests: every Declared slot decided absent, a stub backend that
// runs nothing. A test that needs one capability provides it on top. Test
// support only — never linked into a binary (tests/arch gates the import).
package enginefixture

import (
	"context"
	"io"

	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	coreengine "github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/lm/engine"
)

// Descriptor returns a valid descriptor for a synthetic engine named name.
func Descriptor(name string) engine.Descriptor {
	return engine.Descriptor{
		Name:       name,
		NewBackend: func(agent.Launcher) agent.Backend { return &stubBackend{name: name} },
		NewConfig:  func() agent.BackendConfig { return &stubConfig{name: name} },
		Surfaces:   agent.Declaration{},
		SettingsWriter: agent.Absent[func(agent.SettingsOptions) agent.SettingsWriter](
			name + " (fixture) writes no settings"),
		InstanceConfig: agent.Absent[func(agent.SettingsOptions) agent.InstanceConfigWriter](
			name + " (fixture) generates no instance config"),
		CommandExports:  agent.Absent[func([]*bundles.LoadedContent) []agent.CommandExport](name + " (fixture) exports no commands"),
		SkillExports:    agent.Absent[func([]*bundles.LoadedSkill) []agent.SkillExport](name + " (fixture) exports no skills"),
		HookGlobalScope: agent.Absent[engine.HookGlobalScope](name + " (fixture) has no global settings path"),
		VersionCommand:  agent.Absent[engineversion.Command](name + " (fixture) has no binary to ask"),
		Home:            agent.Absent[agent.EngineHome](name + " (fixture) keeps no global state"),
		Provisioning: agent.Absent[agent.ProvisioningPolicy](
			name + " (fixture) has no credential material to provision"),
		Container:         agent.Absent[agent.EngineContainer](name + " (fixture) has no container story"),
		TranscriptReaders: agent.Absent[[]vendorreader.VersionedAdapter](name + " (fixture) keeps no transcripts"),
		Distribution:      coreengine.DistributionTestOnly,
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
