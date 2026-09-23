package coord

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The coordinator IS the verb set: a transport that holds a Verbs holds the
// coordinator, and a verb missing from the interface is a compile error here.
var _ Verbs = (*Coordinator)(nil)

// validating is what every request type is: the ONE validation site.
type validating interface{ Validate() error }

// TestVerbs_EveryRequestValidatesItselfOnce pins the contract every transport
// relies on: a request refused by Validate is refused with ErrInvalidRequest,
// and a well-formed one passes — so a handler that re-checked a field would
// be duplicating a refusal, not adding one.
func TestVerbs_EveryRequestValidatesItselfOnce(t *testing.T) {
	cases := []struct {
		name    string
		req     validating
		refused string // "" = valid
	}{
		{"spawn ok", SpawnRequest{Agent: "worker", Prompt: "go"}, ""},
		{"spawn with axes", SpawnRequest{Agent: "worker", Prompt: "go", Workspace: "worktree", DirtyTree: "fail"}, ""},
		{"spawn needs an agent", SpawnRequest{Prompt: "go"}, "agent is required"},
		{"spawn needs a prompt", SpawnRequest{Agent: "worker"}, "prompt is required"},
		{"spawn refuses an unknown workspace", SpawnRequest{Agent: "worker", Prompt: "go", Workspace: "sandbox"}, "workspace"},
		{"spawn refuses an unknown dirty-tree handler", SpawnRequest{Agent: "worker", Prompt: "go", DirtyTree: "yolo"}, "dirty_tree_handler"},
		{"send ok", SendRequest{To: "child-1", Kind: KindMessage, Body: "hello"}, ""},
		{"send reply carries its own kind", SendRequest{To: ParentAddress, Body: "42", InReplyTo: "ask-1"}, ""},
		{"send needs a recipient", SendRequest{Kind: KindMessage, Body: "hello"}, "to is required"},
		{"send needs a body", SendRequest{To: "child-1", Kind: KindMessage}, "body is required"},
		{"send refuses a reserved kind", SendRequest{To: "child-1", Kind: KindExited, Body: "x"}, "reserved"},
		{"send past the inline cap is not a request error", SendRequest{To: "child-1", Kind: KindMessage, Body: strings.Repeat("x", MaxInlineBodyBytes+1)}, ""},
		{"stop one child", StopRequest{Harp: "child-1"}, ""},
		{"stop all needs a reason", StopRequest{}, "reason is required"},
		{"stop all with a reason", StopRequest{Reason: "done"}, ""},
		{"report ok", ReportRequest{Scope: "progress", Body: "halfway"}, ""},
		{"report needs a body", ReportRequest{Scope: "progress"}, "body is required"},
		{"fetch ok", FetchRequest{Harp: "child-1", ArtifactID: "plan/x"}, ""},
		{"fetch needs an artifact id", FetchRequest{Harp: "child-1"}, "artifact_id is required"},
		{"control steer ok", ControlRequest{Verb: ControlVerbSteer, Harp: "child-1", Body: "rebase"}, ""},
		{"control pause takes no body", ControlRequest{Verb: ControlVerbPause, Harp: "child-1"}, ""},
		{"control steer needs a body", ControlRequest{Verb: ControlVerbSteer, Harp: "child-1"}, "body is required"},
		{"control refuses an unknown verb", ControlRequest{Verb: "nudge", Harp: "child-1"}, "verb"},
		{"host needs a tool", HostRequest{}, "tool is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if tc.refused == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidRequest)
			assert.Contains(t, err.Error(), tc.refused)
		})
	}
}
