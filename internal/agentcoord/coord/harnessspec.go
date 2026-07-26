package coord

import (
	"fmt"

	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file is the ONE place StartRun's HarnessSpec is built (coordinator
// side, issuing StartRun) and decoded (runner side, receiving it) — the C0
// convention documented in coordination.proto lives in code here, so the two
// ends cannot drift apart. HarnessSpec.config carries everything the A3
// typed-field boundary leaves untyped: env, MCP servers, and the delegated
// child's session harp (key "ctxloom.session_harp") — permission_mode is the
// one typed field (security-relevant, per A3/D3).
const (
	// harnessConfigKeySessionHarp is the C0/A8 Struct-key convention.
	harnessConfigKeySessionHarp = "ctxloom.session_harp"
	// harnessConfigKeyEnv holds the child engine's extra environment as a
	// flat string->string object (mirrors operations.AgentChatRequest.Env).
	harnessConfigKeyEnv = "env"
	// harnessConfigKeyMCPServers holds the composed managed MCP set as an
	// array of {name, command, args, env} objects (mirrors
	// agent.ChatMCPServer — the same shape childMCPServers already builds
	// for the legacy path).
	harnessConfigKeyMCPServers = "mcp_servers"
)

// HarnessSpecInput is everything buildHarnessSpec needs beyond what StartRun
// carries directly (task_id/run_id/parent_run_id/role/input/budget) — the
// HarnessSpec message itself.
type HarnessSpecInput struct {
	Harness         string
	Model           string
	Workspace       string
	Env             map[string]string
	MCPServers      []agent.ChatMCPServer
	SessionHarp     string
	Permission      agent.PermissionMode
	ResumeSessionID string
}

// buildHarnessSpec encodes a HarnessSpecInput into the wire HarnessSpec the
// coordinator hands the runner over StartRun.
func buildHarnessSpec(in HarnessSpecInput) (*agentcoordpb.HarnessSpec, error) {
	fields := map[string]any{}
	if in.SessionHarp != "" {
		fields[harnessConfigKeySessionHarp] = in.SessionHarp
	}
	if len(in.Env) > 0 {
		envObj := make(map[string]any, len(in.Env))
		for k, v := range in.Env {
			envObj[k] = v
		}
		fields[harnessConfigKeyEnv] = envObj
	}
	if len(in.MCPServers) > 0 {
		servers := make([]any, 0, len(in.MCPServers))
		for _, s := range in.MCPServers {
			args := make([]any, 0, len(s.Args))
			for _, a := range s.Args {
				args = append(args, a)
			}
			envObj := make(map[string]any, len(s.Env))
			for k, v := range s.Env {
				envObj[k] = v
			}
			servers = append(servers, map[string]any{
				"name":    s.Name,
				"command": s.Command,
				"args":    args,
				"env":     envObj,
			})
		}
		fields[harnessConfigKeyMCPServers] = servers
	}
	var cfg *structpb.Struct
	if len(fields) > 0 {
		var err error
		cfg, err = structpb.NewStruct(fields)
		if err != nil {
			return nil, fmt.Errorf("coord: build HarnessSpec.config: %w", err)
		}
	}
	return &agentcoordpb.HarnessSpec{
		Harness:         in.Harness,
		Model:           in.Model,
		Workspace:       in.Workspace,
		Config:          cfg,
		ResumeSessionId: in.ResumeSessionID,
		PermissionMode:  in.Permission.String(),
	}, nil
}

// DecodedHarnessSpec is the runner-side projection of a StartRun HarnessSpec:
// a ready-to-use agent.ChatRequest (minus the initial prompt, which rides
// StartRun.input separately) plus the fields config carries by convention
// that ChatRequest has no field for.
type DecodedHarnessSpec struct {
	Chat        agent.ChatRequest
	SessionHarp string
}

// decodeHarnessSpec is buildHarnessSpec's inverse: the runner's translation
// of a received StartRun.harness into the agent.ChatRequest its in-process
// backend.Chat call needs. An absent/empty permission_mode or an unparseable
// one is refused rather than silently defaulting — D3 (children never
// prompt) depends on the runner honoring EXACTLY what the coordinator
// declared, never a guessed fallback.
func decodeHarnessSpec(spec *agentcoordpb.HarnessSpec) (DecodedHarnessSpec, error) {
	var out DecodedHarnessSpec
	if spec == nil {
		return out, fmt.Errorf("coord: StartRun.harness is required")
	}
	perm, ok := agent.ParsePermissionMode(spec.GetPermissionMode())
	if !ok {
		return out, fmt.Errorf("coord: StartRun.harness.permission_mode %q does not parse (D3: a delegated child must declare a headless-safe mode)", spec.GetPermissionMode())
	}
	if !perm.SafeHeadless() {
		return out, fmt.Errorf("coord: StartRun.harness.permission_mode %q is not headless-safe (D3: a delegated child has no channel to surface a prompt)", spec.GetPermissionMode())
	}

	cfg := spec.GetConfig().GetFields()
	if v, ok := cfg[harnessConfigKeySessionHarp]; ok {
		out.SessionHarp = v.GetStringValue()
	}
	var env map[string]string
	if v, ok := cfg[harnessConfigKeyEnv]; ok {
		env = make(map[string]string, len(v.GetStructValue().GetFields()))
		for k, fv := range v.GetStructValue().GetFields() {
			env[k] = fv.GetStringValue()
		}
	}
	var servers []agent.ChatMCPServer
	if v, ok := cfg[harnessConfigKeyMCPServers]; ok {
		for _, item := range v.GetListValue().GetValues() {
			sf := item.GetStructValue().GetFields()
			var args []string
			for _, av := range sf["args"].GetListValue().GetValues() {
				args = append(args, av.GetStringValue())
			}
			var senv map[string]string
			if envFields := sf["env"].GetStructValue().GetFields(); len(envFields) > 0 {
				senv = make(map[string]string, len(envFields))
				for k, fv := range envFields {
					senv[k] = fv.GetStringValue()
				}
			}
			servers = append(servers, agent.ChatMCPServer{
				Name:    sf["name"].GetStringValue(),
				Command: sf["command"].GetStringValue(),
				Args:    args,
				Env:     senv,
			})
		}
	}

	out.Chat = agent.ChatRequest{
		WorkDir:     spec.GetWorkspace(),
		Model:       spec.GetModel(),
		Env:         env,
		Permissions: perm,
		// ForwardPermissions is always true on the migrated StartRun path
		// (Wave C2): the engine's permission requests surface as plane-2
		// ApprovalRequests instead of the driver auto-deciding — the
		// coordinator's escalation ladder (approval.go) is the decider now.
		// permission_mode above is unchanged and still gates what may even
		// run headless (D3's structural floor: bypass|plan only); the
		// ladder is the policy layer riding ON TOP of that floor.
		ForwardPermissions: true,
		MCPServers:         servers,
		ResumeSessionID:    spec.GetResumeSessionId(),
	}
	return out, nil
}
