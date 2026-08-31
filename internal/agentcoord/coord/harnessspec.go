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
// coordinator hands the runner over StartRun. D3 is checked HERE as well as at
// the decoding end: headless-safety is a property of the spec, so the end that
// composes one refuses an unsafe posture with the coordinator's own context
// rather than shipping a spec the runner is obliged to reject.
func buildHarnessSpec(in HarnessSpecInput) (*agentcoordpb.HarnessSpec, error) {
	if !in.Permission.SafeHeadless() {
		return nil, fmt.Errorf("coord: permission mode %q is not headless-safe (D3: a delegated run has no channel to surface a prompt)", in.Permission)
	}
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
		fields[harnessConfigKeyMCPServers] = encodeMCPServers(in.MCPServers)
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

// encodeMCPServers writes the managed MCP set in the mcp_servers convention —
// decodeMCPServers' inverse, kept adjacent to it so the shape cannot drift.
func encodeMCPServers(in []agent.ChatMCPServer) []any {
	servers := make([]any, 0, len(in))
	for _, s := range in {
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
	return servers
}

// DecodedHarnessSpec is the runner-side projection of a StartRun HarnessSpec:
// a ready-to-use agent.ChatRequest (minus the initial prompt, which rides
// StartRun.input separately) plus the fields config carries by convention
// that ChatRequest has no field for.
type DecodedHarnessSpec struct {
	Chat        agent.ChatRequest
	SessionHarp string
}

// decodeMCPServers reads the mcp_servers config convention into the typed set.
// An entry that is not a usable server — not an object, or missing the name or
// the command — is REFUSED: coercing it into an empty ChatMCPServer hands the
// engine a server with no identity and no executable, which fails later, far
// from the malformed spec that caused it. An absent key is not an error (no
// managed servers is a valid child).
func decodeMCPServers(v *structpb.Value) ([]agent.ChatMCPServer, error) {
	if v == nil {
		return nil, nil
	}
	var servers []agent.ChatMCPServer
	for i, item := range v.GetListValue().GetValues() {
		sf := item.GetStructValue().GetFields()
		name := sf["name"].GetStringValue()
		command := sf["command"].GetStringValue()
		if name == "" || command == "" {
			return nil, fmt.Errorf("coord: StartRun.harness.config %s[%d] needs both a name and a command (got name %q, command %q)",
				harnessConfigKeyMCPServers, i, name, command)
		}
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
		servers = append(servers, agent.ChatMCPServer{Name: name, Command: command, Args: args, Env: senv})
	}
	return servers, nil
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
	servers, err := decodeMCPServers(cfg[harnessConfigKeyMCPServers])
	if err != nil {
		return out, err
	}

	out.Chat = agent.ChatRequest{
		WorkDir:     spec.GetWorkspace(),
		Model:       spec.GetModel(),
		Env:         env,
		Permissions: perm,
		// ForwardPermissions is false: ctxloom does not broker a second
		// approval UI. The engine's permission requests are decided by the
		// driver against this run's resolved permission_mode, which is the
		// structural floor that gates what may run headless at all
		// (bypass|plan). A human who wants to intervene attaches to the
		// agent's own tmux window and answers the engine's NATIVE prompt.
		ForwardPermissions: false,
		MCPServers:         servers,
		ResumeSessionID:    spec.GetResumeSessionId(),
	}
	return out, nil
}
