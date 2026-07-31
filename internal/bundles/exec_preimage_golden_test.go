package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The exec preimage (ctxloom-exec/1, spec §3.3.2) is what an MCP / hook / skill
// approval binds to, so its bytes are a contract. U030-F05 and U030-F18 both
// claim that contract is not met — an approval can be re-keyed by a change that
// looks like nothing, or by a reimplementation that is faithful to the spec but
// not to Go.
//
// Neither is fixed here: changing these bytes invalidates every recorded
// approval on every machine and is the moment
// signing.ExecPreimageContract must be bumped. That is a trust-model decision,
// not a sweep's, so both rows are ESCALATED and this file records the CURRENT
// bytes exactly.
//
// The pins are therefore doing two jobs. They make the defects concrete for
// whoever decides, and they make any future change to the preimage announce
// itself here instead of silently invalidating approvals in the field.

// TestExecPreimage_NilAndEmptyArgsEnvDiffer records U030-F05: mcpContentPayload
// declares Args/Env WITHOUT omitempty, so an absent value and a present-but-
// empty value encode differently and therefore hash differently.
//
// The two YAML documents below describe the same server — a command with no
// arguments and no environment — and produce two different trust identities.
// Round-tripping a bundle through any writer that normalises `args: []` to an
// omitted key (or the reverse) silently invalidates the approval.
func TestExecPreimage_NilAndEmptyArgsEnvDiffer(t *testing.T) {
	absent := BundleMCP{Command: "srv"}
	present := BundleMCP{Command: "srv", Args: []string{}, Env: map[string]string{}}

	absentBytes, err := absent.ContentPayload()
	require.NoError(t, err)
	presentBytes, err := present.ContentPayload()
	require.NoError(t, err)

	assert.JSONEq(t,
		`{"preimage":"ctxloom-exec/1","command":"srv","args":null,"env":null,"installation":""}`,
		string(absentBytes))
	assert.JSONEq(t,
		`{"preimage":"ctxloom-exec/1","command":"srv","args":[],"env":{},"installation":""}`,
		string(presentBytes))

	assert.NotEqual(t, absent.ComputeContentHash(), present.ComputeContentHash(),
		"U030-F05: an absent and an empty args/env are the same server but not the same trust identity")
}

// TestExecPreimage_HTMLEscapingIsGoSpecific records U030-F18: all three exec
// preimages go through encoding/json.Marshal, whose default encoder escapes
// '<', '>' and '&' to \u003c, \u003e and \u0026 — a Go-specific behaviour
// that is NOT part of the documented ctxloom-exec/1 canonicalization.
//
// The consequence is not cosmetic. Any non-Go implementation of the spec, and
// any Go code using an encoder with SetEscapeHTML(false), computes a DIFFERENT
// preimage for the same server, and therefore cannot verify a countersignature
// this codebase produced. Shell redirects and '&&' in a command make these
// characters ordinary, not exotic.
func TestExecPreimage_HTMLEscapingIsGoSpecific(t *testing.T) {
	mcp := BundleMCP{Command: "sh", Args: []string{"-c", "a < b && c > d"}}
	got, err := mcp.ContentPayload()
	require.NoError(t, err)

	assert.Contains(t, string(got), `\u003c`, "'<' is escaped")
	assert.Contains(t, string(got), `\u003e`, "'>' is escaped")
	assert.Contains(t, string(got), `\u0026`, "'&' is escaped")
	assert.NotContains(t, string(got), "a < b",
		"the literal the author wrote does not appear in the bytes the approval covers")

	hook := BundleHook{Matcher: "*", Type: "command", Command: "echo <x> & y"}
	hookBytes, err := hook.ContentPayload()
	require.NoError(t, err)
	assert.Contains(t, string(hookBytes), `\u003c`)
	assert.Contains(t, string(hookBytes), `\u0026`)

	// The skill preimage shares the encoder, so a path or hash containing these
	// characters escapes identically. Recorded via the shared payload builder.
	skillBytes, err := skillPayloadFor(SkillManifest{
		{Path: "a<b.md", SHA256: "deadbeef", Mode: "0644"},
	})
	require.NoError(t, err)
	assert.Contains(t, string(skillBytes), `\u003c`)
}
