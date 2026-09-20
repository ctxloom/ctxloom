package hosting

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

type fixtureConfig struct{}

func (fixtureConfig) BackendType() string { return "fixture" }

// validHosting is a complete declaration: every Declared slot decided,
// every required field set. Each refusal test below breaks exactly one thing.
func validHosting() Hosting {
	return Hosting{
		Engine:     "fixture",
		NewBackend: func(agent.Launcher) agent.Backend { return nil },
		NewConfig:  func() agent.BackendConfig { return &fixtureConfig{} },
		Surfaces:   agent.Declaration{},
		SettingsWriter: engine.Absent[func(agent.SettingsOptions) agent.SettingsWriter](
			"fixture writes no settings"),
		HookGlobalScope: engine.Absent[HookGlobalScope]("fixture's global path never collapses onto its project path"),
		VersionCommand:  engine.Absent[engineversion.Command]("fixture has no binary to ask"),
	}
}

func TestValidate_AcceptsACompleteHosting(t *testing.T) {
	require.NoError(t, validHosting().Validate())
}

// TestValidate_EveryDeclaredSlotIsGated is the load-bearing test: it finds
// every Declared field on Hosting BY REFLECTION, zeroes each in turn, and
// requires Validate to refuse it BY NAME. A new Declared slot is therefore
// gated the moment it is added, with no list here or in Validate to update.
func TestValidate_EveryDeclaredSlotIsGated(t *testing.T) {
	typ := reflect.TypeOf(Hosting{})
	gated := 0
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.Type.Implements(reflect.TypeOf((*decided)(nil)).Elem()) {
			continue
		}
		gated++
		t.Run(f.Name, func(t *testing.T) {
			d := validHosting()
			reflect.ValueOf(&d).Elem().Field(i).Set(reflect.Zero(f.Type))
			err := d.Validate()
			require.Error(t, err, "zeroing %s must be refused", f.Name)
			assert.ErrorContains(t, err, f.Name)
		})
	}
	// The count is asserted so the loop cannot pass vacuously if the
	// interface match ever stops finding the slots.
	assert.GreaterOrEqual(t, gated, 3, "expected every optional capability to be a Declared slot")
}

func TestValidate_RefusesAnUnnamedEngine(t *testing.T) {
	d := validHosting()
	d.Engine = ""
	assert.ErrorContains(t, d.Validate(), "Engine")
}

func TestValidate_RefusesMissingConstructors(t *testing.T) {
	d := validHosting()
	d.NewBackend = nil
	assert.ErrorContains(t, d.Validate(), "NewBackend")
	d = validHosting()
	d.NewConfig = nil
	assert.ErrorContains(t, d.Validate(), "NewConfig")
	d = validHosting()
	d.Surfaces = nil
	assert.ErrorContains(t, d.Validate(), "Surfaces")
}

// A slot PROVIDED with a nil func is the one omission Declared cannot see
// (presence is what the author said); Validate is where it is caught.
func TestValidate_RefusesProvidedNilFunc(t *testing.T) {
	d := validHosting()
	d.SettingsWriter = engine.Provide[func(agent.SettingsOptions) agent.SettingsWriter](nil)
	assert.ErrorContains(t, d.Validate(), "SettingsWriter")
}

func TestValidate_RefusesIncompleteProvidedVersionCommand(t *testing.T) {
	d := validHosting()
	d.VersionCommand = engine.Provide(engineversion.Command{Args: []string{"--version"}})
	assert.ErrorContains(t, d.Validate(), "VersionCommand")
}

func TestValidate_RefusesIncompleteProvidedHookGlobalScope(t *testing.T) {
	d := validHosting()
	d.HookGlobalScope = engine.Provide(HookGlobalScope{Label: "x"})
	assert.ErrorContains(t, d.Validate(), "HookGlobalScope")
}
