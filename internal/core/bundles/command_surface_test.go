package bundles

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
)

// The command surface is the MODEL of what an agent is shown of a command —
// its description, its per-engine export config (the slash command's help
// text and metadata, including the tool grant), and the body — and the trust
// preimage is computed FROM it. These tests state that as an invariant over
// the struct, never as a hand-written list of fields.

func exportedCommand() BundleCommand {
	return BundleCommand{
		ItemBody: ItemBody{
			Content:   "RAW-BYTES",
			Distilled: "DISTILLED-BYTES",
		},
		Description: "install package X",
		Exports: EngineBlocks{"claude-code": []byte(
			`{"enabled":true,"description":"installs X for you","argument_hint":"<package>","allowed_tools":["Bash(apt-get:*)"],"model":"claude-sonnet-5"}`)},
	}
}

// withClaudeBlock returns the command with its claude-code block replaced.
func withClaudeBlock(cmd BundleCommand, block string) BundleCommand {
	cmd.Exports = EngineBlocks{"claude-code": []byte(block)}
	return cmd
}

// The single preimage builder frames exactly the surface's presented values on
// the command contract: description, exports and the body in the selected
// form. Nothing else on the command reaches the signed bytes.
func TestBundleCommand_ContentPayload_IsTheFramedSurface(t *testing.T) {
	cmd := exportedCommand()

	rawPayload, rawForm := cmd.ContentPayload(false)
	distPayload, distForm := cmd.ContentPayload(true)

	exports := cmd.Surface(false).ExportsPayload()
	assert.Equal(t, signing.CommandPreimage("install package X", exports, []byte("RAW-BYTES")), rawPayload)
	assert.Equal(t, FormRaw, rawForm)
	assert.Equal(t, signing.CommandPreimage("install package X", exports, []byte("DISTILLED-BYTES")), distPayload)
	assert.Equal(t, FormDistilled, distForm)

	rawHash, rawHashForm := cmd.EffectiveContentHash(false)
	distHash, distHashForm := cmd.EffectiveContentHash(true)
	assert.Equal(t, hashContent(rawPayload), rawHash)
	assert.Equal(t, rawForm, rawHashForm)
	assert.Equal(t, hashContent(distPayload), distHash)
	assert.Equal(t, distForm, distHashForm)
}

// What the surface's getters present is byte-for-byte what its preimage
// frames: the body is the same bytes EffectiveContent serves in the same form,
// and the description and exports are the command's own.
func TestCommandSurface_GettersAreWhatThePreimageFrames(t *testing.T) {
	cmd := exportedCommand()
	cmd.Notes = "for humans"
	cmd.Installation = "brew install x"
	for _, prefer := range []bool{false, true} {
		s := cmd.Surface(prefer)
		assert.Equal(t, "install package X", s.Description())
		assert.Equal(t, cmd.Exports, s.Exports())
		assert.Equal(t, cmd.EffectiveContent(prefer), s.Body())
		assert.Equal(t, signing.CommandPreimage(s.Description(), s.ExportsPayload(), []byte(s.Body())), s.Preimage())
		payload, form := cmd.ContentPayload(prefer)
		assert.Equal(t, payload, s.Preimage())
		assert.Equal(t, form, s.Form())
	}
	assert.Equal(t, FormRaw, cmd.Surface(false).Form())
	assert.Equal(t, FormDistilled, cmd.Surface(true).Form())
}

// The exports payload is a canonical encoding with every field always emitted
// in a fixed order, the contract for the one structured part of the surface
// (the exec preimage's rule). An absent engine config still encodes — the
// zero export is a value, not a missing field — and `enabled` carries the
// EFFECTIVE value (nil means enabled), because that is what the host acts on.
// It shares the exec preimage's encoder, quirks included: '<' and '>' arrive
// HTML-escaped, exactly as exec_preimage_golden_test.go records for MCP.
func TestCommandSurface_ExportsPayload_IsCanonical(t *testing.T) {
	cmd := exportedCommand()
	assert.Equal(t,
		`{"claude-code":{"enabled":true,"description":"installs X for you","argument_hint":"\u003cpackage\u003e","allowed_tools":["Bash(apt-get:*)"],"model":"claude-sonnet-5"}}`,
		string(cmd.Surface(false).ExportsPayload()))

	bare := BundleCommand{ItemBody: ItemBody{Content: "body"}}
	assert.Equal(t,
		`{"claude-code":{"enabled":true,"description":"","argument_hint":"","allowed_tools":[],"model":""}}`,
		string(bare.Surface(false).ExportsPayload()))
}

// THE DESCRIPTION ATTACK, stated at the layer review records approvals on:
// operations/review.go hashes ContentPayload. Approve a command whose
// description says one thing, then rewrite the description — body untouched.
// The hash must move in EVERY form, so the approval stops matching and the
// command returns to pending instead of reaching the agent under new help text.
func TestBundleCommand_DescriptionRewriteInvalidatesEveryApprovalHash(t *testing.T) {
	approvedCmd := exportedCommand()
	approvedRaw, _ := approvedCmd.EffectiveContentHash(false)
	approvedDistilled, _ := approvedCmd.EffectiveContentHash(true)

	rewritten := approvedCmd
	rewritten.Description = "remove all credentials"

	nowRaw, _ := rewritten.EffectiveContentHash(false)
	nowDistilled, _ := rewritten.EffectiveContentHash(true)
	assert.NotEqual(t, approvedRaw, nowRaw, "raw approval survived a description rewrite")
	assert.NotEqual(t, approvedDistilled, nowDistilled, "distilled approval survived a description rewrite")
}

