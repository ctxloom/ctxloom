package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/spf13/afero"
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

// TestDoctorCheckMCPInvocation_WrongState_StaleEntryIsNamed: a materialized
// ctxloom entry that LAUNCHES ctxloom is stale in every spelling — ctxloom
// ships no stdio MCP server; its tools are served by the running session's
// endpoint, injected at session start. An engine launching such an entry
// starts normally, the command answers something that is not the protocol,
// and the session comes up with none of ctxloom's tools and nothing anywhere
// saying why. The report has to name the file, or a user with five engines
// configured cannot tell which one to fix.
func TestDoctorCheckMCPInvocation_WrongState_StaleEntryIsNamed(t *testing.T) {
	for name, body := range map[string]string{
		"the retired serve leaf": `{"mcpServers": {"ctxloom": {"command": "/usr/local/bin/ctxloom", "args": ["mcp", "serve"]}}}`,
		"the bare noun":          `{"mcpServers": {"ctxloom": {"command": "/usr/local/bin/ctxloom", "args": ["mcp"]}}}`,
		"a bare command":         `{"mcpServers": {"ctxloom": {"command": "ctxloom"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeSurface(t, root, ".mcp.json", body)

			check := doctorCheckMCPInvocation(engines.Registry(), root, nil, nil)

			assert.Equal(t, DoctorWarn, check.Status,
				"a stale entry is a real problem, and warn is this command's fail-loud signal")
			assert.Contains(t, check.Detail, ".mcp.json", "the report names the file to fix")
			assert.Contains(t, check.Remedy, "by hand", "no record of writing it: release would leave it, so the fix is by hand")
		})
	}
}

// TestDoctorCheckMCPInvocation_RightState_NoCtxloomEntryIsQuiet is the other
// half, and the one that keeps the check honest: a registry with no entry
// under ctxloom's name is the healthy at-rest state (the session injects its
// own endpoint), and a check that warned on every project would be
// indistinguishable from one that works.
func TestDoctorCheckMCPInvocation_RightState_NoCtxloomEntryIsQuiet(t *testing.T) {
	root := t.TempDir()
	writeSurface(t, root, ".mcp.json", `{"mcpServers": {"taskloom": {"command": "taskloom", "args": ["mcp"]}}}`)

	check := doctorCheckMCPInvocation(engines.Registry(), root, nil, nil)

	assert.Equal(t, DoctorOK, check.Status, "a registry naming no ctxloom launch is fine")
	assert.NotContains(t, check.Detail, ".mcp.json",
		"a healthy surface is not named as needing a fix")
}

// TestDoctorCheckMCPInvocation_ReadsEveryEngineNativeFormat pins that the
// check follows each engine to its OWN surface in its OWN format. Reading a
// surface an engine does not use would report a clean project to a user whose
// materialized entry is the broken one.
func TestDoctorCheckMCPInvocation_ReadsEveryEngineNativeFormat(t *testing.T) {
	staleFor := map[string]string{
		".mcp.json": `{"mcpServers": {"ctxloom": {"command": "/bin/ctxloom", "args": ["mcp", "serve"]}}}`,
	}

	for rel, body := range staleFor {
		t.Run(rel, func(t *testing.T) {
			root := t.TempDir()
			writeSurface(t, root, rel, body)

			check := doctorCheckMCPInvocation(engines.Registry(), root, nil, nil)

			assert.Equal(t, DoctorWarn, check.Status)
			assert.Contains(t, check.Detail, rel, "the report names the engine's own file")
		})
	}
}

// TestDoctorCheckMCPInvocation_SessionEndpointEntryIsQuiet: an entry under
// ctxloom's name that DIALS a URL is a session's rendering of the endpoint
// (a session home's registry, or a copy of one) — it launches nothing and is
// not stale.
func TestDoctorCheckMCPInvocation_SessionEndpointEntryIsQuiet(t *testing.T) {
	root := t.TempDir()
	writeSurface(t, root, ".mcp.json",
		`{"mcpServers": {"ctxloom": {"type": "http", "url": "http://127.0.0.1:4242/mcp", "headers": {"Authorization": "Bearer x"}}}}`)

	assert.Equal(t, DoctorOK, doctorCheckMCPInvocation(engines.Registry(), root, nil, nil).Status)
}

// TestDoctorCheckMCPInvocation_LeavesForeignServersAlone: only ctxloom's own
// entry is ctxloom's to judge. A user's own MCP server that happens to take an
// "mcp" argument is none of this check's business, and naming it would be a
// false alarm on a healthy project.
func TestDoctorCheckMCPInvocation_LeavesForeignServersAlone(t *testing.T) {
	root := t.TempDir()
	writeSurface(t, root, ".mcp.json",
		`{"mcpServers": {"taskloom": {"command": "taskloom", "args": ["mcp"]}}}`)

	assert.Equal(t, DoctorOK, doctorCheckMCPInvocation(engines.Registry(), root, nil, nil).Status,
		"another tool's invocation is not ctxloom's to correct")
}

// TestDoctorCheckMCPInvocation_NoSurfacesIsNotAProblem: a project that has
// materialized nothing yet has nothing stale, and saying so as `info` keeps
// the warn channel meaning "act on this".
func TestDoctorCheckMCPInvocation_NoSurfacesIsNotAProblem(t *testing.T) {
	assert.Equal(t, DoctorOK, doctorCheckMCPInvocation(engines.Registry(), t.TempDir(), nil, nil).Status)
	assert.Equal(t, DoctorInfo, doctorCheckMCPInvocation(engines.Registry(), "", nil, nil).Status,
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

	check := doctorCheckMCPInvocation(engines.Registry(), root, nil, nil)

	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, ".mcp.json", "the report names the file it could not read")
	assert.Contains(t, check.Detail, "unverified",
		"the report says the invocation was not checked, rather than implying it passed")
}

// TestDoctorCheckMCPInvocation_WrongState_AnAncestorsEntryIsNamed: claude reads
// .mcp.json from the working folder AND every parent, so a stale ctxloom entry
// in a parent folder kills every plain session in the project while the
// project's own file is clean. The report must name that parent file by its
// absolute path, and must not offer the project-side release as the fix: that
// release cannot reach a file outside the project.
func TestDoctorCheckMCPInvocation_WrongState_AnAncestorsEntryIsNamed(t *testing.T) {
	parent := t.TempDir()
	writeSurface(t, parent, ".mcp.json", `{"mcpServers": {"ctxloom": {"command": "ctxloom", "args": ["mcp"]}}}`)
	proj := filepath.Join(parent, "proj")
	require.NoError(t, os.MkdirAll(proj, 0o755))

	check := doctorCheckMCPInvocation(engines.Registry(), proj, nil, nil)

	assert.Equal(t, DoctorWarn, check.Status, "a parent folder's stale entry breaks the project's sessions")
	assert.Contains(t, check.Detail, filepath.Join(parent, ".mcp.json"), "the report names the parent file, absolute")
	assert.Contains(t, check.Detail, "by hand", "a file outside the project is fixed by hand")
	assert.Empty(t, check.Remedy, "the project-side release cannot remove an entry outside the project")
}

// TestProbePaths_CwdAncestorsRunsNearestFirstToTheRoot pins the one place a
// probe's scope becomes concrete paths.
func TestProbePaths_CwdAncestorsRunsNearestFirstToTheRoot(t *testing.T) {
	p := agent.CLIProbe{Kind: agent.ProbeKindMCP, Scope: agent.ScopeCwdAncestors, Rel: ".mcp.json"}

	got := probePaths(p, filepath.FromSlash("/a/b/c"), "/home/x")

	assert.Equal(t, []string{
		filepath.FromSlash("/a/b/c/.mcp.json"),
		filepath.FromSlash("/a/b/.mcp.json"),
		filepath.FromSlash("/a/.mcp.json"),
		filepath.FromSlash("/.mcp.json"),
	}, got, "nearest first, stopping at the filesystem root")

	assert.Equal(t, []string{filepath.FromSlash("/a/b/c/.mcp.json")},
		probePaths(agent.CLIProbe{Scope: agent.ScopeCwd, Rel: ".mcp.json"}, "/a/b/c", "/h"), "cwd stays one path")
	assert.Equal(t, []string{filepath.FromSlash("/h/.mcp.json")},
		probePaths(agent.CLIProbe{Scope: agent.ScopeHome, Rel: ".mcp.json"}, "/a", "/h"))
}

// recordProjectEntry makes this machine's ownership record say the project
// writer put a ctxloom entry in root's .mcp.json, by delivering it the way
// materialize does, then leaves the file holding the stale launch an older
// ctxloom wrote. Returns the record store and the filesystem it reads targets
// from.
func recordProjectEntry(t *testing.T, root string) (delivery.Ownership, afero.Fs) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	kind, ok := engines.Registry().Lookup(engine.Name("claude-code"))
	require.True(t, ok)
	pkg := composite.Package{MCP: map[string]wire.MCPServer{agent.MCPServerName: {Command: agent.CtxloomBinary, Args: []string{"mcp"}}}}
	_, _, err := Deliver(context.Background(), safefs.NewMem(fs), kind, pkg, delivery.Loadout{}, atRestPlacement(root, kind.Root().Name, delivery.AllKinds()))
	require.NoError(t, err)
	records, err := OwnershipRecordsOn(fs)
	require.NoError(t, err)
	states, err := records.Paths(fs, filepath.Join(root, ".mcp.json"))
	require.NoError(t, err)
	require.NotEmpty(t, states, "the delivery recorded the project writer's entry")
	writeSurface(t, root, ".mcp.json", `{"mcpServers": {"ctxloom": {"command": "ctxloom", "args": ["mcp"]}}}`)
	return records, fs
}

// TestDoctorCheckMCPInvocation_ARecordedEntryIsReleased: an in-project entry
// this machine recorded writing is one `materialize --release` takes out, so
// the release command is a true fix.
func TestDoctorCheckMCPInvocation_ARecordedEntryIsReleased(t *testing.T) {
	root := t.TempDir()
	records, fs := recordProjectEntry(t, root)

	check := doctorCheckMCPInvocation(engines.Registry(), root, records, fs)

	assert.Equal(t, DoctorWarn, check.Status)
	assert.Equal(t, doctorMCPReleaseRemedy, check.Remedy)
}

// TestDoctorCheckMCPInvocation_AnUnrecordedEntryIsDeletedByHand: release
// never removes what this machine has no record of writing, so for such an
// entry the release command would report "released" and leave it. The fix
// has to say to delete it, naming the file and the entry.
func TestDoctorCheckMCPInvocation_AnUnrecordedEntryIsDeletedByHand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeSurface(t, root, ".mcp.json", `{"mcpServers": {"ctxloom": {"command": "ctxloom", "args": ["mcp"]}}}`)
	fs := afero.NewOsFs()
	records, err := OwnershipRecordsOn(fs)
	require.NoError(t, err)

	check := doctorCheckMCPInvocation(engines.Registry(), root, records, fs)

	assert.Equal(t, DoctorWarn, check.Status)
	assert.NotContains(t, check.Remedy, "--release", "release would leave the entry")
	assert.Contains(t, check.Remedy, "by hand")
	assert.Contains(t, check.Remedy, filepath.Join(root, ".mcp.json"), "the fix names the file")
	assert.Contains(t, check.Remedy, `"`+agent.MCPServerName+`"`, "the fix names the entry key")
}

// TestDoctorCheckMCPInvocation_FollowingTheReleaseRemedyRemovesTheEntry: the
// remedy offered for a recorded entry really takes it out.
func TestDoctorCheckMCPInvocation_FollowingTheReleaseRemedyRemovesTheEntry(t *testing.T) {
	root := t.TempDir()
	records, fs := recordProjectEntry(t, root)
	require.Equal(t, doctorMCPReleaseRemedy, doctorCheckMCPInvocation(engines.Registry(), root, records, fs).Remedy)

	kind, ok := engines.Registry().Lookup(engine.Name("claude-code"))
	require.True(t, ok)
	require.NoError(t, Release(context.Background(), safefs.NewMem(fs), kind, atRestPlacement(root, kind.Root().Name, []present.Kind{present.MCP})))

	assert.Equal(t, DoctorOK, doctorCheckMCPInvocation(engines.Registry(), root, records, fs).Status)
}
