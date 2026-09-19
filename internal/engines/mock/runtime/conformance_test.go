package runtime_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/engines/mock/runtime"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// ---------------------------------------------------------------------------
// THE L5 CONFORMANCE LOOP: what the mock's probe walk READS (L2) is compared,
// probe by probe, against what the declaration SAYS the engine reads (L1),
// and any divergence fails.
//
// L2 is written to be L1-driven, but "written to be" is a claim, and the walk
// has its own resolver switch, its own path joins and its own argv lookup —
// each a place where it can quietly read something other than what L1
// declared: a scope L1 grew that L2 has no resolver for (recorded as
// Unresolved, never a build error), a Rel joined onto the wrong root, a
// flag-value probe reading the wrong flag. Every one of those renders as an
// ordinary present:false row, which is exactly the shape of the silent no-op
// this instrument exists to catch — so the instrument itself needs the check.
//
// The measured half of the loop (a real engine's strace read-set, from the
// isolation probe) compares against the same ProbeRecord.Path set this test
// pins; this test is what makes that set trustworthy as L1's meaning.
// ---------------------------------------------------------------------------

// conformanceFixture is one declaration's worth of roots and argv, built so
// that EVERY declared probe has a location on this run: each env-dir var is
// set, each flag-value probe's flag is passed a path, each required flag is
// present. The walk is then expected to read exactly these locations.
type conformanceFixture struct {
	cwd, home string
	envRoots  map[string]string // ScopeEnvDir EnvVar → root
	flagPaths map[string]string // ScopeFlagValue Flag → path passed in argv
	argv      []string
}

func buildConformanceFixture(t *testing.T, cli agent.EngineCLI) conformanceFixture {
	t.Helper()
	f := conformanceFixture{
		cwd:       t.TempDir(),
		home:      t.TempDir(),
		envRoots:  map[string]string{},
		flagPaths: map[string]string{},
	}
	for _, p := range cli.Probes {
		switch p.Scope {
		case agent.ScopeEnvDir:
			if _, seen := f.envRoots[p.EnvVar]; !seen {
				f.envRoots[p.EnvVar] = t.TempDir()
			}
		case agent.ScopeFlagValue:
			if _, seen := f.flagPaths[p.Flag]; !seen {
				f.flagPaths[p.Flag] = filepath.Join(t.TempDir(), "surface-"+string(p.Kind))
			}
		}
	}
	for _, fl := range cli.Flags {
		path, probed := f.flagPaths[fl.Name]
		switch {
		case probed:
			f.argv = append(f.argv, fl.Name, path)
		case fl.Required && fl.TakesValue():
			f.argv = append(f.argv, fl.Name, "required-value")
		case fl.Required:
			f.argv = append(f.argv, fl.Name)
		}
	}
	if cli.Prompt == agent.PromptPositional {
		f.argv = append(f.argv, "the positional prompt")
	}
	return f
}

// expectedReadPath is the test's OWN derivation of where L1 says a probe
// reads — deliberately independent of the walk's, so the two can disagree.
func (f conformanceFixture) expectedReadPath(p agent.CLIProbe) string {
	switch p.Scope {
	case agent.ScopeCwd:
		return filepath.Join(f.cwd, p.Rel)
	case agent.ScopeHome:
		return filepath.Join(f.home, p.Rel)
	case agent.ScopeEnvDir:
		return filepath.Join(f.envRoots[p.EnvVar], p.Rel)
	case agent.ScopeFlagValue:
		return f.flagPaths[p.Flag]
	default:
		return ""
	}
}

// assertWalkConforms runs the walk over the fixture and compares every record
// against its declaring probe.
func assertWalkConforms(t *testing.T, cli agent.EngineCLI, f conformanceFixture) {
	t.Helper()
	parsed, err := cli.ParseArgv(f.argv)
	if err != nil {
		t.Fatalf("fixture argv %v did not parse against %s/%s: %v", f.argv, cli.Engine, cli.Surface, err)
	}
	res := runtime.Resolver{Cwd: f.cwd, Home: f.home, Getenv: func(k string) string { return f.envRoots[k] }}
	recs := runtime.Walk(cli, parsed, res)

	if len(recs) != len(cli.Probes) {
		t.Fatalf("walk produced %d records for %d declared probes: the walk reads a different set than L1 declares", len(recs), len(cli.Probes))
	}
	for i, p := range cli.Probes {
		rec := recs[i]
		if rec.Order != i || rec.Kind != string(p.Kind) || rec.Scope != string(p.Scope) || rec.Rel != p.Rel {
			t.Errorf("probe %d: record (order=%d kind=%s scope=%s rel=%q) does not mirror the declaration (kind=%s scope=%s rel=%q)",
				i, rec.Order, rec.Kind, rec.Scope, rec.Rel, p.Kind, p.Scope, p.Rel)
		}
		if rec.Unresolved {
			t.Errorf("probe %d (%s/%s): every declared probe has a location on this fixture, yet the walk could not resolve it: %s",
				i, p.Kind, p.Scope, rec.Note)
		}
		if rec.Fallback {
			t.Errorf("probe %d (%s/%s): env var %s was set, yet the walk fell back to $HOME", i, p.Kind, p.Scope, p.EnvVar)
		}
		if want := f.expectedReadPath(p); rec.Path != want {
			t.Errorf("probe %d (%s/%s): the walk read %q but L1 declares %q", i, p.Kind, p.Scope, rec.Path, want)
		}
	}
}

