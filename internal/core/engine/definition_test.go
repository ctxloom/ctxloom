package engine_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

var grammar = engine.CLIGrammar{
	Mode:       engine.Structured,
	Binary:     "vendor",
	Flags:      []engine.Flag{{Name: "-p"}, {Name: "--model", HasValue: true}, {Name: "--settings", HasValue: true}},
	Positional: 1,
}

// parseArgvBounded is grammar.ParseArgv with a bound: the parse advances by
// the index each flag read hands back, and a read that failed to advance it
// would otherwise spin the test binary instead of failing the test.
func parseArgvBounded(t *testing.T, argv []string) (engine.Parsed, error) {
	t.Helper()
	type result struct {
		p   engine.Parsed
		err error
	}
	r := testsupport.Within(t, 5*time.Second, func() result {
		p, err := grammar.ParseArgv(argv)
		return result{p, err}
	}, "ParseArgv(%q) did not return", argv)
	return r.p, r.err
}

func TestCLIGrammar_ParseArgv_AcceptsDeclaredFlagsAndPositionals(t *testing.T) {
	p, err := parseArgvBounded(t, []string{"-p", "--model", "m1", "--settings=s.json", "--", "-p"})
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
			_, err := parseArgvBounded(t, argv)
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
func (stub) Exports(engine.Items) (engine.Exports, error)     { return engine.Exports{}, nil }
func (stub) Home() engine.HomeSpec                            { return engine.HomeSpec{} }
func (s stub) Container() (engine.ContainerSpec, error) {
	return engine.ContainerSpec{}, engine.ErrUnsupported{Engine: s.Name, Capability: "container"}
}
func (stub) Transcripts() []engine.TranscriptReader { return nil }
func (stub) Hooks() engine.HookCodec                { return nil }
func (stub) Wake() engine.Declared[engine.WakeSpec] {
	return engine.Absent[engine.WakeSpec]("a test double wakes nothing")
}
func (stub) Approvals() engine.Declared[engine.ApprovalCodec] {
	return engine.Absent[engine.ApprovalCodec]("a test double approves nothing")
}

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

// Distribution's zero value is UNSET, and Validate refuses it: an engine
// that declared nothing must not default-ship. Every decided member is
// accepted; a value outside the enum is as undecided as the zero.
func TestBase_Validate_RefusesAnUndecidedDistributionAndAnUppercaseName(t *testing.T) {
	b := engine.Base{Definition: engine.Definition{Name: "x", Modes: []engine.Mode{engine.Interactive}, CLI: []engine.CLIGrammar{{Mode: engine.Interactive, Binary: "x"}}}}
	require.ErrorIs(t, b.Validate(), engine.ErrDefinition)
	require.ErrorContains(t, b.Validate(), "Distribution")
	for _, dist := range []engine.Distribution{engine.DistributionDefault, engine.DistributionOptIn, engine.DistributionTestOnly} {
		b.Distribution = dist
		require.NoError(t, b.Validate(), "%v", dist)
	}
	b.Distribution = engine.Distribution(99)
	require.ErrorContains(t, b.Validate(), "Distribution")
	b.Distribution = engine.DistributionDefault
	b.Name = "X"
	require.ErrorContains(t, b.Validate(), "lowercase")
}
