package claudeengine

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

func TestDescriptor_Validates(t *testing.T) {
	require.NoError(t, Descriptor().Validate())
}

func TestDescriptor_NameIsTheEnginePackagesOwn(t *testing.T) {
	d := Descriptor()
	assert.Equal(t, claude.EngineName, d.Name)
	assert.Equal(t, agent.DistributionDefault, d.Distribution)
}

// The home declaration is built from the engine's own constants, so the
// directory the seed lands in IS the directory SessionConfigDir names.
func TestDescriptor_HomeIsBuiltFromClaudesOwnConstants(t *testing.T) {
	home, ok := Descriptor().Home.Get()
	require.True(t, ok)
	require.Len(t, home.Vars, 1)
	assert.Equal(t, claude.ConfigDirEnv, home.Vars[0].EnvVar)
	assert.Equal(t, claude.HomeLeaf, home.Vars[0].Subdir)

	seed, ok := home.Credentials.Get()
	require.True(t, ok, "claude relocates credentials with its home var")
	assert.Equal(t, claude.HomeLeaf, seed.Subdir)
	assert.Equal(t, "ANTHROPIC_API_KEY", seed.EnvTrigger)
	assert.Equal(t, "claude login", seed.LoginHint)
	require.Len(t, seed.Files, 1, "only .credentials.json crosses — never .claude.json (the user's whole config)")
	assert.Equal(t, filepath.ToSlash(filepath.Join(claude.ConfigDirName, claude.CredentialsFileName)), seed.Files[0].HostRelHome)
	assert.Equal(t, claude.CredentialsFileName, seed.Files[0].DestName)
	assert.True(t, seed.Files[0].Required)
}

// The seed's destination leaf and the directory CLAUDE_CONFIG_DIR is pointed
// at are decided by different code (the descriptor and SessionConfigDir);
// they must be the same directory, or the engine reads a home nothing
// prepared. Both sides are read here, neither is re-typed.
func TestDescriptor_SeedLandsWhereSessionConfigDirPoints(t *testing.T) {
	home, ok := Descriptor().Home.Get()
	require.True(t, ok)
	seed, ok := home.Credentials.Get()
	require.True(t, ok)

	dir, err := claude.SessionConfigDir(filepath.Join(string(filepath.Separator), "proj"), "ugly-icy-squid")
	require.NoError(t, err)
	assert.Equal(t, seed.Subdir, filepath.Base(dir), "the seed must land in the leaf CLAUDE_CONFIG_DIR names")
}

func TestDescriptor_ContainerAuthPrefersEnvAndMountsTheRealCredentialReadWrite(t *testing.T) {
	c, ok := Descriptor().Container.Get()
	require.True(t, ok)
	assert.NotEmpty(t, c.Install, "claude has an official npm installer")
	assert.Equal(t, "claude --version", c.ValidateCommand)
	assert.Equal(t, []string{claude.ConfigDirName}, c.OverlayDirs)
	assert.Equal(t, filepath.Join(claude.ConfigDirName, claude.TranscriptsDirName), c.TranscriptStoreRel)

	auth, ok := c.Auth.Get()
	require.True(t, ok)
	assert.Equal(t, []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}, auth.EnvTriggers)
	assert.Contains(t, auth.EnvPassthrough, "ANTHROPIC_BASE_URL")
	require.Len(t, auth.CredentialFiles, 1)
	assert.False(t, auth.CredentialFiles[0].ReadOnly, "claude's token refresh must write back into the one real file")
	assert.Equal(t, auth.CredentialFiles[0].HostRelHome, auth.CredentialFiles[0].ContainerRelHome)
	assert.NotEmpty(t, auth.Hint)
}

