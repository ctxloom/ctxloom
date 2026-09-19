package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// stagePremisedProject is stageMinimalProject plus one premised fragment, so
// both surfaces under test have something conditional to list.
func stagePremisedProject(t *testing.T) string {
	t.Helper()
	workDir := stageMinimalProject(t)
	bundleDir := authoredV1(filepath.Join(workDir, ".ctxloom"))
	require.NoError(t, os.MkdirAll(bundleDir, 0o755))
	doc := `version: "1.0"
fragments:
  gamma:
    premise: You are about to remove a worktree.
    content: GAMMA-BODY
`
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "premised.yaml"), []byte(doc), 0o644))
	return workDir
}

// The premise catalog has two emitters — `ctxloom fragment premises` for a
// shell and ctxloom://fragments for an MCP client — and one source of the
// wording they emit: operations.PremiseSelectionInstruction. This drives BOTH
// real surfaces from the same built binary and pins their instruction text
// identical, and identical to the source. It reddens if either emitter stops
// calling the shared function: a re-worded copy on one side differs from the
// other, and a re-worded copy on both sides differs from the source.
//
// The wording was fixed by measurement and the apparatus that measured it is
// gone, so a copy that drifts cannot be re-derived back. One source, or the
// measured one loses.
func TestMCP_FragmentsResource_InstructionMatchesFragmentPremisesCLI(t *testing.T) {
	binPath := buildCtxloomBinary(t)
	workDir := stagePremisedProject(t)

	// CLI emitter: piped output resolves to JSON, so ask for it explicitly and
	// read the structured payload the way an agent running the command would.
	cli := exec.Command(binPath, "fragment", "premises", "--format", "json")
	cli.Dir = workDir
	cli.Env = testsupport.ScrubbedEnv(t)
	cliOut, err := cli.Output()
	require.NoError(t, err, "fragment premises --format json failed")
	var cliPayload struct {
		Instruction string `json:"instruction"`
		Fragments   []struct {
			Name string `json:"name"`
		} `json:"fragments"`
	}
	require.NoError(t, json.Unmarshal(cliOut, &cliPayload), "CLI output: %s", cliOut)
	require.NotEmpty(t, cliPayload.Fragments, "the staged premise must be listed, or the CLI side of the comparison is vacuous")

	// MCP emitter: the same binary, serving the resource over the wire.
	c := startMCPServer(t, binPath, workDir)
	initSession(t, c)
	body, _ := readResource(t, c, 106, "ctxloom://fragments")
	var mcpPayload struct {
		Instruction string `yaml:"instruction"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(body), &mcpPayload), "resource body: %s", body)

	want := operations.PremiseSelectionInstruction()
	assert.Equal(t, want, cliPayload.Instruction,
		"the CLI must emit the source function's wording verbatim")
	assert.Equal(t, want, mcpPayload.Instruction,
		"the MCP resource must emit the source function's wording verbatim")
	assert.Equal(t, cliPayload.Instruction, mcpPayload.Instruction,
		"both emitters must serve identical instruction text")
}