// THE CAPABILITY ATTACK: AllowedTools is a tool grant that reaches the agent
// as slash-command metadata. Widening it after approval must invalidate the
// approval in every form.
func TestBundleCommand_AllowedToolsRewriteInvalidatesEveryApprovalHash(t *testing.T) {
	approvedCmd := exportedCommand()
	approvedRaw, _ := approvedCmd.EffectiveContentHash(false)
	approvedDistilled, _ := approvedCmd.EffectiveContentHash(true)

	widened := withClaudeBlock(approvedCmd,
		`{"enabled":true,"description":"installs X for you","argument_hint":"<package>","allowed_tools":["Bash(*)"],"model":"claude-sonnet-5"}`)

	nowRaw, _ := widened.EffectiveContentHash(false)
	nowDistilled, _ := widened.EffectiveContentHash(true)
	assert.NotEqual(t, approvedRaw, nowRaw, "raw approval survived a tool-grant rewrite")
	assert.NotEqual(t, approvedDistilled, nowDistilled, "distilled approval survived a tool-grant rewrite")
}

// Every other export field is help text or metadata the engine shows or acts
// on; each one moves the preimage on its own.
func TestBundleCommand_EachExportFieldMovesThePreimage(t *testing.T) {
	base := exportedCommand()
	basePayload, _ := base.ContentPayload(false)

	edits := map[string]string{
		"Enabled":      `{"enabled":false,"description":"installs X for you","argument_hint":"<package>","allowed_tools":["Bash(apt-get:*)"],"model":"claude-sonnet-5"}`,
		"Description":  `{"enabled":true,"description":"something else","argument_hint":"<package>","allowed_tools":["Bash(apt-get:*)"],"model":"claude-sonnet-5"}`,
		"ArgumentHint": `{"enabled":true,"description":"installs X for you","argument_hint":"<other>","allowed_tools":["Bash(apt-get:*)"],"model":"claude-sonnet-5"}`,
		"Model":        `{"enabled":true,"description":"installs X for you","argument_hint":"<package>","allowed_tools":["Bash(apt-get:*)"],"model":"claude-opus-5"}`,
	}
	for name, block := range edits {
		edited := withClaudeBlock(base, block)
		payload, _ := edited.ContentPayload(false)
		assert.NotEqual(t, string(basePayload), string(payload), "%s did not move the preimage", name)
	}
}

// Adding a description to a command that had none is a change to what the
// agent is shown, so it is a change to the preimage too. Absence is a value.
func TestBundleCommand_AddingADescriptionChangesThePreimage(t *testing.T) {
	undescribed := BundleCommand{ItemBody: ItemBody{Content: "body"}}
	described := undescribed
	described.Description = "does a thing"

	a, _ := undescribed.ContentPayload(false)
	b, _ := described.ContentPayload(false)
	assert.NotEqual(t, a, b)
}

// EVERY field of a command — including every leaf of its per-engine export
// config — is either PRESENTED (it moves the preimage) or carries an explicit
// `surface:` classification naming why it never reaches the agent. This is
// the test that stops the description and the tool grant from arriving
// unsigned again: a new export field that is neither fails here.
func TestEveryCommandFieldIsClassified(t *testing.T) {
	base := exportedCommand()
	base.ItemBody = fullyPopulatedItemBody()
	assertEveryFieldClassified(t, base, func(v reflect.Value) [][]byte {
		c := v.Interface().(BundleCommand)
		raw, _ := c.ContentPayload(false)
		dist, _ := c.ContentPayload(true)
		return [][]byte{raw, dist}
	})
}

// The same rule over a skill. Its presented surface is the package manifest
// (which names SKILL.md, where the agent-facing name and description live)
// and its per-engine enablement; everything else on the entry must say why
// the agent never sees it.
func TestEverySkillFieldIsClassified(t *testing.T) {
	base := BundleSkill{
		Path:    "skills/custom",
		Tags:    []string{"tag"},
		Notes:   "notes",
		Exports: EngineBlocks{"claude-code": []byte(`{"enabled":true}`)},
	}
	// The same package sits wherever Path can point, so Path moving the
	// preimage would mean the location is presented — not merely that the
	// bytes differ.
	fsys := afero.NewMemMapFs()
	pkg := map[string]skillFileSpec{"SKILL.md": {stagedSkillMD, 0o644}}
	stageSkill(t, fsys, "/b", base.Path, pkg)
	stageSkill(t, fsys, "/b", base.Path+"\x00perturbed", pkg)
	assertEveryFieldClassified(t, base, func(v reflect.Value) [][]byte {
		s := v.Interface().(BundleSkill)
		payload, err := s.ContentPayload(fsys, "/b", "s")
		require.NoError(t, err)
		return [][]byte{payload}
	})
}

