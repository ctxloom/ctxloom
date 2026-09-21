package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeSurface materializes one engine's MCP registry under root, creating the
// directories the engine would have created itself.
func writeSurface(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// TestDoctorCheckMCPInvocation_WrongState_StaleEntryIsNamed drives the shape
// this check exists for: a settings file materialized before the machine
// surface moved to `mcp serve`, still naming the noun on its own.
//
// The engine starts, the entry launches ctxloom, ctxloom answers a listing
// into a JSON-RPC pipe, and the session comes up with none of ctxloom's tools
// and nothing anywhere saying why. The report has to name the file, or a user
// with five engines configured cannot tell which one to fix.
func TestDoctorCheckMCPInvocation_WrongState_StaleEntryIsNamed(t *testing.T) {
	root := t.TempDir()
	writeSurface(t, root, ".mcp.json", `{
	  "mcpServers": {
	    "ctxloom": {"command": "/usr/local/bin/ctxloom", "args": ["mcp"]}
	  }
	}`)

	check := doctorCheckMCPInvocation(root)

	assert.Equal(t, DoctorWarn, check.Status,
		"a stale entry is a real problem, and warn is this command's fail-loud signal")
	assert.Contains(t, check.Detail, ".mcp.json", "the report names the file to fix")
	assert.Contains(t, check.Detail, "ctxloom init", "the report names re-init as the fix")
}

// TestDoctorCheckMCPInvocation_RightState_CurrentEntryIsQuiet is the other
// half, and the one that keeps the check honest. A check that warned on every
// project would be indistinguishable from one that works, and would teach a
// user to ignore doctor.
func TestDoctorCheckMCPInvocation_RightState_CurrentEntryIsQuiet(t *testing.T) {
	root := t.TempDir()
	writeSurface(t, root, ".mcp.json", `{
	  "mcpServers": {
	    "ctxloom": {"command": "/usr/local/bin/ctxloom", "args": ["mcp", "serve"]}
	  }
	}`)

	check := doctorCheckMCPInvocation(root)

	assert.Equal(t, DoctorOK, check.Status, "an entry naming the server leaf is fine")
	assert.NotContains(t, check.Detail, ".mcp.json",
		"a healthy surface is not named as needing a fix")
}

// TestDoctorCheckMCPInvocation_ReadsEveryEngineNativeFormat pins that the
// check follows each engine to its OWN surface in its OWN format. Reading a
// surface an engine does not use would report a clean project to a user whose
// materialized entry is the broken one.
func TestDoctorCheckMCPInvocation_ReadsEveryEngineNativeFormat(t *testing.T) {
	staleFor := map[string]string{
		".mcp.json": `{"mcpServers": {"ctxloom": {"command": "/bin/ctxloom", "args": ["mcp"]}}}`,
	}

	for rel, body := range staleFor {
		t.Run(rel, func(t *testing.T) {
			root := t.TempDir()
			writeSurface(t, root, rel, body)

			check := doctorCheckMCPInvocation(root)

			assert.Equal(t, DoctorWarn, check.Status)
			assert.Contains(t, check.Detail, rel, "the report names the engine's own file")
		})
	}
}

// TestDoctorCheckMCPInvocation_RightState_CurrentEntryInEveryFormatIsQuiet is
// the paired negative for the walk above: each format's CORRECT spelling must
// read as healthy, or the check is just a file-exists probe wearing a warning.
func TestDoctorCheckMCPInvocation_RightState_CurrentEntryInEveryFormatIsQuiet(t *testing.T) {
	currentFor := map[string]string{
		".mcp.json": `{"mcpServers": {"ctxloom": {"command": "/bin/ctxloom", "args": ["mcp", "serve"]}}}`,
	}

	for rel, body := range currentFor {
		t.Run(rel, func(t *testing.T) {
			root := t.TempDir()
			writeSurface(t, root, rel, body)

			assert.Equal(t, DoctorOK, doctorCheckMCPInvocation(root).Status)
		})
	}
}

// TestDoctorCheckMCPInvocation_LeavesForeignServersAlone: only ctxloom's own
// entry is ctxloom's to judge. A user's own MCP server that happens to take an
// "mcp" argument is none of this check's business, and naming it would be a
// false alarm on a healthy project.
func TestDoctorCheckMCPInvocation_LeavesForeignServersAlone(t *testing.T) {
	root := t.TempDir()
	writeSurface(t, root, ".mcp.json",
		`{"mcpServers": {"taskloom": {"command": "taskloom", "args": ["mcp"]}}}`)

	assert.Equal(t, DoctorOK, doctorCheckMCPInvocation(root).Status,
		"another tool's invocation is not ctxloom's to correct")
}

// TestDoctorCheckMCPInvocation_NoSurfacesIsNotAProblem: a project that has
// materialized nothing yet has nothing stale, and saying so as `info` keeps
// the warn channel meaning "act on this".
func TestDoctorCheckMCPInvocation_NoSurfacesIsNotAProblem(t *testing.T) {
	assert.Equal(t, DoctorOK, doctorCheckMCPInvocation(t.TempDir()).Status)
	assert.Equal(t, DoctorInfo, doctorCheckMCPInvocation("").Status,
		"with no project located there is nothing to inspect")
}

// TestDoctorCheckMCPInvocation_UnreadableSurfaceIsNotHealth: "I could not read
// it" is not "it is fine". A registry that failed to parse may hold the very
// entry this check is looking for, so reporting ok beside it would claim an
// inspection that did not happen — and the user would be told their engines
// are wired correctly on the strength of a file nobody could open.
func TestDoctorCheckMCPInvocation_UnreadableSurfaceIsNotHealth(t *testing.T) {
	root := t.TempDir()
	writeSurface(t, root, ".mcp.json", `{"mcpServers": {`)

	check := doctorCheckMCPInvocation(root)

	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, ".mcp.json", "the report names the file it could not read")
	assert.Contains(t, check.Detail, "unverified",
		"the report says the invocation was not checked, rather than implying it passed")
}
