package mockengine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// flagProbeCLI declares one flag-value probe, the shape claude uses for
// --settings.
func flagProbeCLI() agent.EngineCLI {
	return agent.EngineCLI{
		Engine:  "probe-test",
		Surface: agent.CLISurfaceOneshot,
		Flags: []agent.CLIFlag{
			{Name: "--settings", Value: agent.ValueString},
		},
		Probes: []agent.CLIProbe{{
			Kind:  agent.ProbeKindSettings,
			Scope: agent.ScopeFlagValue,
			Flag:  "--settings",
		}},
	}
}

func digestOf(t *testing.T, cli agent.EngineCLI, argv []string, res Resolver) string {
	t.Helper()
	parsed, err := cli.ParseArgv(argv)
	if err != nil {
		t.Fatalf("parse %v: %v", argv, err)
	}
	return BuildReport(cli, Walk(cli, parsed, res), nil, nil).DiscoveryDigest
}

// U079-F08: canonicalRendering carries only the STABLE fields, and every
// discriminator between these two runs lived in Note, which is excluded. So a
// probe that could not be RESOLVED at all — ctxloom never passed --settings —
// hashed identically to a probe that resolved and found NOTHING — ctxloom
// passed --settings and wrote no file there. Those are different delivery
// failures, and DiscoveryDigest is the single value a conformance test is meant
// to assert on.
func TestDigest_UnresolvedProbeDiffersFromResolvedButAbsent(t *testing.T) {
	cli := flagProbeCLI()
	res := Resolver{Cwd: t.TempDir(), Home: t.TempDir()}
	missing := filepath.Join(t.TempDir(), "never-written.json")

	neverAsked := digestOf(t, cli, nil, res)
	askedAndEmpty := digestOf(t, cli, []string{"--settings", missing}, res)

	if neverAsked == askedAndEmpty {
		t.Error("a probe whose flag was never passed hashes identically to one whose flag pointed at a file that was never written")
	}
}

// The digest must still be machine-INDEPENDENT: two runs that delivered the
// same bytes to the same declared surface agree regardless of where on disk the
// path landed. Whatever discriminates the case above must not be the path.
func TestDigest_StaysIndependentOfTheResolvedPath(t *testing.T) {
	cli := flagProbeCLI()
	res := Resolver{Cwd: t.TempDir(), Home: t.TempDir()}

	one := filepath.Join(t.TempDir(), "a.json")
	two := filepath.Join(t.TempDir(), "b.json")
	writeSettings(t, one)
	writeSettings(t, two)

	if digestOf(t, cli, []string{"--settings", one}, res) != digestOf(t, cli, []string{"--settings", two}, res) {
		t.Error("the same bytes at two different absolute paths produced different digests")
	}
}

// And two runs that BOTH resolved a path and both found nothing still agree —
// the new discriminator marks unresolvability, not absence.
func TestDigest_TwoResolvedAbsentProbesStillAgree(t *testing.T) {
	cli := flagProbeCLI()
	res := Resolver{Cwd: t.TempDir(), Home: t.TempDir()}

	a := filepath.Join(t.TempDir(), "gone.json")
	b := filepath.Join(t.TempDir(), "also-gone.json")

	if digestOf(t, cli, []string{"--settings", a}, res) != digestOf(t, cli, []string{"--settings", b}, res) {
		t.Error("two runs that both resolved a path and both found it absent disagreed")
	}
}

func writeSettings(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"k":"v"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}
