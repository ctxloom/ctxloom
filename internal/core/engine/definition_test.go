package engine_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

var grammar = engine.CLIGrammar{
	Mode:       engine.Structured,
	Binary:     "vendor",
	Flags:      []engine.Flag{{Name: "-p"}, {Name: "--model", HasValue: true}, {Name: "--settings", HasValue: true}},
	Positional: 1,
}

func TestCLIGrammar_ParseArgv_AcceptsDeclaredFlagsAndPositionals(t *testing.T) {
	p, err := grammar.ParseArgv([]string{"-p", "--model", "m1", "--settings=s.json", "--", "-p"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"-p": "", "--model": "m1", "--settings": "s.json"}, p.Flags)
	require.Equal(t, []string{"-p"}, p.Positionals, "everything after -- is positional, flags included")
}

func TestCLIGrammar_ParseArgv_RefusesDrift(t *testing.T) {
	for name, argv := range map[string][]string{
		"undeclared flag":        {"--resume", "k"},
		"value-taking flag bare": {"--model"},
		"value on a bare flag":   {"-p=yes"},
		"too many positionals":   {"a", "b"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := grammar.ParseArgv(argv)
			require.True(t, errors.Is(err, engine.ErrArgv), "%v", err)
		})
	}
}

func TestCLIFor_SelectsTheModesGrammar(t *testing.T) {
	g, ok := engine.CLIFor([]engine.CLIGrammar{grammar}, engine.Structured)
	require.True(t, ok)
	require.Equal(t, "vendor", g.Binary)
	_, ok = engine.CLIFor([]engine.CLIGrammar{grammar}, engine.Interactive)
	require.False(t, ok)
}

// stub is the smallest Engine: a Base and nothing else.
type stub struct{ engine.Base }

func (stub) Instance(engine.Session) (engine.Instance, error) { return nil, nil }

func stubEngine(name engine.Name, dist engine.Distribution) engine.Engine {
	return stub{engine.Base{Definition: engine.Definition{Name: name, Distribution: dist}}}
}

func TestNewRegistry_RefusesADuplicateName(t *testing.T) {
	_, err := engine.NewRegistry(stubEngine("a", engine.DistributionDefault), stubEngine("a", engine.DistributionTestOnly))
	require.ErrorContains(t, err, `"a" registered twice`)
}

func TestRegistry_Names_AreSortedAndFiltered(t *testing.T) {
	reg, err := engine.NewRegistry(stubEngine("b", engine.DistributionTestOnly), stubEngine("a", engine.DistributionDefault))
	require.NoError(t, err)
	require.Equal(t, []engine.Name{"a", "b"}, reg.Names(nil))
	require.Equal(t, []engine.Name{"a"}, reg.Names(func(d engine.Definition) bool { return d.Distribution == engine.DistributionDefault }))
	_, ok := reg.Lookup("A")
	require.False(t, ok, "exact match only")
}

func TestRegistry_Default_IsTheOneDefaultDistributionEngine(t *testing.T) {
	reg, _ := engine.NewRegistry(stubEngine("t", engine.DistributionTestOnly), stubEngine("d", engine.DistributionDefault))
	e, err := reg.Default()
	require.NoError(t, err)
	require.Equal(t, engine.Name("d"), e.Root().Name)

	none, _ := engine.NewRegistry(stubEngine("t", engine.DistributionTestOnly))
	_, err = none.Default()
	require.ErrorContains(t, err, "0 engines ship by default")
	two, _ := engine.NewRegistry(stubEngine("a", engine.DistributionDefault), stubEngine("b", engine.DistributionDefault))
	_, err = two.Default()
	require.ErrorContains(t, err, "2 engines ship by default")
}

func TestBase_Validate_RefusesAnUndecidedDistributionAndAnUppercaseName(t *testing.T) {
	b := engine.Base{Definition: engine.Definition{Name: "x", Modes: []engine.Mode{engine.Interactive}, CLI: []engine.CLIGrammar{{Mode: engine.Interactive, Binary: "x"}}}}
	require.ErrorIs(t, b.Validate(), engine.ErrDefinition)
	b.Distribution = engine.DistributionDefault
	require.NoError(t, b.Validate())
	b.Name = "X"
	require.ErrorContains(t, b.Validate(), "lowercase")
}
