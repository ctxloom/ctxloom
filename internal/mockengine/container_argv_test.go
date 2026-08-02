package mockengine_test

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// The container tests spawn the mock with a HAND-WRITTEN vendor argv: there is
// no seam by which they could obtain the real driver's, because buildArgs is
// unexported in every backend and nothing exported returns a spawn argv. These
// two functions are that argv, lifted out of the docker-gated file so the
// assertions below bind the very bytes the container run uses.
//
// containerPrompt is the task both container runs deliver; codex carries it in
// argv, claude on stdin, which is the whole point of the delivery pin.
const containerPrompt = "summarize the project rules"

// claudeContainerVendorArgv mirrors what claude's buildArgs emits under
// SkipSetup. The mock's own personality selector is NOT part of it — main.go
// consumes the leading --claude before this reaches ParseArgv.
func claudeContainerVendorArgv() []string {
	return []string{"--print", "--output-format", "json", "--model", "mock-model"}
}

// codexContainerVendorArgv mirrors what codex's buildArgs emits: the exec
// subcommand, the sandbox tier, then the prompt as the TRAILING POSITIONAL.
func codexContainerVendorArgv() []string {
	return []string{"exec", "--sandbox", "read-only", containerPrompt}
}

// oneshotCLI resolves a backend's oneshot declaration through the same seam the
// mock binary uses, so these assertions read the live L1 declaration rather than
// a copy of it.
func oneshotCLI(t *testing.T, backend string) agent.EngineCLI {
	t.Helper()
	clis, ok := backends.EngineCLIsFor(backend)
	if !ok {
		t.Fatalf("backend %q declares no engine CLI to impersonate", backend)
	}
	cli, ok := agent.EngineCLIFor(clis, agent.CLISurfaceOneshot)
	if !ok {
		t.Fatalf("backend %q has no oneshot surface", backend)
	}
	return cli
}

// The container test hand-writes the vendor argv, so the mock constrains the
// DECLARATION, not the DRIVER. The hand-writing is real. The consequence is
// not: the driver is bound to the same declaration, in both
// directions, by its own anti-drift gates (TestEngineCLI_BuildArgsFlagsAreDeclared
// and TestEngineCLI_EveryDeclaredFlagIsEmitted in internal/claude and
// internal/codex), and the mock refuses any argv the declaration cannot read.
// Driver-versus-declaration drift therefore fails at the driver.
//
// What NOTHING pinned is the third edge: this hand-written argv against the
// declaration. It is only checked when docker is available, and the check that
// matters most is not checked even then — see the delivery test below. These two
// tests close that edge, in the default gate, with no container required.
func TestContainerArgv_ParsesAgainstTheLiveDeclaration(t *testing.T) {
	for _, tc := range []struct {
		backend string
		argv    []string
	}{
		{"claude-code", claudeContainerVendorArgv()},
		{"codex", codexContainerVendorArgv()},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			cli := oneshotCLI(t, tc.backend)
			if _, err := cli.ParseArgv(tc.argv); err != nil {
				t.Errorf("the container run's hand-written argv no longer parses against %s's declared grammar: %v",
					tc.backend, err)
			}
		})
	}
}

// The edge a green container run cannot catch. Each container test delivers the
// prompt on a channel it hard-codes — claude on cmd.Stdin, codex as the trailing
// positional — and the mock reads it from wherever the DECLARATION says. Move a
// declaration's delivery and the container run still exits 0 with a parseable
// report and a non-empty reply, having proved nothing about prompt delivery,
// because neither container test asserts PromptSHA256.
//
// So the channel each container test uses is asserted here against the live
// declaration instead.
func TestContainerArgv_PromptChannelMatchesTheDeclaration(t *testing.T) {
	claude := oneshotCLI(t, "claude-code")
	if claude.Prompt != agent.PromptStdin {
		t.Errorf("claude oneshot now declares prompt delivery %q, but the container test writes the prompt to cmd.Stdin",
			claude.Prompt)
	}

	codexCLI := oneshotCLI(t, "codex")
	if codexCLI.Prompt != agent.PromptPositional {
		t.Errorf("codex oneshot now declares prompt delivery %q, but the container test passes the prompt as a trailing argv positional",
			codexCLI.Prompt)
	}
	if codexCLI.Subcommand != "exec" {
		t.Errorf("codex oneshot now declares subcommand %q, but the container test hand-writes \"exec\"", codexCLI.Subcommand)
	}

	// And the positional the mock would read must be the prompt itself, not the
	// sandbox tier that precedes it: readPrompt takes the LAST positional.
	parsed, err := codexCLI.ParseArgv(codexContainerVendorArgv())
	if err != nil {
		t.Fatalf("codex container argv: %v", err)
	}
	n := len(parsed.Positionals)
	if n == 0 {
		t.Fatal("codex container argv carries no positional, so the mock would see no prompt at all")
	}
	if got := parsed.Positionals[n-1]; got != containerPrompt {
		t.Errorf("the last positional is %q, not the prompt — the mock reads the wrong token as the task", got)
	}
}
