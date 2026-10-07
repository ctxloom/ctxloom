// Package discover finds live coordinator endpoints on the host by scanning
// ~/.ctxloom/coord/*/*/endpoint.json — one per coordinator ROOT, of which a
// project holds one per independent session tree — the D1 consumer discovery
// mechanism for a process with no coordinator of its own (e.g. `ctxloom
// session transcript watch`, a separate CLI invocation from whatever process
// hosts a coordinator for a session in some project).
//
// Deliberately a LEAF package (it imports only internal/core/paths and the
// toolbox), so both
// halves of the endpoint.json contract compile against ONE declaration: the
// file's LAYOUT lives here (DirName, FileName, State, MCPPath, LoopbackURL),
// and the writer (internal/adapters/coordgrpc's Serve) and the readers
// (internal/adapters/operations' live feed and doctor) all import it, instead
// of two copies that must be kept in step by hand.
package discover

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

const (
	// DirName is the per-user directory holding coordinator state, one
	// directory per root under its project: ~/.ctxloom/<DirName>/<project-key>/
	// <root-harp>/. Re-exports
	// paths.CoordDirName: internal/core/paths is the single declarative source of
	// truth for path SEGMENTS (docs/architecture/core/paths.md), while this
	// package stays the LAYOUT owner (see the package doc above) that coord,
	// the writer, imports.
	DirName = paths.CoordDirName
	// FileName is the discovery file inside a root's state dir. 0600 and
	// host-local: it carries the consumer credential. Re-exports
	// paths.CoordEndpointFileName.
	FileName = paths.CoordEndpointFileName
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
// container listener never opened or an unminted credential absent rather than zero.
type State struct {
	LoopbackPort int `json:"loopback_port,omitempty"`
	// ListenAddrs are the addresses a container cell had it listen on beyond
	// loopback, all on LoopbackPort.
	ListenAddrs  []string `json:"listen_addrs,omitempty"`
	ConsumerCred string   `json:"consumer_cred,omitempty"`
	// ProjectDir is the project the coordinator serves. The state dir is
	// keyed by the project's id, not its path, so this is how a viewer that
	// has not asked the coordinator anything can still say which project it
	// belongs to.
	ProjectDir string `json:"project_dir,omitempty"`
}

// LoopbackURL is the host-local URL for a coordinator bound to port on
// loopback — the value coord advertises and the value List hands back, from one
// format string.
func LoopbackURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, MCPPath)
}

// Endpoint is one root's coordinator: the URL to dial (gRPC over h2c), the
// read-only D1 consumer credential to present as a bearer token, and the
// project it serves.
type Endpoint struct {
	URL        string
	Cred       string
	ProjectDir string
}

// redactedCred stands in for Cred wherever an Endpoint is rendered
// generically. It is a fixed marker, not a length or prefix: the credential
// is a bearer token, and any fragment of one is a fragment too many.
const redactedCred = "<redacted>"

// String renders the endpoint with its credential withheld. Value receiver on
// purpose: that puts it in the method set of both Endpoint and *Endpoint, so
// fmt's %v, %+v and %s redact whichever form a caller happens to hold. The
// URL stays legible because it is the one thing a log line about an endpoint
// is for.
func (e Endpoint) String() string {
	return fmt.Sprintf("{URL:%s Cred:%s}", e.URL, redactedCred)
}

// GoString covers %#v, the one fmt verb that bypasses Stringer and would
// otherwise print the struct literal, credential included.
func (e Endpoint) GoString() string {
	return fmt.Sprintf("discover.Endpoint{URL:%q, Cred:%q}", e.URL, redactedCred)
}

// LogValue is the slog counterpart of String: a structured record keeps the
// URL queryable while the credential never reaches a handler.
func (e Endpoint) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("url", e.URL),
		slog.String("cred", redactedCred),
	)
}

