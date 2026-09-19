package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// linkedSkillsProject lays down a project whose default agent's profile ships
// one bundle with the corpus's measured miss: admit and unattended linked under
// one id, plus a linked skill the engine is not given (disabled for
// claude-code) and an unlinked skill beside them. CTXLOOM_ROOT points config.Load
// at it, as the hook's own process would be.
func linkedSkillsProject(t *testing.T) string {
	t.Helper()
	testsupport.Isolate(t)
	root := t.TempDir()
	t.Setenv(projectroot.EnvVar, root)

	appDir := filepath.Join(root, paths.AppDirName)
	require.NoError(t, os.MkdirAll(paths.ProfilesPath(appDir), 0o755))
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir),
		[]byte(fmt.Sprintf("version: %d\ndefault_agent: default\nagents:\n  default:\n    profiles:\n      - ops\n", config.CurrentConfigVersion)), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(paths.ProfilesPath(appDir), "ops.yaml"),
		[]byte("name: ops\nbundles:\n  - nightly\n"), 0o644))

	bundleDir := filepath.Join(paths.LocalBundlesPathFor(appDir, paths.LayoutV2), "nightly")
	require.NoError(t, os.WriteFile(mkdirp(t, bundleDir, "bundle.yaml"),
		[]byte("version: \"1.0\"\nskills:\n  admit:\n    tags: [ctxloom:link_id=nightly]\n  unattended:\n    tags: [ctxloom:link_id=nightly]\n"+
			"  withheld:\n    tags: [ctxloom:link_id=nightly]\n    llm:\n      claude-code:\n        enabled: false\n  free: {}\n"), 0o644))
	for _, name := range []string{"admit", "unattended", "withheld", "free"} {
		require.NoError(t, os.WriteFile(mkdirp(t, bundleDir, "skills", name, "SKILL.md"),
			[]byte("---\nname: "+name+"\ndescription: "+name+".\n---\n\nBody.\n"), 0o644))
	}
	return root
}

func mkdirp(t *testing.T, elems ...string) string {
	t.Helper()
	p := filepath.Join(elems...)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	return p
}

// postToolUsePayload is the JSON claude-code writes to a PostToolUse hook's
// stdin for one tool call.
func postToolUsePayload(transcriptPath, toolName, toolInput string) string {
	return `{"session_id":"s","hook_event_name":"PostToolUse","transcript_path":` + jsonString(transcriptPath) +
		`,"cwd":"/repo","tool_name":` + jsonString(toolName) + `,"tool_input":` + toolInput + `,"tool_response":"ok"}`
}

func skillPayload(transcriptPath, skill string) string {
	return postToolUsePayload(transcriptPath, claude.SkillToolName, `{"skill":"`+skill+`","args":""}`)
}

// skillTranscript is a session transcript in which the named skills were
// invoked, in order, through the Skill tool on the main thread.
func skillTranscript(t *testing.T, skills ...string) string {
	t.Helper()
	lines := []string{userPrompt("good night", "u1")}
	for i, s := range skills {
		lines = append(lines, assistantTool(fmt.Sprintf("a%d", i), fmt.Sprintf("msg_%d", i), claude.SkillToolName, `{"skill":"`+s+`","args":""}`))
	}
	return writeClaudeTranscript(t, lines...)
}

func skillMatesCmd(payload string) *cobra.Command {
	c := &cobra.Command{}
	c.SetIn(bytes.NewBufferString(payload))
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetContext(context.Background())
	return c
}

func additionalContext(out claude.PostToolUseOutput) string {
	if out.HookSpecificOutput == nil {
		return ""
	}
	return out.HookSpecificOutput.AdditionalContext
}

// TestSkillMatesOutput_MemberCompletesMateUninvoked_NamesTheMate is the verb
// end to end in process: real config, real bundle on disk, real session index,
// real transcript read through the engine's adapter. admit just completed and
// unattended has not run: the line names it.
//
// MUTATION -- have skillMatesOutput pass nil for the delivered skills -- turns
// this red while every silence test stays green.
func TestSkillMatesOutput_MemberCompletesMateUninvoked_NamesTheMate(t *testing.T) {
	linkedSkillsProject(t)
	seedHookSession(t, "claude-code")
	transcript := skillTranscript(t, "admit")

	out, err := skillMatesOutput(skillMatesCmd(skillPayload(transcript, "admit")))

	require.NoError(t, err)
	assert.Equal(t, claude.SkillMatesContext("admit", []string{"unattended"}), additionalContext(out))
}

// TestSkillMatesOutput_NamesOnlySkillsTheEngineHas: a linked skill that is
// disabled for claude-code was never materialized, so naming it would point
// the model at a skill it cannot invoke. The delivered set is filtered by the
// engine's own pick before mates are computed.
//
// MUTATION -- drop the SkillEnabled filter -- turns this red.
func TestSkillMatesOutput_NamesOnlySkillsTheEngineHas(t *testing.T) {
	linkedSkillsProject(t)
	seedHookSession(t, "claude-code")
	transcript := skillTranscript(t, "admit", "unattended")

	out, err := skillMatesOutput(skillMatesCmd(skillPayload(transcript, "admit")))

	require.NoError(t, err)
	assert.Nil(t, out.HookSpecificOutput, "withheld is linked but not the engine's; with unattended invoked there is nothing to name")
}

