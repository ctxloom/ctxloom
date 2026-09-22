package wire

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

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

// TestMCPServer_RemoteRoundTrip pins that a network-hosted server — URL and
// Headers, no Command — survives both encodings this type is written in
// (a bundle's yaml, an engine registry's json) with every field intact, and
// that a stdio entry is untouched by the remote fields' existence. A field
// that marshals but does not come back is the silent-drop shape this project
// produces most often.
func TestMCPServer_RemoteRoundTrip(t *testing.T) {
	remote := MCPServer{
		URL:     "https://mcp.example.com/v1",
		Headers: map[string]string{"Authorization": "Bearer t"},
		Notes:   "n",
	}
	stdio := MCPServer{Command: "cmd", Args: []string{"a"}, Env: map[string]string{"K": "v"}}

	for _, tt := range []struct {
		name      string
		marshal   func(any) ([]byte, error)
		unmarshal func([]byte, any) error
	}{
		{"json", json.Marshal, json.Unmarshal},
		{"yaml", yaml.Marshal, yaml.Unmarshal},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, in := range []MCPServer{remote, stdio} {
				data, err := tt.marshal(in)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				var got MCPServer
				if err := tt.unmarshal(data, &got); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if !reflect.DeepEqual(got, in) {
					t.Errorf("round trip changed the value\n got: %#v\nwant: %#v\nbytes: %s", got, in, data)
				}
			}
		})
	}
}

// TestCloneMCPServer_DeepCopiesHeaders extends the aliasing promise to
// Headers: a remote entry's header map is as mutable as a stdio entry's Env,
// and a shared map would let one delivery's Authorization rewrite leak into
// the bundle-resolved set every other engine reads.
func TestCloneMCPServer_DeepCopiesHeaders(t *testing.T) {
	src := MCPServer{URL: "https://mcp.example.com", Headers: map[string]string{"Authorization": "Bearer t"}}

	clone := CloneMCPServer(src)
	if clone.Headers["Authorization"] != "Bearer t" {
		t.Fatalf("clone.Headers = %v, want the source's headers", clone.Headers)
	}
	clone.Headers["Authorization"] = "mutated"

	if src.Headers["Authorization"] != "Bearer t" {
		t.Errorf("writing through the clone's Headers changed the source: %v", src.Headers)
	}
	if CloneMCPServer(MCPServer{URL: "https://x"}).Headers != nil {
		t.Errorf("nil Headers must stay nil")
	}
}

// TestMCPServer_Validate pins the rule that reconciles "command is required"
// with a command-less remote entry: a server is EXACTLY ONE of stdio (Command)
// or remote (URL). Command is required only on the stdio side; a URL entry
// has no command by definition. The scheme is the transport — it is the only
// place the protocol is named — so anything but http/https is refused.
func TestMCPServer_Validate(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   MCPServer
		want error
	}{
		{"stdio", MCPServer{Command: "cmd"}, nil},
		{"remote https", MCPServer{URL: "https://mcp.example.com/v1"}, nil},
		{"remote http", MCPServer{URL: "http://127.0.0.1:8080/mcp"}, nil},
		{"neither", MCPServer{Args: []string{"a"}}, ErrMCPServerNoTarget},
		{"both", MCPServer{Command: "cmd", URL: "https://x"}, ErrMCPServerTwoTargets},
		{"bad scheme", MCPServer{URL: "unix:///run/mcp.sock"}, ErrMCPServerURLScheme},
		{"no scheme", MCPServer{URL: "mcp.example.com/v1"}, ErrMCPServerURLScheme},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate()
			if !errors.Is(err, tt.want) {
				t.Errorf("Validate() = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestMCPServer_Validate_SessionEndpointDeclaration pins the third target:
// an entry SERVED BY the running session's endpoint declares no command and
// no URL — the bundle contributes nothing executable, and the host supplies
// its own endpoint (URL + bearer) at delivery. It is exclusive with both
// other targets, and the declaration's value is the one the host knows.
func TestMCPServer_Validate_SessionEndpointDeclaration(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   MCPServer
		want error
	}{
		{"declared", MCPServer{ServedBy: ServedBySessionEndpoint}, nil},
		{"with a command", MCPServer{ServedBy: ServedBySessionEndpoint, Command: "cmd"}, ErrMCPServerTwoTargets},
		{"with a url", MCPServer{ServedBy: ServedBySessionEndpoint, URL: "https://x"}, ErrMCPServerTwoTargets},
		{"with args only", MCPServer{ServedBy: ServedBySessionEndpoint, Args: []string{"mcp", "serve"}}, ErrMCPServerTwoTargets},
		{"unknown server", MCPServer{ServedBy: "some-other-thing"}, ErrMCPServerServedBy},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate()
			if !errors.Is(err, tt.want) {
				t.Errorf("Validate() = %v, want %v", err, tt.want)
			}
		})
	}
	if !(MCPServer{ServedBy: ServedBySessionEndpoint}).IsSessionEndpoint() {
		t.Error("a session-endpoint declaration must report IsSessionEndpoint")
	}
	if (MCPServer{Command: "cmd"}).IsSessionEndpoint() || (MCPServer{URL: "https://x"}).IsSessionEndpoint() {
		t.Error("a stdio or remote server is not a session-endpoint declaration")
	}
}

// TestMCPServer_SessionEndpoint_RoundTripsAsServedBy pins the on-disk
// spelling: `served_by: session-endpoint`, nothing else on the entry.
func TestMCPServer_SessionEndpoint_RoundTripsAsServedBy(t *testing.T) {
	var got MCPServer
	if err := yaml.Unmarshal([]byte("served_by: session-endpoint\n"), &got); err != nil {
		t.Fatal(err)
	}
	if got.ServedBy != ServedBySessionEndpoint {
		t.Fatalf("decoded ServedBy = %q, want %q", got.ServedBy, ServedBySessionEndpoint)
	}
	out, err := yaml.Marshal(MCPServer{ServedBy: ServedBySessionEndpoint})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "served_by: session-endpoint\n" {
		t.Errorf("encoded = %q, want the bare declaration", out)
	}
	j, _ := json.Marshal(MCPServer{ServedBy: ServedBySessionEndpoint})
	if string(j) != `{"served_by":"session-endpoint"}` {
		t.Errorf("json = %s, want the bare declaration", j)
	}
}