// List returns every LIVE coordinator root's endpoint this host user can
// reach, most-recently-active first (endpoint.json mtime) — the same
// recency policy the retired agentbus socket scan used. A coordinator with
// no minted consumer credential yet (Serve() never ran, or a stale pre-D1
// state dir) is skipped SILENTLY, not erred: that is the common, expected
// case, and the caller simply tries the next candidate. So, as silently, is a
// root whose owner lock (paths.CoordOwnerLockFileName) nobody holds: the
// coordinator that wrote it has exited — endpoint.json is kept for the next
// one to re-bind — and its port and credential died with it. The lock is
// the kernel's answer, so a coordinator killed outright is caught the same
// as one that closed cleanly.
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
	coordDir, err := paths.HomeCoordDir()
	if err != nil {
		return nil, []error{fmt.Errorf("discover: resolve coordinator state root: %w", err)}
	}
	matches, err := filepath.Glob(filepath.Join(coordDir, "*", "*", FileName))
	if err != nil {
		return nil, []error{fmt.Errorf("discover: glob coordinator endpoint files: %w", err)}
	}
	// Stat each candidate exactly ONCE into a snapshot before sorting, then
	// sort the snapshot. Calling os.Stat inside the comparator (the previous
	// shape) was both O(n log n) syscalls AND — the real defect — an
	// INCONSISTENT comparator: discovery exists for the case where ANOTHER
	// live process owns a coordinator, and that coordinator rewrites its
	// endpoint.json on Serve(), so a file's mtime can change mid-sort. A
	// comparator whose keys move underfoot violates sort.Slice's strict-weak-
	// ordering contract and yields an arbitrary order. Snapshotting fixes the
	// keys; a path tiebreak makes the order fully reproducible even when two
	// coordinators share an mtime (1-second filesystem granularity makes that
	// the exact case "most-recently-active first" matters most — several
	// coordinators started together).
	type candidate struct {
		path string
		mt   time.Time
	}
	snap := make([]candidate, len(matches))
	for i, m := range matches {
		snap[i] = candidate{path: m, mt: mtime(m)}
	}
	sort.Slice(snap, func(i, j int) bool {
		if !snap[i].mt.Equal(snap[j].mt) {
			return snap[i].mt.After(snap[j].mt) // most-recently-active first
		}
		return snap[i].path < snap[j].path // stable, reproducible tiebreak
	})
	for _, s := range snap {
		ep, ok, err := readEndpoint(s.path)
		if err != nil {
			skipped = append(skipped, err)
		}
		if ok {
			endpoints = append(endpoints, ep)
		}
	}
	return endpoints, skipped
}

// readEndpoint reads one candidate endpoint.json. ok is false for every
// candidate that is not a live endpoint; err is set only for the ones List
// reports (an unreadable or undecodable file, a lock that cannot be probed),
// never for the two ordinary, silent cases.
func readEndpoint(m string) (Endpoint, bool, error) {
	raw, err := os.ReadFile(m)
	if err != nil {
		return Endpoint{}, false, fmt.Errorf("discover: read %s: %w", m, err)
	}
	var ep State
	if err := json.Unmarshal(raw, &ep); err != nil {
		return Endpoint{}, false, fmt.Errorf("discover: decode %s: %w", m, err)
	}
	if ep.LoopbackPort == 0 || ep.ConsumerCred == "" {
		// The documented, common, NON-error case: a coordinator whose
		// Serve() has not minted a consumer credential yet, or a stale
		// pre-D1 state dir. Deliberately not reported — it is not
		// distinguishable from "healthy, just early" and reporting it would
		// make the common case noisy.
		return Endpoint{}, false, nil
	}
	live, err := safefs.New().Locks.Held(filepath.Join(filepath.Dir(m), paths.CoordOwnerLockFileName))
	if err != nil {
		return Endpoint{}, false, fmt.Errorf("discover: %s: %w", m, err)
	}
	if !live {
		// The writer is gone: endpoint.json outlives its coordinator on
		// purpose (a relaunch re-binds its ports), but its port and
		// credential died with it. Silent, like the not-yet-minted case:
		// every coordinator that ever exited leaves one.
		return Endpoint{}, false, nil
	}
	return Endpoint{URL: LoopbackURL(ep.LoopbackPort), Cred: ep.ConsumerCred, ProjectDir: ep.ProjectDir}, true, nil
}

// mtime reads a path's modification time (zero on error, which sorts an
// unreadable entry last — the right fail direction). It is a package var, not a
// plain func, so a test can observe the load-bearing property this unit's sort
// depends on: List must snapshot each candidate's mtime EXACTLY ONCE before
// sorting, never re-stat inside the comparator (which was both O(n log n)
// syscalls and an inconsistent comparator when a live coordinator rewrote its
// endpoint.json mid-sort).
var mtime = func(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}