// TestSkillMatesOutput_AllMatesInvoked_Silent: the transcript already carries
// a Skill call for the mate, so the completion has nothing left to point at.
//
// MUTATION -- read the transcript but ignore it (pass nil events) -- turns
// this red.
func TestSkillMatesOutput_AllMatesInvoked_Silent(t *testing.T) {
	linkedSkillsProject(t)
	seedHookSession(t, "claude-code")
	transcript := skillTranscript(t, "unattended", "admit")

	out, err := skillMatesOutput(skillMatesCmd(skillPayload(transcript, "admit")))

	require.NoError(t, err)
	assert.Nil(t, out.HookSpecificOutput)
}

// TestSkillMatesOutput_NonMemberSkill_Silent: a delivered skill in no link
// group earns nothing.
func TestSkillMatesOutput_NonMemberSkill_Silent(t *testing.T) {
	linkedSkillsProject(t)
	seedHookSession(t, "claude-code")
	transcript := skillTranscript(t, "free")

	out, err := skillMatesOutput(skillMatesCmd(skillPayload(transcript, "free")))

	require.NoError(t, err)
	assert.Nil(t, out.HookSpecificOutput)
}

// TestSkillMatesOutput_NonSkillTool_SilentWithoutReadingAnything: for any
// other tool the answer is silence, and it is given before the session,
// transcript or config are touched -- with no harp in the environment and no
// transcript on disk, there is still no error to report.
//
// MUTATION -- move the InvokedSkill check after the transcript read -- turns
// this red (the read fails and an error surfaces).
func TestSkillMatesOutput_NonSkillTool_SilentWithoutReadingAnything(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv(agent.SessionHarpEnv, "")

	out, err := skillMatesOutput(skillMatesCmd(postToolUsePayload("/nonexistent/t.jsonl", "Bash", `{"command":"ls"}`)))

	require.NoError(t, err)
	assert.Nil(t, out.HookSpecificOutput)
}

// TestSkillMatesOutput_NamesWhyItStayedSilent pins that a Skill completion the
// hook could not follow up has a STATED reason, so exit 0 with nothing said is
// never one of the outcomes.
//
// MUTATION -- return (PostToolUseOutput{}, nil) from any one of the guards --
// turns the corresponding subtest red.
func TestSkillMatesOutput_NamesWhyItStayedSilent(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T)
		payload func(t *testing.T) string
	}{
		{"undecodable payload", func(t *testing.T) { linkedSkillsProject(t); seedHookSession(t, "claude-code") },
			func(t *testing.T) string { return "not json at all" }},
		{"no harp in the environment", func(t *testing.T) { linkedSkillsProject(t); t.Setenv(agent.SessionHarpEnv, "") },
			func(t *testing.T) string { return skillPayload(skillTranscript(t, "admit"), "admit") }},
		{"transcript cannot be read", func(t *testing.T) { linkedSkillsProject(t); seedHookSession(t, "claude-code") },
			func(t *testing.T) string { return skillPayload("/nonexistent/t.jsonl", "admit") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			out, err := skillMatesOutput(skillMatesCmd(tc.payload(t)))
			require.Error(t, err, "silence here must carry a reason")
			assert.Nil(t, out.HookSpecificOutput)
		})
	}
}

// TestRunHookSkillMates_AlwaysLeavesValidJSONAndExitsZero pins the hook
// contract: whatever went wrong, stdout is a JSON object the engine can parse
// and the exit status is 0 -- a PostToolUse hook that fails is a hook that
// interrupts the tool call it rode on.
func TestRunHookSkillMates_AlwaysLeavesValidJSONAndExitsZero(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv(agent.SessionHarpEnv, "")
	cmd := skillMatesCmd(skillPayload("/nonexistent/t.jsonl", "admit"))
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)

	require.NoError(t, runHookSkillMates(cmd, nil))

	var out claude.PostToolUseOutput
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &out), "stdout must be valid JSON: %q", stdout.String())
	assert.Nil(t, out.HookSpecificOutput)
}

// TestHookSkillMates_FiresOnTheWire drives the BUILT BINARY exactly as
// claude-code does: `ctxloom hook skill-mates` with the PostToolUse payload on
// stdin, the session harp in the environment, and the project root resolvable
// from the environment. The JSON on stdout must carry the line under
// hookSpecificOutput.additionalContext, the field the engine attaches.
//
// This is the assertion the in-process tests cannot make: that the verb is
// registered, hidden or not, under the name the installed hook command
// invokes, and that its stdout is the engine's wire shape rather than a Go
// value.
func TestHookSkillMates_FiresOnTheWire(t *testing.T) {
	bin := buildCtxloomBinary(t)
	linkedSkillsProject(t)
	seedHookSession(t, "claude-code")
	transcript := skillTranscript(t, "admit")

	run := func(payload string) map[string]any {
		t.Helper()
		cmd := exec.Command(bin, "hook", "skill-mates")
		cmd.Stdin = bytes.NewBufferString(payload)
		stderr := &bytes.Buffer{}
		cmd.Stderr = stderr
		stdout, err := cmd.Output()
		require.NoError(t, err, "hook must exit 0; stderr: %s", stderr.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(stdout, &out), "stdout must be one JSON object: %q", stdout)
		return out
	}

	fired := run(skillPayload(transcript, "admit"))
	specific, ok := fired["hookSpecificOutput"].(map[string]any)
	require.True(t, ok, "a mate left uninvoked must produce hookSpecificOutput: %v", fired)
	assert.Equal(t, claude.HookEventPostToolUse, specific["hookEventName"])
	assert.Equal(t, claude.SkillMatesContext("admit", []string{"unattended"}), specific["additionalContext"])

	silent := run(skillPayload(skillTranscript(t, "admit", "unattended"), "unattended"))
	assert.NotContains(t, silent, "hookSpecificOutput", "every mate invoked: the hook injects nothing")
}
