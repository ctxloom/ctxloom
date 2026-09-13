package engine

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/engineversion"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

type fixtureConfig struct{}

func (fixtureConfig) BackendType() string { return "fixture" }

// validDescriptor is a complete declaration: every Declared slot decided,
// every required field set. Each refusal test below breaks exactly one thing.
func validDescriptor() Descriptor {
	return Descriptor{
		Name:       "fixture",
		Aliases:    []string{"fix"},
		NewBackend: func(agent.Launcher) agent.Backend { return nil },
		NewConfig:  func() agent.BackendConfig { return &fixtureConfig{} },
		Surfaces:   agent.Declaration{},
		SettingsWriter: agent.Absent[func(agent.SettingsOptions) agent.SettingsWriter](
			"fixture writes no settings"),
		InstanceConfig: agent.Absent[func(agent.SettingsOptions) agent.InstanceConfigWriter](
			"fixture generates no instance config"),
		CredentialProjector: agent.Absent[func() agent.CredentialProjector]("fixture has no credentials"),
		CommandExports:      agent.Absent[func([]*bundles.LoadedContent) []agent.CommandExport]("fixture exports no commands"),
		SkillExports:        agent.Absent[func([]*bundles.LoadedSkill) []agent.SkillExport]("fixture exports no skills"),
		HookGlobalScope:     agent.Absent[HookGlobalScope]("fixture's global path never collapses onto its project path"),
		VersionCommand:      agent.Absent[engineversion.Command]("fixture has no binary to ask"),
		Home:                agent.Absent[agent.EngineHome]("fixture keeps no global state"),
		Container:           agent.Absent[agent.EngineContainer]("fixture has no container story"),
		TranscriptReaders:   agent.Absent[[]vendorreader.VersionedAdapter]("fixture keeps no transcripts"),
	}
}

func TestValidate_AcceptsACompleteDescriptor(t *testing.T) {
	require.NoError(t, validDescriptor().Validate())
}

// TestValidate_EveryDeclaredSlotIsGated is the load-bearing test: it finds
// every Declared field on Descriptor BY REFLECTION, zeroes each in turn, and
// requires Validate to refuse it BY NAME. A new Declared slot is therefore
// gated the moment it is added, with no list here or in Validate to update.
func TestValidate_EveryDeclaredSlotIsGated(t *testing.T) {
	typ := reflect.TypeOf(Descriptor{})
	gated := 0
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.Type.Implements(reflect.TypeOf((*decided)(nil)).Elem()) {
			continue
		}
		gated++
		t.Run(f.Name, func(t *testing.T) {
			d := validDescriptor()
			reflect.ValueOf(&d).Elem().Field(i).Set(reflect.Zero(f.Type))
			err := d.Validate()
			require.Error(t, err, "zeroing %s must be refused", f.Name)
			assert.ErrorContains(t, err, f.Name)
		})
	}
	// The count is asserted so the loop cannot pass vacuously if the
	// interface match ever stops finding the slots.
	assert.GreaterOrEqual(t, gated, 10, "expected every optional capability to be a Declared slot")
}

func TestValidate_RefusesEmptyOrUppercaseName(t *testing.T) {
	d := validDescriptor()
	d.Name = ""
	assert.ErrorContains(t, d.Validate(), "Name")
	d.Name = "Fixture"
	assert.ErrorContains(t, d.Validate(), "lowercase")
}

func TestValidate_RefusesAliasEqualToNameOrUppercase(t *testing.T) {
	d := validDescriptor()
	d.Aliases = []string{"fixture"}
	assert.ErrorContains(t, d.Validate(), "alias")
	d.Aliases = []string{"Fix"}
	assert.ErrorContains(t, d.Validate(), "lowercase")
	d.Aliases = []string{"fix", "fix"}
	assert.ErrorContains(t, d.Validate(), "twice")
}

func TestValidate_RefusesMissingConstructors(t *testing.T) {
	d := validDescriptor()
	d.NewBackend = nil
	assert.ErrorContains(t, d.Validate(), "NewBackend")
	d = validDescriptor()
	d.NewConfig = nil
	assert.ErrorContains(t, d.Validate(), "NewConfig")
	d = validDescriptor()
	d.Surfaces = nil
	assert.ErrorContains(t, d.Validate(), "Surfaces")
}

// A slot PROVIDED with a nil func is the one omission Declared cannot see
// (presence is what the author said); Validate is where it is caught.
func TestValidate_RefusesProvidedNilFunc(t *testing.T) {
	d := validDescriptor()
	d.SettingsWriter = agent.Provide[func(agent.SettingsOptions) agent.SettingsWriter](nil)
	assert.ErrorContains(t, d.Validate(), "SettingsWriter")
	d = validDescriptor()
	d.CredentialProjector = agent.Provide[func() agent.CredentialProjector](nil)
	assert.ErrorContains(t, d.Validate(), "CredentialProjector")
}

func TestValidate_RefusesIncompleteProvidedVersionCommand(t *testing.T) {
	d := validDescriptor()
	d.VersionCommand = agent.Provide(engineversion.Command{Args: []string{"--version"}})
	assert.ErrorContains(t, d.Validate(), "VersionCommand")
}

func TestValidate_RefusesIncompleteProvidedHookGlobalScope(t *testing.T) {
	d := validDescriptor()
	d.HookGlobalScope = agent.Provide(HookGlobalScope{Label: "x"})
	assert.ErrorContains(t, d.Validate(), "HookGlobalScope")
}

// Home and Container carry their own Validate; Descriptor.Validate must run
// it, or an engine could register a home whose seed lands nowhere.
func TestValidate_RunsHomeAndContainerValidation(t *testing.T) {
	d := validDescriptor()
	d.Home = agent.Provide(agent.EngineHome{})
	assert.ErrorContains(t, d.Validate(), "EngineHome")
	d = validDescriptor()
	d.Container = agent.Provide(agent.EngineContainer{})
	assert.ErrorContains(t, d.Validate(), "EngineContainer")
}

// InTreeAgentHome derivation reads Vars[0]; a second var has no consumer
// yet, so it is refused rather than silently half-applied.
func TestValidate_RefusesMultiVarHomeUntilAConsumerExists(t *testing.T) {
	d := validDescriptor()
	d.Home = agent.Provide(agent.EngineHome{
		Vars:        []agent.HomeVar{{EnvVar: "A", Subdir: "a"}, {EnvVar: "B", Subdir: "b"}},
		Credentials: agent.Absent[agent.CredentialSeed]("elsewhere"),
	})
	assert.ErrorContains(t, d.Validate(), "one home var")
}
