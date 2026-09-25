//go:build arch

package runtime_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock/runtime"
)

// ---------------------------------------------------------------------------
// THE CLASS GATE: every limb of evidence this mock carries must be able to say
// NO. For each thing ctxloom either delivers or does not, the run where it was
// delivered and the run where it was not must produce DIFFERENT assertable
// evidence. A limb that renders identically either way is not evidence — it is
// a field that always agrees with you, and a test written against it can never
// fail.
//
// This is the instrument this project uses to detect its characteristic bug
// (exit 0, success message, zero bytes delivered). Three of its limbs once had
// exactly the blindness they exist to catch:
//
//   - PromptSHA256 hashed a nil prompt to e3b0c442… and was therefore NEVER
//     empty, so "no prompt was delivered" and "a prompt was delivered" were the
//     same report. runtime_test.go's `if rep.PromptSHA256 == ""` assertion
//     could not fail.
//   - A ScopeEnvDir probe whose env var was unset fell back to the REAL $HOME
//     and reported present:true off the developer's own config dir — a run in
//     which ctxloom delivered nothing rendered as a run in which it delivered
//     everything, and nothing in the digest said which.
//   - The declared SetEnv/StripEnv contract was read by nothing at all: a run
//     where ctxloom stopped setting CTXLOOM_CONTEXT_FILE reported identically
//     to one where it did.
//
// WHY THIS IS NOT A PER-FIELD TEST. The rule is stated over the ASSERTABLE
// CORE — the discovery digest plus the prompt-presence pair — which is what a
// caller is told to assert on. A new evidence limb that is recorded in a
// diagnostic-only field (a Note, a Root, an absolute Path, all deliberately
// excluded from the digest) fails this gate, because the limb it adds cannot
// change any answer a test is entitled to make.
//
// WHAT IT CANNOT REACH: the limb table is DECLARED, not discovered. A new
// surface kind is not covered until a row is added. The rows are cheap; the
// point is that each one then holds forever. It also proves only that the two
// runs DIFFER, not that they differ correctly — the per-limb assertions below
// each row carry that half.
// ---------------------------------------------------------------------------

// limbRun is one launch of the mock: what the harness delivered, and what the
// engine was told.
type limbRun struct {
	// cli overrides the personality (default claude oneshot).
	cli *agent.EngineCLI
	// prompt is what arrives on stdin.
	prompt string
	// files are cwd-relative files to deliver before the run.
	files map[string]string
	// homeFiles are $HOME-relative files (the ScopeEnvDir fallback root).
	homeFiles map[string]string
	// env is the child's environment.
	env map[string]string
}

// fingerprint is the report's ASSERTABLE CORE: exactly the values a test is
// told it may assert on. Diagnostic fields (Root, Path, Head, Note) are
// excluded here for the same reason they are excluded from the digest, which
// is what makes this gate a statement about evidence rather than about text.
func fingerprint(rep runtime.Report) string {
	return rep.DiscoveryDigest + "|prompt=" + strconv.FormatBool(rep.PromptPresent) + "|" + rep.PromptSHA256
}

// runLimb drives one launch over a fresh workspace and returns its report.
func runLimb(t *testing.T, r limbRun) runtime.Report {
	t.Helper()
	cli := claudeOneshot(t)
	if r.cli != nil {
		cli = *r.cli
	}
	cwd, home := t.TempDir(), t.TempDir()
	writeFiles(t, cwd, r.files)
	writeFiles(t, home, r.homeFiles)
	argv, stdin := limbArgv(cli, r.prompt)
	parsed, err := cli.ParseArgv(argv)
	if err != nil {
		t.Fatalf("parse argv %v: %v", argv, err)
	}
	getenv := func(k string) string { return r.env[k] }
	var stdout, stderr bytes.Buffer
	rt := &runtime.Runtime{
		CLI:    cli,
		Argv:   parsed,
		Res:    runtime.Resolver{Cwd: cwd, Home: home, Getenv: getenv},
		Getenv: getenv,
		Stdin:  strings.NewReader(stdin),
		Stdout: &stdout,
		Stderr: &stderr,
	}
	rt.Run()
	rep, err := runtime.ExtractReport(stderr.String())
	if err != nil {
		t.Fatalf("extract report: %v\nstderr:\n%s", err, stderr.String())
	}
	return rep
}