// assertEnvObservationConforms: the env observation reads exactly the declared
// SetEnv then StripEnv names, in order, with the declared expectation on each.
func assertEnvObservationConforms(t *testing.T, cli agent.EngineCLI) {
	t.Helper()
	recs := runtime.ObserveEnv(cli, func(string) (string, bool) { return "v", true })
	want := len(cli.SetEnv) + len(cli.StripEnv)
	if len(recs) != want {
		t.Fatalf("env observation has %d records for %d declared variables", len(recs), want)
	}
	for i, name := range cli.SetEnv {
		if recs[i].Name != name || recs[i].Expect != runtime.EnvExpectSet {
			t.Errorf("env %d: observed %s/%s, L1 declares SetEnv %s", i, recs[i].Name, recs[i].Expect, name)
		}
	}
	for j, name := range cli.StripEnv {
		i := len(cli.SetEnv) + j
		if recs[i].Name != name || recs[i].Expect != runtime.EnvExpectStripped {
			t.Errorf("env %d: observed %s/%s, L1 declares StripEnv %s", i, recs[i].Name, recs[i].Expect, name)
		}
	}
}

// assertPromptChannelConforms runs the whole runtime with a prompt offered on
// BOTH channels and checks the report hashed the one L1 declares. A runtime
// that read the other channel — or both — would report a different hash.
func assertPromptChannelConforms(t *testing.T, cli agent.EngineCLI, f conformanceFixture) {
	t.Helper()
	const stdinPrompt = "the stdin prompt\n"
	parsed, err := cli.ParseArgv(f.argv)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	rt := &runtime.Runtime{
		CLI:       cli,
		Argv:      parsed,
		Res:       runtime.Resolver{Cwd: f.cwd, Home: f.home, Getenv: func(k string) string { return f.envRoots[k] }},
		Getenv:    func(string) string { return "" },
		LookupEnv: func(string) (string, bool) { return "", false },
		Stdin:     strings.NewReader(stdinPrompt),
		Stdout:    &stdout,
		Stderr:    &stderr,
	}
	_ = rt.Run() // the exit code is the wire adapter's business, not this test's
	rep, err := runtime.ExtractReport(stderr.String())
	if err != nil {
		t.Fatalf("extract report: %v\nstderr:\n%s", err, stderr.String())
	}
	var want string
	switch cli.Prompt {
	case agent.PromptStdin:
		want = sha256hex([]byte(stdinPrompt))
	case agent.PromptPositional:
		want = sha256hex([]byte(parsed.Positionals[len(parsed.Positionals)-1]))
	default:
		t.Fatalf("L1 declares prompt delivery %q, which this loop has no expectation for — extend it", cli.Prompt)
	}
	if rep.PromptSHA256 != want {
		t.Errorf("prompt read off the wrong channel: report hash %s, L1 declares delivery %q whose bytes hash to %s",
			rep.PromptSHA256, cli.Prompt, want)
	}
}

// TestConformance_EveryImpersonableDeclaration_WalkReadsWhatL1Declares runs
// the loop over every surface of every registered backend the mock can
// impersonate — including any added later, which is where a divergence would
// first appear.
func TestConformance_EveryImpersonableDeclaration_WalkReadsWhatL1Declares(t *testing.T) {
	var checked int
	for _, name := range backends.List() {
		clis, ok := backends.EngineCLIsFor(name)
		if !ok {
			continue
		}
		for _, cli := range clis {
			t.Run(name+"/"+string(cli.Surface), func(t *testing.T) {
				f := buildConformanceFixture(t, cli)
				assertWalkConforms(t, cli, f)
				assertEnvObservationConforms(t, cli)
				assertPromptChannelConforms(t, cli, f)
			})
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no backend declared an engine CLI: this loop compared nothing, which is this project's signature false green")
	}
}

// TestConformance_SyntheticDeclaration_EveryScopeL1AcceptsIsWalked covers the
// scope vocabulary the shipped declarations happen not to use. The synthetic
// declaration is checked against L1's own Validate first, so it is a legal L1
// declaration by L1's rules — and then the walk must read every probe of it.
// A scope Validate accepts but the walk cannot resolve is the exact drift
// this loop exists to catch, and the shipped backends would not surface it
// until one of them used the scope.
func TestConformance_SyntheticDeclaration_EveryScopeL1AcceptsIsWalked(t *testing.T) {
	const flag = "--surface-file"
	cli := agent.EngineCLI{
		Engine:  "synthetic",
		Surface: agent.CLISurfaceOneshot,
		Binary:  "synthetic",
		Prompt:  agent.PromptStdin,
		Flags:   []agent.CLIFlag{{Name: flag, Value: agent.ValuePath}},
		Probes: []agent.CLIProbe{
			{Kind: agent.ProbeKindContext, Scope: agent.ScopeCwd, Rel: "CTX.md"},
			{Kind: agent.ProbeKindSettings, Scope: agent.ScopeHome, Rel: ".synthetic/settings.json"},
			{Kind: agent.ProbeKindMCP, Scope: agent.ScopeEnvDir, EnvVar: "SYNTHETIC_HOME", EnvHomeDefault: ".synthetic", Rel: "mcp.json"},
			{Kind: agent.ProbeKindCommands, Scope: agent.ScopeFlagValue, Flag: flag, Dir: true},
		},
		SetEnv:   []string{"SYNTHETIC_SET"},
		StripEnv: []string{"SYNTHETIC_STRIPPED"},
	}
	if err := cli.Validate(); err != nil {
		t.Fatalf("the synthetic declaration must be legal by L1's own rules: %v", err)
	}
	f := buildConformanceFixture(t, cli)
	assertWalkConforms(t, cli, f)
	assertEnvObservationConforms(t, cli)
}
