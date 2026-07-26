package agent

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProbeKindOf_MatchesSurfaceKindLabels pins the two vocabularies together:
// every delivery SurfaceKind maps onto a DECLARED ProbeKind constant. Without
// this, renaming a SurfaceKind label would silently orphan every probe
// declaration that used the old spelling — a declaration nothing reads is the
// same lie as a delivery that writes nothing.
func TestProbeKindOf_MatchesSurfaceKindLabels(t *testing.T) {
	declared := map[ProbeKind]bool{
		ProbeKindContext:  true,
		ProbeKindMCP:      true,
		ProbeKindSettings: true,
		ProbeKindCommands: true,
		ProbeKindSkills:   true,
		ProbeKindAgents:   true,
	}
	for _, k := range []SurfaceKind{SurfaceContext, SurfaceMCP, SurfaceSettings, SurfaceCommands, SurfaceSkills} {
		assert.True(t, declared[ProbeKindOf(k)],
			"SurfaceKind %s maps to probe kind %q, which is not a declared ProbeKind constant", k, ProbeKindOf(k))
	}
}

// grammar is a small stand-in declaration exercising every ValueShape and the
// subcommand form, so the parser's contract is pinned without depending on any
// real backend.
func grammar() EngineCLI {
	return EngineCLI{
		Engine:     "stub",
		Surface:    CLISurfaceOneshot,
		Binary:     "stub",
		Subcommand: "exec",
		Prompt:     PromptPositional,
		Flags: []CLIFlag{
			{Name: "--flag", Value: ValueNone},
			{Name: "--str", Value: ValueString},
			{Name: "--path", Value: ValuePath},
		},
		Probes: []CLIProbe{
			{Kind: ProbeKindSettings, Scope: ScopeFlagValue, Flag: "--path"},
			{Kind: ProbeKindContext, Scope: ScopeCwd, Rel: "STUB.md"},
		},
	}
}

// TestParseArgv_ReadsDeclaredGrammar covers the happy path: subcommand,
// valueless flag, value-taking flags, and the trailing positional prompt.
func TestParseArgv_ReadsDeclaredGrammar(t *testing.T) {
	got, err := grammar().ParseArgv([]string{"exec", "--flag", "--str", "x", "--path", "/tmp/p", "do the thing"})
	require.NoError(t, err)
	assert.Equal(t, "exec", got.Subcommand)
	assert.True(t, got.Has("--flag"))
	v, ok := got.Value("--str")
	assert.True(t, ok)
	assert.Equal(t, "x", v)
	assert.Equal(t, []string{"do the thing"}, got.Positionals)
}

// TestParseArgv_EmptyStringValueIsPresent pins the distinction claude's
// `--tools ""` turns on: a flag with an EMPTY value token is present, and must
// not read as absent.
func TestParseArgv_EmptyStringValueIsPresent(t *testing.T) {
	got, err := grammar().ParseArgv([]string{"exec", "--str", ""})
	require.NoError(t, err)
	v, ok := got.Value("--str")
	assert.True(t, ok, "an empty value token is still an occurrence")
	assert.Equal(t, "", v)
	assert.Empty(t, got.Positionals, "the empty value is the flag's value, not a positional")
}

// TestParseArgv_UndeclaredFlagIsAnError is the anti-drift primitive every
// backend's own test rests on.
func TestParseArgv_UndeclaredFlagIsAnError(t *testing.T) {
	_, err := grammar().ParseArgv([]string{"exec", "--brand-new"})
	var undeclared *UndeclaredFlagError
	require.ErrorAs(t, err, &undeclared)
	assert.Equal(t, "--brand-new", undeclared.Flag)
	assert.Contains(t, err.Error(), "--brand-new")
}

// TestParseArgv_MissingValueAndSubcommand covers the two malformed-line cases.
func TestParseArgv_MissingValueAndSubcommand(t *testing.T) {
	_, err := grammar().ParseArgv([]string{"exec", "--path"})
	var missing *MissingValueError
	assert.True(t, errors.As(err, &missing), "a value-taking flag at end of argv is a missing-value error")

	_, err = grammar().ParseArgv([]string{"--flag"})
	var sub *SubcommandError
	assert.True(t, errors.As(err, &sub), "a declared subcommand must lead the line")
}

