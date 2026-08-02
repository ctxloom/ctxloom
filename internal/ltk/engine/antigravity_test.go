package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// agRunCommandPayload is a verbatim capture from a live agy v1.0.7 PreToolUse
// hook (2026-06-10), not a hand-written approximation.
const agRunCommandPayload = `{
    "artifactDirectoryPath": "/home/u/.gemini/antigravity-cli/brain/c6a3e887-aeea-4f48-87d9-242525b5f482",
    "conversationId": "c6a3e887-aeea-4f48-87d9-242525b5f482",
    "stepIdx": 3,
    "toolCall": {
        "args": {
            "CommandLine": "echo hook-probe-1",
            "Cwd": "/tmp/agy-probe",
            "WaitMsBeforeAsync": 1000
        },
        "name": "run_command"
    },
    "transcriptPath": "/home/u/.gemini/antigravity-cli/brain/c6a3e887-aeea-4f48-87d9-242525b5f482/.system_generated/logs/transcript_full.jsonl",
    "workspacePaths": ["/tmp/agy-probe"]
}`

func TestAntigravityDecode(t *testing.T) {
	cases := []struct {
		name      string
		payload   string
		wantTool  string
		wantCmd   string
		wantShell ir.Shell
		wantPath  string
	}{
		{
			name:      "run_command (verbatim capture)",
			payload:   agRunCommandPayload,
			wantTool:  "run_command",
			wantCmd:   "echo hook-probe-1",
			wantShell: ir.ShellBash,
		},
		{
			name:      "execute_command",
			payload:   `{"toolCall":{"name":"execute_command","args":{"CommandLine":"rm -rf x","Cwd":"/w"}}}`,
			wantTool:  "execute_command",
			wantCmd:   "rm -rf x",
			wantShell: ir.ShellBash,
		},
		{
			name:     "write_to_file",
			payload:  `{"toolCall":{"name":"write_to_file","args":{"TargetFile":"/w/a.txt","CodeContent":"hi","Overwrite":true}}}`,
			wantTool: "write_to_file",
			wantPath: "/w/a.txt",
		},
		{
			name:     "replace_file_content",
			payload:  `{"toolCall":{"name":"replace_file_content","args":{"TargetFile":"/w/a.txt","TargetContent":"hi","ReplacementContent":"yo"}}}`,
			wantTool: "replace_file_content",
			wantPath: "/w/a.txt",
		},
		{
			name:     "unknown tool fails open with empty request",
			payload:  `{"toolCall":{"name":"browser_navigate","args":{}}}`,
			wantTool: "browser_navigate",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := Antigravity{}.Decode([]byte(c.payload))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if req.ToolName != c.wantTool || req.Command != c.wantCmd ||
				req.Shell != c.wantShell || req.FilePath != c.wantPath {
				t.Fatalf("req = %+v, want tool=%q cmd=%q shell=%q path=%q",
					req, c.wantTool, c.wantCmd, c.wantShell, c.wantPath)
			}
		})
	}
}

func TestAntigravityDecodeMalformed(t *testing.T) {
	if _, err := (Antigravity{}).Decode([]byte("{not json")); err == nil {
		t.Fatal("malformed payload must error")
	}
}

func TestAntigravityEncodeAllowIsSilent(t *testing.T) {
	out, err := Antigravity{}.Encode(Response{Allow: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Stdout) != 0 || len(out.Stderr) != 0 || out.ExitCode != 0 {
		t.Errorf("allow should be a zero Output, got %+v", out)
	}
}

func TestAntigravityEncodeDeny(t *testing.T) {
	out, err := Antigravity{}.Encode(Response{
		Allow:   false,
		Reason:  "Use just test.",
		Suggest: "just test",
	})
	if err != nil {
		t.Fatal(err)
	}
	// agy fails OPEN on non-zero exit, so a deny MUST be exit 0 + decision JSON.
	if out.ExitCode != 0 {
		t.Errorf("Antigravity deny uses exit 0 + JSON, got exit %d", out.ExitCode)
	}
	var decoded struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(out.Stdout, &decoded); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if decoded.Decision != "deny" {
		t.Fatalf("decision = %q, want deny", decoded.Decision)
	}
	if !strings.Contains(decoded.Reason, "just test") {
		t.Errorf("reason missing suggestion: %q", decoded.Reason)
	}
}

// TestAntigravityEncodeDeny_ReasonlessRuleStillCarriesAReason pins the
// invariant that a reasonless deny must still carry a reason, at the layer
// that actually decides it.
//
// The row read `antigravitycli.EncodeDeny("")` — which emits
// {"decision":"deny"} with no reason, so agy renders "Tool call denied with
// reason: " — as a live silent no-op. It is not reachable: this Encode is the
// only caller in the repo (the wire types live in an internal/ package, so
// there can be no other), and it passes Response.Message(), which supplies
// "denied by ltk (no reason or suggested alternative was configured for this
// rule)" precisely when a rule carries neither. The fallback belongs there
// rather than in EncodeDeny: making EncodeDeny refuse an empty reason would turn
// Encode into an error return, and an ltk hook that exits non-zero makes agy
// FAIL OPEN — the tool call it was denying would proceed. So the reasonless
// deny must stay a well-formed deny, and this test is what keeps it one.
func TestAntigravityEncodeDeny_ReasonlessRuleStillCarriesAReason(t *testing.T) {
	out, err := Antigravity{}.Encode(Response{Allow: false})
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 0 {
		t.Errorf("deny is exit 0 + JSON (agy fails OPEN on non-zero), got exit %d", out.ExitCode)
	}
	var decoded struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(out.Stdout, &decoded); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if decoded.Decision != "deny" {
		t.Fatalf("decision = %q, want deny", decoded.Decision)
	}
	if strings.TrimSpace(decoded.Reason) == "" {
		t.Error("a rule with no reason and no suggestion must still tell the model why: agy renders this field verbatim after \"Tool call denied with reason: \"")
	}
}

func TestGetAntigravity(t *testing.T) {
	for _, name := range []string{"antigravity", "agy", "antigravity-cli", "ANTIGRAVITY"} {
		e, err := Get(name)
		if err != nil {
			t.Errorf("%q should resolve: %v", name, err)
			continue
		}
		if e.Name() != "antigravity" {
			t.Errorf("Get(%q) = %s", name, e.Name())
		}
	}
	for _, name := range []string{"anti", "a", "gravity"} {
		if _, err := Get(name); err == nil {
			t.Errorf("%q should not resolve to an engine", name)
		}
	}
}
