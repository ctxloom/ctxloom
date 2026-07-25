// Package discover finds live coordinator endpoints on the host by scanning
// ~/.ctxloom/coord/*/endpoint.json — the D1 consumer discovery mechanism for
// a process with no coordinator of its own (e.g. `ctxloom session watch`, a
// separate CLI invocation from whatever process hosts the coordinator for a
// given project's session).
//
// Deliberately a LEAF package: internal/agentcoord/coord imports
// internal/operations (children.go's AgentChatLaunch/JoinLeadBlocks), so
// internal/operations — this discovery mechanism's only production consumer
// (sessionfeed.go) — cannot import coord without a cycle. This package
// therefore re-reads coord's endpoint.json shape as an independently
// defined JSON structure (mirroring coord/httpserver.go's endpointState and
// MCPPath) rather than importing coord's types; the two must be kept in
// sync by hand, documented on both sides.
package discover

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// coordDirName mirrors coord/statedir.go's unexported coordDirName constant.
const coordDirName = "coord"

// mcpPath mirrors coord/httpserver.go's MCPPath constant: the gRPC server
// rides the same host:port as the MCP endpoint (one h2c listener,
// content-type routed).
const mcpPath = "/mcp"

// Endpoint is one project's coordinator: the URL to dial (gRPC over h2c) and
// the read-only D1 consumer credential to present as a bearer token.
type Endpoint struct {
	URL  string
	Cred string
}

// endpointFile mirrors coord/httpserver.go's endpointState JSON shape (only
// the fields a consumer needs — WidePort is container-reachability only,
// irrelevant to a host-local CLI viewer).
type endpointFile struct {
	LoopbackPort int    `json:"loopback_port"`
	ConsumerCred string `json:"consumer_cred"`
}

// List returns every project's coordinator endpoint this host user can
// reach, most-recently-active first (endpoint.json mtime) — the same
// recency policy the retired agentbus socket scan used. A coordinator with
// no minted consumer credential yet (Serve() never ran, or a stale pre-D1
// state dir) is skipped, not erred: the caller tries the next candidate.
func List() []Endpoint {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(home, paths.AppDirName, coordDirName, "*", "endpoint.json"))
	sort.Slice(matches, func(i, j int) bool { return mtime(matches[i]).After(mtime(matches[j])) })
	var out []Endpoint
	for _, m := range matches {
		raw, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var ep endpointFile
		if json.Unmarshal(raw, &ep) != nil || ep.LoopbackPort == 0 || ep.ConsumerCred == "" {
			continue
		}
		out = append(out, Endpoint{
			URL:  fmt.Sprintf("http://127.0.0.1:%d%s", ep.LoopbackPort, mcpPath),
			Cred: ep.ConsumerCred,
		})
	}
	return out
}

func mtime(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}
