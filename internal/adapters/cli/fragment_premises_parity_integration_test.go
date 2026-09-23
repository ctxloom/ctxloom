package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	runnermcp "github.com/ctxloom/ctxloom/internal/adapters/runner/mcp"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// stagePremisedProject sets up a project with one premised fragment and a
// config that trips no review-gate logic on startup, so the CLI surface under
// test has something conditional to list.
func stagePremisedProject(t *testing.T) string {
	t.Helper()
	workDir := t.TempDir()
	appDir := filepath.Join(workDir, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(fmt.Sprintf("version: %d\nllm:\n  configs:\n    claude-code:\n      type: claude-code\n", config.CurrentConfigVersion)), 0o644))
	bundleDir := authoredV1(filepath.Join(workDir, ".ctxloom"))
	require.NoError(t, os.MkdirAll(bundleDir, 0o755))
	doc := `version: "1.0"
fragments:
  gamma:
    premise: You are about to remove a worktree.
    content: GAMMA-BODY
`
	bundletree.WriteOS(t, bundleDir, "premised", doc)
	return workDir
}

// The premise catalog has two emitters — `ctxloom fragment premises` for a
// shell and the session endpoint's server instructions for an MCP client
// (operations.SessionInstructions, which every session's runner advertises
// at initialize and points at the ctxloom://fragments catalog) — and one
// source of the wording they emit: operations.PremiseSelectionInstruction.
// This drives the CLI from the built binary and the endpoint's surface from
// the same registration the runner serves (runnermcp.NewDocServer), and pins
// the instruction text identical, and identical to the source. It reddens if
// either emitter stops calling the shared function.
//
// The wording was fixed by measurement and the apparatus that measured it is
// gone, so a copy that drifts cannot be re-derived back. One source, or the
// measured one loses.
func TestMCP_SessionInstructions_MatchFragmentPremisesCLI(t *testing.T) {
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

	// MCP emitter: the session endpoint's advertised instructions, from the
	// same server the runner serves.
	server, closeHome, err := runnermcp.NewDocServer()
	require.NoError(t, err)
	defer closeHome()
	ctx := t.Context()
	serverT, clientT := mcp.NewInMemoryTransports()
	_, err = server.Connect(ctx, serverT, nil)
	require.NoError(t, err)
	client := mcp.NewClient(&mcp.Implementation{Name: "premise-parity"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	require.NoError(t, err)
	defer cs.Close()
	advertised := cs.InitializeResult().Instructions

	want := operations.PremiseSelectionInstruction()
	assert.Equal(t, want, cliPayload.Instruction,
		"the CLI must emit the source function's wording verbatim")
	assert.Contains(t, advertised, want,
		"the session endpoint must advertise the source function's wording verbatim")
}
