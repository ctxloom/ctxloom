package content

// The six surface types this package ships. Registration is the ONLY place the
// set of kinds is enumerated: there is no content.Kind enum and no data table
// mirroring these entries, so a seventh kind is added by calling Register and
// nothing here changes.
func init() {
	Register(fragmentType{})
	Register(commandType{})
	Register(mcpType{})
	Register(hookType{})
	Register(skillType{})
	Register(profileType{})
}

// Compile-time proof that each type satisfies the registry contract.
var (
	_ SurfaceType = fragmentType{}
	_ SurfaceType = commandType{}
	_ SurfaceType = mcpType{}
	_ SurfaceType = hookType{}
	_ SurfaceType = skillType{}
	_ SurfaceType = profileType{}
)
