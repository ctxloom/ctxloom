package wire

import "testing"

// TestCloneMCPServer_DeepCopiesArgsAndEnv pins the one promise CloneMCPServer
// makes: the copy never aliases the source's Args backing array or Env map. A
// plain struct copy is shallow and would share both, so a caller that injects
// an env var into a delivered server (an isolated cell does exactly that)
// would write through into the bundle-resolved set every other engine reads.
func TestCloneMCPServer_DeepCopiesArgsAndEnv(t *testing.T) {
	src := MCPServer{
		Command: "cmd",
		Args:    []string{"mcp", "serve"},
		Env:     map[string]string{"K": "v"},
	}

	clone := CloneMCPServer(src)

	// Guard against a vacuous assertion: the clone must carry the values
	// before mutating it proves anything about aliasing.
	if len(clone.Args) != 2 || clone.Args[1] != "serve" {
		t.Fatalf("clone.Args = %v, want [mcp serve]", clone.Args)
	}
	if clone.Env["K"] != "v" {
		t.Fatalf("clone.Env[K] = %q, want %q", clone.Env["K"], "v")
	}

	clone.Args[0] = "mutated"
	clone.Env["K"] = "mutated"

	if src.Args[0] != "mcp" {
		t.Errorf("writing through the clone's Args changed the source: %v", src.Args)
	}
	if src.Env["K"] != "v" {
		t.Errorf("writing through the clone's Env changed the source: %v", src.Env)
	}
}

// TestCloneMCPServer_NilContainersStayNil keeps the copy from fabricating a
// declaration the source never made: nil Args/Env mean "this server declares
// none", and an empty non-nil map would read as "declares an empty set".
func TestCloneMCPServer_NilContainersStayNil(t *testing.T) {
	clone := CloneMCPServer(MCPServer{Command: "cmd"})
	if clone.Args != nil {
		t.Errorf("Args = %v, want nil", clone.Args)
	}
	if clone.Env != nil {
		t.Errorf("Env = %v, want nil", clone.Env)
	}
}
