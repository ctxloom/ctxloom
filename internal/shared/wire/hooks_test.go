package wire

import "testing"

func TestHooksConfig_HasAny(t *testing.T) {
	tests := []struct {
		name string
		cfg  HooksConfig
		want bool
	}{
		{"empty", HooksConfig{}, false},
		{
			"unified event populated",
			HooksConfig{Unified: UnifiedHooks{PostTool: []Hook{{Command: "x"}}}},
			true,
		},
		{
			"plugin event populated",
			HooksConfig{Plugins: map[string]BackendHooks{
				"claude-code": {"PostToolUse": []Hook{{Command: "x"}}},
			}},
			true,
		},
		{
			"plugin present but empty",
			HooksConfig{Plugins: map[string]BackendHooks{"claude-code": {}}},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.HasAny(); got != tt.want {
				t.Errorf("HasAny() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUnifiedHooks_Append(t *testing.T) {
	dst := UnifiedHooks{PostTool: []Hook{{Matcher: "X", Command: "x"}}}
	src := UnifiedHooks{
		PostTool:     []Hook{{Matcher: "Y", Command: "y"}},
		PostFileEdit: []Hook{{Matcher: "Z", Command: "z"}},
	}

	dst.Append(src)

	if len(dst.PostTool) != 2 {
		t.Fatalf("PostTool len = %d, want 2", len(dst.PostTool))
	}
	if dst.PostTool[1].Matcher != "Y" {
		t.Errorf("PostTool[1].Matcher = %q, want %q", dst.PostTool[1].Matcher, "Y")
	}
	if len(dst.PostFileEdit) != 1 {
		t.Errorf("PostFileEdit len = %d, want 1", len(dst.PostFileEdit))
	}
}

func TestUnifiedHooks_Append_AllEvents(t *testing.T) {
	dst := UnifiedHooks{}
	src := UnifiedHooks{
		PreTool:      []Hook{{Command: "pre-tool"}},
		PostTool:     []Hook{{Command: "post-tool"}},
		SessionStart: []Hook{{Command: "session-start"}},
		SessionEnd:   []Hook{{Command: "session-end"}},
		PreShell:     []Hook{{Command: "pre-shell"}},
		PostFileEdit: []Hook{{Command: "post-file-edit"}},
	}

	dst.Append(src)

	for _, c := range []struct {
		name string
		got  []Hook
	}{
		{"PreTool", dst.PreTool},
		{"PostTool", dst.PostTool},
		{"SessionStart", dst.SessionStart},
		{"SessionEnd", dst.SessionEnd},
		{"PreShell", dst.PreShell},
		{"PostFileEdit", dst.PostFileEdit},
	} {
		if len(c.got) != 1 {
			t.Errorf("%s len = %d, want 1", c.name, len(c.got))
		}
	}
}