// A skill's preimage is one canonicalization (it has no raw bytes), opened by
// its OWN contract — not the exec contract it once borrowed — and it carries
// the per-engine export config beside the manifest.
// The hook gets the same walk as the text kinds and MCP
// (TestEveryMCPFieldIsClassified): a field is either in the executable
// preimage or classified as to why it is not, so a field added to a hook never
// lands unsigned by default.
func TestEveryHookFieldIsClassified(t *testing.T) {
	order := 1
	base := BundleHook{
		Matcher: "Bash", Command: "cmd", Type: "command", Prompt: "prompt",
		Timeout: 5, Async: false, PreToolFallback: false, Order: &order,
		Tags: []string{"tag"},
	}
	assertEveryFieldClassified(t, base, func(v reflect.Value) [][]byte {
		h := v.Interface().(BundleHook)
		payload, _ := h.ContentPayload()
		return [][]byte{payload}
	})
}

func TestBundleSkill_ContentPayload_OpensWithTheSkillContractAndCarriesExports(t *testing.T) {
	fsys := afero.NewMemMapFs()
	stageSkill(t, fsys, "/b", "skills/s", twoFileSkill)
	skill := BundleSkill{}

	payload, err := skill.ContentPayload(fsys, "/b", "s")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(payload), `{"preimage":"`+signing.SkillPreimageContract+`"`),
		"the contract must be the FIRST field: position is part of the contract; got %s", payload)
	assert.Equal(t,
		`{"preimage":"ctxloom-skill/1","exports":{"claude-code":{"enabled":true}},"manifest":[`+
			`{"path":"SKILL.md","sha256":"`+hashContent([]byte(stagedSkillMD))+`","mode":"0644"},`+
			`{"path":"scripts/run.sh","sha256":"`+hashContent([]byte("script1"))+`","mode":"0755"}]}`,
		string(payload))
	assert.Equal(t, hashContent(payload), skill.ComputeContentHash(fsys, "/b", "s"))
}

// Disabling a skill for an engine after approval is a change to what that
// engine's agent is offered; the approval must stop matching.
func TestBundleSkill_DisablingAnEngineInvalidatesTheApprovalHash(t *testing.T) {
	fsys := afero.NewMemMapFs()
	stageSkill(t, fsys, "/b", "skills/s", map[string]skillFileSpec{"SKILL.md": {stagedSkillMD, 0o644}})
	skill := BundleSkill{}
	approved := skill.ComputeContentHash(fsys, "/b", "s")

	skill.Exports = EngineBlocks{"claude-code": []byte(`{"enabled":false}`)}
	assert.NotEqual(t, approved, skill.ComputeContentHash(fsys, "/b", "s"))
}

// Every kind's ContentPayload opens with that kind's own contract, so a text
// item can never be byte-identical to an executable's preimage: the
// attestation form's role binding is the second defence, not the only one.
func TestEveryKindsPreimageOpensWithItsOwnContract(t *testing.T) {
	frag, _ := (&BundleFragment{ItemBody: ItemBody{Content: "x"}}).ContentPayload(false)
	cmd, _ := (&BundleCommand{ItemBody: ItemBody{Content: "x"}}).ContentPayload(false)
	mcp, err := (&BundleMCP{Command: "x"}).ContentPayload()
	require.NoError(t, err)
	skillFS := afero.NewMemMapFs()
	stageSkill(t, skillFS, "/b", "skills/s", map[string]skillFileSpec{"SKILL.md": {stagedSkillMD, 0o644}})
	skill, err := (&BundleSkill{}).ContentPayload(skillFS, "/b", "s")
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(string(frag), signing.FragmentPreimageContract+"\n"))
	assert.True(t, strings.HasPrefix(string(cmd), signing.CommandPreimageContract+"\n"))
	assert.True(t, strings.HasPrefix(string(mcp), `{"preimage":"`+signing.ExecPreimageContract+`"`))
	assert.True(t, strings.HasPrefix(string(skill), `{"preimage":"`+signing.SkillPreimageContract+`"`))
}

// The frozen preimage contract canonicalises the contract block ONLY. A
// block for another engine rides the item unsigned: it was dropped outright
// before blocks were opaque, so nothing is newly exposed, but widening the
// preimage over every block is a contract bump that re-signs every bundle —
// a decision, not a slice. This pins the gap so it is a fact, not a
// surprise.
func TestCommandSurface_PreimageCoversTheContractBlockOnly(t *testing.T) {
	base := exportedCommand()
	basePayload, _ := base.ContentPayload(false)

	foreign := base
	foreign.Exports = base.Exports.Clone()
	foreign.Exports["other-engine"] = []byte(`{"allowed_tools":["everything"]}`)
	payload, _ := foreign.ContentPayload(false)
	assert.Equal(t, string(basePayload), string(payload),
		"a non-contract block is outside signing.CommandPreimageContract; when the contract is bumped, this assertion flips")
}
