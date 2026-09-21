package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validHome() HomeSpec {
	return HomeSpec{
		Vars: []HomeVar{{Name: "X_CONFIG_DIR", Subdir: "x"}},
		Credentials: Provide(CredentialSeed{
			Subdir:      "x",
			EnvTriggers: []string{"X_API_KEY"},
			LoginHint:   "x login",
			Files:       []SeedFile{{HostRelHome: ".x/creds.json", DestName: "creds.json", Required: true}},
			Accept:      []MaterialDelivery{MaterialDeliveryMounted, MaterialDeliveryReplicated},
		}),
	}
}

// TestHomeSpec_ZeroValue_IsTheNullObject: an engine that keeps no home
// returns the zero spec, and it validates — nothing is relocated, nothing is
// seeded, and no Declared slot has to be written for it.
func TestHomeSpec_ZeroValue_IsTheNullObject(t *testing.T) {
	var zero HomeSpec
	require.NoError(t, zero.Validate())
	assert.False(t, zero.Relocates())
	_, seeds := zero.Credentials.Get()
	assert.False(t, seeds)
}

// A seed without a var to land under is refused even on an otherwise-zero
// spec: there is nowhere for it to go.
func TestHomeSpec_Validate_RefusesASeedWithNoVar(t *testing.T) {
	h := HomeSpec{Credentials: Provide(CredentialSeed{Subdir: "x"})}
	assert.ErrorContains(t, h.Validate(), "no home var")
}

// A seed that names no delivery it accepts is refused HERE: the material and
// the way it may reach the instance are one declaration.
func TestHomeSpec_Validate_RefusesASeedAcceptingNothing(t *testing.T) {
	h := validHome()
	seed, _ := h.Credentials.Get()
	seed.Accept = nil
	h.Credentials = Provide(seed)
	assert.ErrorContains(t, h.Validate(), "Accept is empty")
}

func TestHomeSpec_Validate_AcceptsACompleteDeclaration(t *testing.T) {
	require.NoError(t, validHome().Validate())
}

func TestHomeSpec_Validate_RefusesEmptyVarFields(t *testing.T) {
	h := validHome()
	h.Vars[0].Subdir = ""
	assert.ErrorContains(t, h.Validate(), "Subdir")
	h = validHome()
	h.Vars[0].Name = ""
	assert.ErrorContains(t, h.Validate(), "Name")
}

// An undecided Credentials slot is the omission the type family exists to
// refuse: an engine with a relocatable home MUST say whether its credentials
// move with it.
func TestHomeSpec_Validate_RefusesUndecidedCredentials(t *testing.T) {
	h := validHome()
	h.Credentials = Declared[CredentialSeed]{}
	assert.ErrorContains(t, h.Validate(), "Credentials")
}

func TestHomeSpec_Validate_AcceptsAbsentCredentials(t *testing.T) {
	h := validHome()
	h.Credentials = Absent[CredentialSeed]("creds live in the OS keychain, which no env var relocates")
	assert.NoError(t, h.Validate())
}

// The seed's Subdir must be one the engine's own home var points at:
// otherwise the seed lands where the engine never looks, and the run starts
// logged out while reporting success.
func TestHomeSpec_Validate_RefusesSeedSubdirNoVarNames(t *testing.T) {
	h := validHome()
	seed, _ := h.Credentials.Get()
	seed.Subdir = "elsewhere"
	h.Credentials = Provide(seed)
	assert.ErrorContains(t, h.Validate(), "elsewhere")
}

func TestHomeSpec_Validate_RefusesSeedWithoutRequiredFile(t *testing.T) {
	h := validHome()
	seed, _ := h.Credentials.Get()
	seed.Files[0].Required = false
	h.Credentials = Provide(seed)
	assert.ErrorContains(t, h.Validate(), "Required")
}

func TestHomeSpec_Validate_RefusesSeedWithoutLoginHint(t *testing.T) {
	h := validHome()
	seed, _ := h.Credentials.Get()
	seed.LoginHint = ""
	h.Credentials = Provide(seed)
	assert.ErrorContains(t, h.Validate(), "LoginHint")
}

func validContainer() ContainerSpec {
	return ContainerSpec{
		Install:         []byte("RUN true\n"),
		ValidateCommand: "x --version",
		Auth: Provide(ContainerAuth{
			EnvTriggers:     []string{"X_API_KEY"},
			EnvPassthrough:  []string{"X_API_KEY", "X_BASE_URL"},
			CredentialFiles: []CredentialFile{{HostRelHome: ".x/creds.json", ContainerRelHome: ".x/creds.json"}},
			Hint:            "no X_API_KEY and no ~/.x credentials",
		}),
		OverlayDirs:        []string{".x"},
		TranscriptStoreRel: ".x/projects",
	}
}

func TestContainerSpec_Validate_AcceptsACompleteDeclaration(t *testing.T) {
	require.NoError(t, validContainer().Validate())
}

func TestContainerSpec_Validate_RefusesUndecidedAuth(t *testing.T) {
	c := validContainer()
	c.Auth = Declared[ContainerAuth]{}
	assert.ErrorContains(t, c.Validate(), "Auth")
}

func TestContainerSpec_Validate_RefusesInstallWithoutValidate(t *testing.T) {
	c := validContainer()
	c.ValidateCommand = ""
	assert.ErrorContains(t, c.Validate(), "ValidateCommand")
}

// A vendorless auth (mock: authenticates against nothing) must not ALSO name
// triggers or files: the two shapes mean opposite things at resolve time and
// a declaration carrying both has no single reading.
func TestContainerAuth_Validate_VendorlessExcludesTriggersAndFiles(t *testing.T) {
	a := ContainerAuth{Vendorless: "authenticates against no vendor", EnvTriggers: []string{"X"}}
	assert.ErrorContains(t, a.Validate(), "Vendorless")
	a = ContainerAuth{Vendorless: "authenticates against no vendor"}
	assert.NoError(t, a.Validate())
}

// A vendor-backed auth with neither a trigger nor a credential file can never
// resolve — every containerized run of it would degrade. That is a
// declaration of "unknown", which is what Absent is for.
func TestContainerAuth_Validate_RefusesNothingToResolve(t *testing.T) {
	a := ContainerAuth{Hint: "no way in"}
	assert.ErrorContains(t, a.Validate(), "resolve")
}

func TestContainerAuth_Validate_RefusesMissingHint(t *testing.T) {
	a := ContainerAuth{EnvTriggers: []string{"X_API_KEY"}}
	assert.ErrorContains(t, a.Validate(), "Hint")
}