// writeFiles writes each file under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		writeFile(t, dir, rel, []byte(body))
	}
}

// limbArgv is the launch line and stdin for one limb. It carries every
// REQUIRED valueless flag the surface declares, so the line the harness
// builds is one the real binary would accept — read off the declaration
// rather than hardcoded, so a personality that requires a different flag is
// driven correctly without editing this harness. The prompt goes on the
// channel L1 DECLARES — claude oneshot takes stdin, another personality may
// take a trailing positional; a harness that always used stdin would be
// testing the wrong limb for half of them.
func limbArgv(cli agent.EngineCLI, prompt string) (argv []string, stdin string) {
	argv = []string{}
	if cli.Subcommand != "" {
		argv = append(argv, cli.Subcommand)
	}
	for _, f := range cli.Flags {
		if f.Required && f.Value == agent.ValueNone {
			argv = append(argv, f.Name)
		}
	}
	if prompt == "" {
		return argv, ""
	}
	if cli.Prompt == agent.PromptPositional {
		return append(argv, prompt), ""
	}
	return argv, prompt
}

func TestArch_EvidenceReport_EveryLimbCanSayNo(t *testing.T) {
	// A synthetic declaration is the only way to exercise StripEnv: no shipped
	// backend declares one today, and a limb with no production declaration is
	// exactly the limb that rots.
	stripCLI := claudeOneshot(t)
	stripCLI.StripEnv = []string{"ANTHROPIC_API_KEY"}

	// Synthetic for the same reason: no shipped backend declares a ScopeEnvDir
	// probe today, and the $HOME-fallback marker below is precisely the limb
	// that rots once nothing exercises it.
	envDirCLI := claudeOneshot(t)
	envDirCLI.Probes = append(append([]agent.CLIProbe(nil), envDirCLI.Probes...), agent.CLIProbe{
		Kind: agent.ProbeKindSettings, Scope: agent.ScopeEnvDir,
		EnvVar: syntheticHomeEnv, EnvHomeDefault: ".mock-engine", Rel: "config.toml",
	})

	const promptBody = "composed context\n\ndo the task"
	syntheticHome := "/nonexistent-engine-home"

	for _, tc := range []struct {
		name string
		// no and yes differ in EXACTLY the one limb under test.
		no, yes limbRun
		// check asserts the absent run is legible as absent.
		check func(t *testing.T, no runtime.Report)
	}{
		{
			name: "prompt bytes",
			no:   limbRun{prompt: ""},
			yes:  limbRun{prompt: promptBody},
			check: func(t *testing.T, no runtime.Report) {
				if no.PromptPresent {
					t.Error("promptPresent is true for a run that received zero bytes")
				}
				if no.PromptSHA256 != "" {
					t.Errorf("promptSha256 = %q for a zero-byte prompt — the empty-string hash is not evidence of delivery", no.PromptSHA256)
				}
			},
		},
		{
			name: "cwd context surface",
			no:   limbRun{},
			yes:  limbRun{files: map[string]string{claude.ContextFileName: "# CLAUDE.md\n"}},
			check: func(t *testing.T, no runtime.Report) {
				rec, ok := recordByKind(no, string(agent.ProbeKindContext))
				if !ok || rec.Present {
					t.Errorf("undelivered context surface did not record present:false: %+v", rec)
				}
			},
		},
		{
			name: "env-dir root",
			// The env var unset: the walk falls back to $HOME/<default>, which
			// on a developer's machine is a REAL config directory. The two runs
			// must not be able to render alike.
			no:  limbRun{cli: &envDirCLI},
			yes: limbRun{cli: &envDirCLI, env: map[string]string{syntheticHomeEnv: syntheticHome}},
			check: func(t *testing.T, no runtime.Report) {
				var fell bool
				for _, rec := range no.Records {
					if rec.Scope == string(agent.ScopeEnvDir) && rec.Fallback {
						fell = true
					}
				}
				if !fell {
					t.Error("no env-dir record marks the $HOME fallback, so a surface ctxloom never delivered can read as one it did")
				}
			},
		},
		{
			name: "declared SetEnv variable",
			no:   limbRun{},
			yes:  limbRun{env: map[string]string{agent.SCMContextFileEnv: "/tmp/ctx.md"}},
			check: func(t *testing.T, no runtime.Report) {
				rec, ok := envRecord(no, agent.SCMContextFileEnv)
				if !ok {
					t.Fatalf("no env record for the declared SetEnv variable %s", agent.SCMContextFileEnv)
				}
				if rec.Present || rec.Honored() {
					t.Errorf("an unset declared variable reported as honoured: %+v", rec)
				}
			},
		},
		{
			name: "declared StripEnv variable",
			no:   limbRun{cli: &stripCLI},
			yes:  limbRun{cli: &stripCLI, env: map[string]string{"ANTHROPIC_API_KEY": "sk-leaked"}},
			check: func(t *testing.T, no runtime.Report) {
				rec, ok := envRecord(no, "ANTHROPIC_API_KEY")
				if !ok {
					t.Fatal("no env record for the declared StripEnv variable")
				}
				if !rec.Honored() {
					t.Errorf("a correctly stripped variable reported as a violation: %+v", rec)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			no, yes := runLimb(t, tc.no), runLimb(t, tc.yes)
			if fingerprint(no) == fingerprint(yes) {
				t.Errorf("delivering %s changed nothing a test may assert on:\n absent:  %s\n present: %s",
					tc.name, fingerprint(no), fingerprint(yes))
			}
			tc.check(t, no)
		})
	}
}

// TestArch_EvidenceReport_EnvViolationsNameEveryBreach proves the env section answers the
// question it exists for in one call, so a caller need not re-derive the
// honoured/violated rule that the record already knows.
func TestArch_EvidenceReport_EnvViolationsNameEveryBreach(t *testing.T) {
	cli := claudeOneshot(t)
	cli.StripEnv = []string{"ANTHROPIC_API_KEY"}
	rep := runLimb(t, limbRun{
		cli: &cli,
		env: map[string]string{
			agent.SessionHarpEnv:    "witty-tag",
			"ANTHROPIC_API_KEY":     "sk-leaked",
			agent.SCMContextFileEnv: "", // set, but EMPTY: ctxloom's own silent no-op shape
		},
	})
	got := map[string]bool{}
	for _, v := range rep.EnvViolations() {
		got[v.Name] = true
	}
	if !got["ANTHROPIC_API_KEY"] {
		t.Error("a variable declared StripEnv but present on the child is not reported as a violation")
	}
	if !got[agent.SCMContextFileEnv] {
		t.Errorf("a declared variable set to the EMPTY string is not reported as a violation — that is delivery of nothing, dressed as delivery: %+v", rep.Env)
	}
	if got[agent.SessionHarpEnv] {
		t.Error("a correctly set variable was reported as a violation")
	}
}

// syntheticHomeEnv is the env var the synthetic ScopeEnvDir probe above reads.
// It names no shipped engine on purpose: the limb under test is the FALLBACK
// marker, not any one engine's home.
const syntheticHomeEnv = "MOCK_ENGINE_HOME"

// envRecord finds one env observation by name.
func envRecord(rep runtime.Report, name string) (runtime.EnvRecord, bool) {
	for _, e := range rep.Env {
		if e.Name == name {
			return e, true
		}
	}
	return runtime.EnvRecord{}, false
}