func TestDescriptor_EveryCapabilityClaudeCarriesIsProvided(t *testing.T) {
	d := Descriptor()
	for name, decided := range map[string]bool{
		"SettingsWriter":    d.SettingsWriter.Decided() && d.SettingsWriter.AbsentReason() == "",
		"InstanceConfig":    d.InstanceConfig.Decided() && d.InstanceConfig.AbsentReason() == "",
		"Provisioning":      d.Provisioning.Decided() && d.Provisioning.AbsentReason() == "",
		"CommandExports":    d.CommandExports.Decided() && d.CommandExports.AbsentReason() == "",
		"SkillExports":      d.SkillExports.Decided() && d.SkillExports.AbsentReason() == "",
		"HookGlobalScope":   d.HookGlobalScope.Decided() && d.HookGlobalScope.AbsentReason() == "",
		"VersionCommand":    d.VersionCommand.Decided() && d.VersionCommand.AbsentReason() == "",
		"TranscriptReaders": d.TranscriptReaders.Decided() && d.TranscriptReaders.AbsentReason() == "",
	} {
		assert.True(t, decided, "%s must be provided for claude", name)
	}
	assert.True(t, d.EnforcesReadOnlyPlan, "--permission-mode plan is read-only")
	assert.Equal(t, claude.EngineName, d.NewBackend(nil).Name())
	assert.Equal(t, claude.EngineName, d.NewConfig().BackendType())
}

// parseVersion reads the MEASURED shape: "2.1.225 (Claude Code)".
func TestParseVersion_VersionLeadsNameFollows(t *testing.T) {
	v, err := parseVersion("2.1.225 (Claude Code)\n")
	require.NoError(t, err)
	assert.Equal(t, "2.1.225", v)
}

func TestCommandExports_ProjectsTheClaudeCodeBlock(t *testing.T) {
	off := false
	ex := CommandExports([]*bundles.LoadedContent{{
		Name: "p", Content: "body",
		LLM: bundles.LLMExports{ClaudeCode: bundles.ClaudeCodeConfig{Enabled: &off, Description: "d", ArgumentHint: "h", AllowedTools: []string{"Read"}, Model: "m"}},
	}})
	require.Len(t, ex, 1)
	assert.Equal(t, agent.CommandExport{Name: "p", Content: "body", Enabled: false, Description: "d", ArgumentHint: "h", AllowedTools: []string{"Read"}, Model: "m"}, ex[0])
}

func TestSkillExports_ReadsTheClaudeCodeEnablement(t *testing.T) {
	off := false
	ex := SkillExports([]*bundles.LoadedSkill{
		{Frontmatter: bundles.SkillFrontmatter{Name: "on"}},
		{Frontmatter: bundles.SkillFrontmatter{Name: "off"}, LLM: bundles.SkillLLMExports{ClaudeCode: bundles.SkillEngineExport{Enabled: &off}}},
	})
	require.Len(t, ex, 2)
	assert.True(t, ex[0].Enabled)
	assert.False(t, ex[1].Enabled)
}

// Claude's refresh token is single-use and rotating, so the ORDER is the
// declaration: a mount has one inode and one refresh path, replication has a
// window in which an instance can present a token another already spent.
// Asserting the order, not the set, is what keeps a later edit from quietly
// preferring the mechanism with the failure mode.
func TestDescriptor_AcceptsMountedBeforeReplicated(t *testing.T) {
	p, ok := Descriptor().Provisioning.Get()
	require.True(t, ok, "claude has credential material, so it declares a policy rather than absence")
	require.NoError(t, p.Validate())
	assert.Equal(t, []agent.MaterialDelivery{agent.MaterialDeliveryMounted, agent.MaterialDeliveryReplicated}, p.Accept)
}

// A stripped copy is refused at every position. It is not a weaker sharing
// mode: the copy has the single-use refresh token removed, so it works until
// the access token expires and then that instance is stuck with no way back.
// Accepting it as a last resort would turn a loud launch-time refusal into a
// run that dies hours later, far from its cause.
func TestDescriptor_AcceptsNoDeliveryThatCannotRenew(t *testing.T) {
	p, ok := Descriptor().Provisioning.Get()
	require.True(t, ok)
	for _, d := range p.Accept {
		assert.NotEqual(t, agent.MaterialDeliveryAbsent, d)
		assert.True(t, d.Decided(), "every accepted delivery must be a decided one")
	}
}