// TestValidate_RejectsSelfInconsistentDeclarations proves the declaration's own
// guard rails bite.
func TestValidate_RejectsSelfInconsistentDeclarations(t *testing.T) {
	require.NoError(t, grammar().Validate())

	dup := grammar()
	dup.Flags = append(dup.Flags, CLIFlag{Name: "--flag"})
	assert.Error(t, dup.Validate(), "a duplicate flag is invalid")

	orphan := grammar()
	orphan.Probes = []CLIProbe{{Kind: ProbeKindMCP, Scope: ScopeFlagValue, Flag: "--nope"}}
	assert.Error(t, orphan.Validate(), "a probe reading an undeclared flag is invalid")

	valueless := grammar()
	valueless.Probes = []CLIProbe{{Kind: ProbeKindMCP, Scope: ScopeFlagValue, Flag: "--flag"}}
	assert.Error(t, valueless.Validate(), "a probe cannot read the value of a valueless flag")
}

// TestEngineCLIFor_SelectsBySurface pins surface selection.
func TestEngineCLIFor_SelectsBySurface(t *testing.T) {
	list := []EngineCLI{{Surface: CLISurfaceOneshot}, {Surface: CLISurfaceInteractive}}
	got, ok := EngineCLIFor(list, CLISurfaceInteractive)
	require.True(t, ok)
	assert.Equal(t, CLISurfaceInteractive, got.Surface)
	_, ok = EngineCLIFor(list, CLISurface("acp"))
	assert.False(t, ok, "an undeclared surface is absent, not fabricated")
}

// TestParseArgv_EnforcesTheWHOLEDeclaredGrammar is the anti-fiction test for
// the three constraints the declaration carries beyond flag NAMES. A grammar
// that checks only "is this name declared" accepts argv lines the real vendor
// binary rejects with exit 2 — and the stand-in that runs on that grammar then
// reports a fully green run for a launch that could never have started
// (U079-F01/F02). The constraints are enforced HERE, in the shared reader, so
// the driver's own anti-drift test and the fake get the same answer.
func TestParseArgv_EnforcesTheWHOLEDeclaredGrammar(t *testing.T) {
	strict := grammar()
	strict.Flags = []CLIFlag{
		{Name: "--print", Value: ValueNone, Required: true},
		{Name: "--sandbox", Value: ValueString, Enum: []string{"read-only", "workspace-write"}},
		{Name: "--bypass", Value: ValueNone, ConflictsWith: []string{"--sandbox"}},
	}
	strict.Probes = nil
	require.NoError(t, strict.Validate())

	ok, err := strict.ParseArgv([]string{"exec", "--print", "--sandbox", "read-only"})
	require.NoError(t, err, "the declared-legal line must still parse")
	assert.True(t, ok.Has("--print"))

	var missing *MissingFlagError
	_, err = strict.ParseArgv([]string{"exec", "--sandbox", "read-only"})
	require.Error(t, err, "a required flag left out must not parse")
	assert.True(t, errors.As(err, &missing), "got %T: %v", err, err)

	var badVal *ValueNotAllowedError
	_, err = strict.ParseArgv([]string{"exec", "--print", "--sandbox", "nonsense"})
	require.Error(t, err, "a value outside the declared enum must not parse")
	assert.True(t, errors.As(err, &badVal), "got %T: %v", err, err)

	var conflict *ConflictingFlagsError
	_, err = strict.ParseArgv([]string{"exec", "--print", "--sandbox", "read-only", "--bypass"})
	require.Error(t, err, "two flags declared mutually exclusive must not parse together")
	assert.True(t, errors.As(err, &conflict), "got %T: %v", err, err)

	// The conflict is symmetric: declaring it on one side must reject either
	// argv order, or the constraint is a coin toss.
	_, err = strict.ParseArgv([]string{"exec", "--print", "--bypass", "--sandbox", "read-only"})
	assert.Error(t, err, "the exclusion must hold in either argv order")
}

// TestValidate_RejectsUnenforceableConstraints proves a constraint that names
// something undeclared fails at the declaration rather than silently never
// firing — a typo'd ConflictsWith is a rule that is always satisfied.
func TestValidate_RejectsUnenforceableConstraints(t *testing.T) {
	typo := grammar()
	typo.Flags = append(typo.Flags, CLIFlag{Name: "--x", ConflictsWith: []string{"--nope"}})
	assert.Error(t, typo.Validate(), "a conflict naming an undeclared flag can never fire")

	enumOnBool := grammar()
	enumOnBool.Flags = append(enumOnBool.Flags, CLIFlag{Name: "--y", Value: ValueNone, Enum: []string{"a"}})
	assert.Error(t, enumOnBool.Validate(), "a valueless flag cannot constrain a value it never takes")
}
