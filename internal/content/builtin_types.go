package content

// The six surface types this package ships. Registration is the ONLY place the
// set of kinds is enumerated: there is no content.Kind enum and no data table
// mirroring these entries, so a seventh kind is added by calling Register and
// nothing here changes.
//
// Five of them implement TrustGated. Profile does not — see Profile's doc
// comment.
func init() {
	Register(fragmentType{})
	Register(commandType{})
	Register(mcpType{})
	Register(hookType{})
	Register(skillType{})
	Register(profileType{})
}

// Compile-time proof that each type satisfies the registry contract, and that
// exactly the intended five surfaces participate in trust.
var (
	_ SurfaceType = fragmentType{}
	_ SurfaceType = commandType{}
	_ SurfaceType = mcpType{}
	_ SurfaceType = hookType{}
	_ SurfaceType = skillType{}
	_ SurfaceType = profileType{}

	_ TrustGated = Fragment{}
	_ TrustGated = Command{}
	_ TrustGated = MCP{}
	_ TrustGated = Hook{}
	_ TrustGated = Skill{}
)
