// Package discover finds live coordinator endpoints on the host by scanning
// ~/.ctxloom/coord/*/endpoint.json — the D1 consumer discovery mechanism for
// a process with no coordinator of its own (e.g. `ctxloom session transcript watch`, a
// separate CLI invocation from whatever process hosts the coordinator for a
// given project's session).
//
// Deliberately a LEAF package: internal/agentcoord/coord imports
// internal/operations (children.go's AgentChatLaunch/JoinLeadBlocks), so
// internal/operations — this discovery mechanism's only production consumer
// (sessionfeed.go) — cannot import coord without a cycle.
//
// That constraint fixes the direction of the endpoint.json contract: the file's
// LAYOUT lives here (DirName, FileName, State, MCPPath, LoopbackURL) and coord,
// the writer, imports it. The reader cannot reach the writer, so the writer
// reaches the reader; either way both halves compile against one declaration
// instead of two that must be kept in step by hand.
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

const (
	// DirName is the per-user directory holding one subdirectory of coordinator
	// state per project: ~/.ctxloom/<DirName>/<project-key>/.
	DirName = "coord"
	// FileName is the discovery file inside a project's state dir. 0600 and
	// host-local: it carries the consumer credential.
	FileName = "endpoint.json"
	// MCPPath is retained in the advertised CTXLOOM_COORD_URL shape
	// (http://<host>:<port>/mcp) for continuity — the gRPC server rides the same
	// host:port as the MCP endpoint (one h2c listener, content-type routed), and
	// a runner derives its dial target from the URL's host:port.
	MCPPath = "/mcp"
)

// State is endpoint.json's layout: the ports a coordinator last bound, so a
// relaunched one re-binds the SAME endpoint (adopted container RunnerChannels
// re-Hello against a stable, re-bindable endpoint), plus the read-only D1
// consumer credential an out-of-process viewer needs. The credential is
// re-minted every Serve() and never journaled, so this file IS its only
// persistence.
//
// Written by coord's Serve, read by List below. omitempty throughout keeps an
// unbound wide listener or an unminted credential absent rather than zero.
type State struct {
	LoopbackPort int    `json:"loopback_port,omitempty"`
	WidePort     int    `json:"wide_port,omitempty"`
	ConsumerCred string `json:"consumer_cred,omitempty"`
}

// LoopbackURL is the host-local URL for a coordinator bound to port on
// loopback — the value coord advertises and the value List hands back, from one
// format string.
func LoopbackURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, MCPPath)
}

// Endpoint is one project's coordinator: the URL to dial (gRPC over h2c) and
// the read-only D1 consumer credential to present as a bearer token.
type Endpoint struct {
	URL  string
	Cred string
}

// List returns every project's coordinator endpoint this host user can
// reach, most-recently-active first (endpoint.json mtime) — the same
// recency policy the retired agentbus socket scan used. A coordinator with
// no minted consumer credential yet (Serve() never ran, or a stale pre-D1
// state dir) is skipped SILENTLY, not erred: that is the common, expected
// case, and the caller simply tries the next candidate.
//
// Every OTHER way a candidate fails to become an endpoint (the
// user home dir unresolvable, the glob itself erroring, a candidate file
// present but unreadable, or present but undecodable JSON) used to collapse
// into that exact same silent skip, with no error channel at all — so the
// one production consumer (operations/sessionfeed.go) had no way to tell
// "no endpoint file exists" apart from "one exists but is corrupt or
// permission-denied" and unconditionally asserted the former. skipped now
// reports every one of those non-silent cases (never the documented
// not-yet-minted case, kept quiet on purpose) so the caller CAN tell them
// apart, without ever aborting discovery over one bad candidate.
func List() (endpoints []Endpoint, skipped []error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, []error{fmt.Errorf("discover: resolve user home dir: %w", err)}
	}
	matches, err := filepath.Glob(filepath.Join(home, paths.AppDirName, DirName, "*", FileName))
	if err != nil {
		return nil, []error{fmt.Errorf("discover: glob coordinator endpoint files: %w", err)}
	}
	sort.Slice(matches, func(i, j int) bool { return mtime(matches[i]).After(mtime(matches[j])) })
	for _, m := range matches {
		raw, rerr := os.ReadFile(m)
		if rerr != nil {
			skipped = append(skipped, fmt.Errorf("discover: read %s: %w", m, rerr))
			continue
		}
		var ep State
		if uerr := json.Unmarshal(raw, &ep); uerr != nil {
			skipped = append(skipped, fmt.Errorf("discover: decode %s: %w", m, uerr))
			continue
		}
		if ep.LoopbackPort == 0 || ep.ConsumerCred == "" {
			// The documented, common, NON-error case: a coordinator whose
			// Serve() has not minted a consumer credential yet, or a stale
			// pre-D1 state dir. Deliberately not added to skipped — it is
			// not distinguishable from "healthy, just early" and reporting
			// it would make the common case noisy.
			continue
		}
		endpoints = append(endpoints, Endpoint{
			URL:  LoopbackURL(ep.LoopbackPort),
			Cred: ep.ConsumerCred,
		})
	}
	return endpoints, skipped
}

func mtime(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}
