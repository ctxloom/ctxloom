// Package acceptance: P2's fixture MCP server, and the verdict that reads it.
//
// UNTAGGED ON PURPOSE, like probe_assert.go and capability_probe_registry.go
// beside it. P2's cells are all @live — they need a real engine, a real
// subscription and a paid turn — but the two things the cell's honesty actually
// rests on are not live at all: the fixture SERVER (does it speak enough MCP for
// a real client to complete a tool round trip?) and the VERDICT (does it refuse
// every way a cell could look green without the round trip happening?). Both are
// hermetic, so both live where plain `go test ./tests/acceptance/` runs them.
//
// WHAT P2 IS FOR. Capability row 8 — MCP registration plus a tool round trip —
// is the one capability every engine CLAIMS and almost nothing proves. j002300
// proved a real round trip per engine, but only through ctxloom's OWN forwarder
// server, only on host/none: an arbitrary, bundle- or config-declared MCP server
// reaching a live engine through that engine's native registration surface, and
// its tool being called, has never been demonstrated anywhere. That is what P2
// buys.
//
// THE CHANNEL, STATED (channelMCPToolResult). The minted harp exists ONLY as the
// return value of this server's get_nonce tool. It is not in the composed
// context, not in the prompt, not in the environment, not in the config, not in
// any file the agent's workspace contains. An engine can produce it only by
// connecting to the server ctxloom registered on its behalf and calling the
// tool. That is the whole probe: the nonce IS the round trip, in one string.
//
// THE RESIDUAL, AND WHAT CLOSES IT. The harp sits in a file beside the server
// binary, and that directory's path is named in the project's config.yaml, which
// an agent with shell and file tools could read. So the echo alone is NOT proof:
// an engine that catted the nonce file would produce a green-looking answer
// without ever speaking MCP, and a probe that accepted it would be measuring a
// filesystem, not a protocol. Two things close that hole and neither is
// optional:
//
//	THE SERVER APPENDS A CALL RECORD for every tools/call it serves, and the
//	verdict requires one. A run that never called the tool fails on the call log
//	no matter what its stdout says.
//
// THERE USED TO BE A SECOND ONE — the fixture sited outside the cell's workspace
// so nothing in the agent's tree held the value — and it was retired
// deliberately, not lost. It was defence in depth, never the mechanism: it stops
// an agent stumbling onto the path and does nothing against one that goes
// looking, which any engine with shell tools can. Measured: the codex P3 row of
// 2026-08-13 grepped the harp out of the fixture's own script, answered
// correctly, and RED anyway on the effect. Retiring it is what buys the
// container axis — the workspace is the thing bind-mounted into a container
// cell, so a fixture outside it is unreachable there by construction.
//
// The call record is the structural one. It is what makes the probe non-tautological: the
// assertion demands the TOOL PATH, not merely the value, so planting the harp in
// composed context — the classic false green, and the mutation this file's tests
// perform deliberately — still reds the cell. The call log deliberately records
// the tool NAME and never the returned value, so the log itself can never become
// a second channel to the nonce.
//
// THAT CLAIM WAS MUTATION-TESTED, hermetically and live. Live: the
// harp was planted in composed context, the MCP registration was removed so the
// server could not start, and the workspace leak guard was disabled so the
// assertion would actually be reached. The claude-code cell RED, on the tool
// path — MCP-DELIVERY, "the fixture MCP server NEVER STARTED" — which is the
// result the probe's whole value depends on.
//
// One honest limit on what that measured. The model did not take the bait: it
// answered {"nonce":"unable-to-locate-tool-schema"} rather than echoing the
// value sitting in its context, so the live run proved the PATH reds and not
// that a willing model would be caught. That is partly the prompt's own doing —
// it tells the engine the nonce is nowhere in its context, which discourages
// exactly the substitution the mutation was trying to provoke. The rigorous
// version of the check is therefore the hermetic one, which bypasses model
// behaviour entirely: TestMCPProbeAssert_AcceptsOnlyTheRoundTrip feeds the
// verdict a byte-identical CORRECT answer with no tool call and requires a red.
// If that test is ever deleted or loosened, this probe is a string comparison.
//
// WHY A GO BINARY. The design asks for "a self-contained stdio JSON-RPC
// responder, no deps", and the binary is the only form that holds ON EVERY AXIS:
// a container cell runs the server INSIDE the container as a child of the
// engine, where the agent image ships no interpreter. A scripted fixture would
// make this probe measure interpreter availability instead of the axis under
// test. Go is already the toolchain, so it costs no new dependency — see
// probeMCPBuildServer, and cmd/mockengine for the same shape.
package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// probeMCPServerName is the MCP server name ctxloom registers on the engine's
// behalf, and probeMCPToolName the one tool it serves. Both are lowercase and
// underscore-free/underscore-simple on purpose: engines NAMESPACE MCP tools
// differently (claude presents this one as mcp__probenonce__get_nonce), and a
// name carrying a hyphen or a dot survives that mangling in fewer of them.
const (
	probeMCPServerName = "probenonce"
	probeMCPToolName   = "get_nonce"
)

// probeMCPExpectedKey is the single key the engine's whole stdout must parse to
// an object of. Named once so the prompt and the assertion cannot drift apart.
const probeMCPExpectedKey = "nonce"

// probeMCPBinaryName / probeMCPNonceName / probeMCPLogName are the fixture's
// three files, all SIBLINGS in one directory rather than paths the server
// invents, because the verdict must read exactly the log the server wrote — a
// server that chose its own path would leave the assertion guessing, and an
// assertion that guesses at its evidence is how a probe comes to pass by
// reading nothing.
//
// THE NONCE IS A FILE, NOT AN ARGUMENT. The server's command and args are
// registered in the project's config.yaml, which lives INSIDE the workspace and
// is readable by the agent under test. A nonce on argv would therefore be
// reachable without ever calling the tool, and the cell would pass while
// proving nothing — the exact false green this probe exists to prevent. The
// fixture directory is sited outside the workspace by the caller.
const (
	probeMCPBinaryName = "nonce-mcp-server"
	probeMCPNonceName  = "nonce.txt"
	probeMCPLogName    = "nonce_mcp_calls.jsonl"
)

// The fixture server is cmd/probe-mcp-server: a line-delimited JSON-RPC 2.0
// responder over stdio, which is what MCP's stdio transport is.
//
// IT IS A BINARY, NOT A SCRIPT, and that is load-bearing rather than stylistic.
// A container cell runs the server INSIDE the container as a child of the
// engine, and the agent image ships no interpreter beyond a shell — so a
// scripted fixture makes this probe measure interpreter availability instead of
// the axis under test. cmd/mockengine is the same shape and the same precedent:
// a test-support executable spawned as a subprocess.
//
// It answers more than the round trip strictly needs — ping, resources/list,
// prompts/list, and a proper -32601 for anything else — because several vendor
// clients drive it and a server that hangs up on a handshake method it did not
// expect would red a cell for the FIXTURE's incompleteness while reporting an
// MCP-delivery failure. The one behaviour that is not politeness: a message
// with no "id" is a NOTIFICATION and is never answered, because answering one
// is a protocol violation that some clients treat as fatal.

// probeMCPFixture is a written-out fixture server: where its script is, where
// its call log will be, and which nonce it will hand back.
type probeMCPFixture struct {
	Dir       string
	Binary    string
	NoncePath string
	CallLog   string
	Nonce     string
}

// probeMCPWriteFixture materializes the server for one cell.
//
// dir is sited INSIDE the cell's workspace by the caller, which is what lets a
// container cell reach it — see this file's header for why the old
// outside-the-workspace requirement was defence in depth rather than the
// mechanism, and probeMCPWorkspaceRel for the paths the config registers.
//
// An EMPTY nonce is refused rather than written: the harp is the entire content
// of the round trip, and a server serving "" would satisfy strings.Contains
// against any output at all.
func probeMCPWriteFixture(dir, nonce string) (probeMCPFixture, error) {
	if nonce == "" {
		return probeMCPFixture{}, fmt.Errorf("capability probe P2: refusing to write a fixture MCP server with an empty nonce — every string contains the empty string, so the round-trip assertion would pass without a round trip")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return probeMCPFixture{}, fmt.Errorf("capability probe P2: creating the fixture MCP server directory: %w", err)
	}
	f := probeMCPFixture{
		Dir:       dir,
		Binary:    filepath.Join(dir, probeMCPBinaryName),
		NoncePath: filepath.Join(dir, probeMCPNonceName),
		CallLog:   filepath.Join(dir, probeMCPLogName),
		Nonce:     nonce,
	}
	// The nonce goes to a FILE beside the binary, never to argv: the server's
	// command and args are registered in config.yaml, inside the workspace the
	// agent can read.
	if err := os.WriteFile(f.NoncePath, []byte(nonce+"\n"), 0o600); err != nil {
		return probeMCPFixture{}, fmt.Errorf("capability probe P2: writing the fixture nonce: %w", err)
	}
	if err := probeMCPBuildServer(f.Binary); err != nil {
		return probeMCPFixture{}, err
	}
	// The log is NOT pre-created. Its absence and its emptiness must be
	// distinguishable from each other in the verdict: "the server never ran"
	// and "the server ran and the tool was never called" are different findings
	// about different subsystems, and a fixture that touched the file first
	// would erase the first one.
	return f, nil
}

// probeMCPWorkspaceRel renders the fixture's binary and directory as paths
// RELATIVE to the cell's workspace root, which is the form the registration must
// carry.
//
// WHY RELATIVE, AND WHY IT IS THE WHOLE REASON BOTH CONTAINER AXES WORK. The
// registration is committed into the cell's tree, and the tree is then run from
// two different absolute locations: a /none cell runs the project directory
// itself (bind-mounted into a container at the SAME absolute path, per
// isolation.buildRunSpec's identity mapper), while a /worktree cell runs a
// per-agent checkout that ctxloom creates at a path nothing here can know in
// advance. An absolute path baked in at fixture-write time is therefore correct
// for at most one of them, and WRONG SILENTLY for the other: the server simply
// fails to spawn, and the cell reds as an MCP-delivery failure that is really a
// harness path bug. A workspace-relative path resolves against the engine's cwd,
// which IS the workspace on every axis, so one fixture shape serves all of them.
//
// The absolute forms on probeMCPFixture stay as they are — the HOST-side verdict
// reads the call log through them, and for a /worktree cell the reader must
// resolve the checkout first (see mcpProbeCallLogPath).
//
// An error rather than a best-effort relative path: filepath.Rel escaping the
// workspace (a "../" result) means the caller sited the fixture outside it, and
// the registration would then name a path no container cell can reach. That is
// the exact defect this function exists to make impossible, so it must not be
// representable in the returned value.
func probeMCPWorkspaceRel(workspaceDir string, f probeMCPFixture) (relBinary, relDir string, err error) {
	for _, p := range []struct {
		abs string
		out *string
	}{{f.Binary, &relBinary}, {f.Dir, &relDir}} {
		rel, rerr := filepath.Rel(workspaceDir, p.abs)
		if rerr != nil {
			return "", "", fmt.Errorf("capability probe P2: relativizing the fixture path %s against the cell workspace %s: %w", p.abs, workspaceDir, rerr)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", "", fmt.Errorf("capability probe P2: the fixture path %s is OUTSIDE the cell workspace %s (relative form %q) — the registration would name a path a container cell cannot reach, because the workspace is the only thing bind-mounted in", p.abs, workspaceDir, rel)
		}
		*p.out = rel
	}
	return relBinary, relDir, nil
}

// probeMCPBundleBlock is the registration: an `mcp:` block in the fixture's own
// BUNDLE, which is the only surface that still exists.
//
// IT WAS A TOP-LEVEL `mcp.servers` KEY IN config.yaml, and this probe's design
// chose that deliberately over a bundle — a bundle's MCP servers pass the
// executable trust gate (bundles.Decide), and a withheld server would red as an
// MCP-delivery failure that is really a trust decision, which is the exact
// channel confusion this ladder refuses.
//
// THAT SURFACE WAS DELETED on 2026-08-19 by c5228d46 ("MCP servers come from
// bundles only; ctxloom's own ships as a builtin"): there is no `mcp:` key in
// config.yaml and none on a profile. An MCP server is declared in a bundle and
// COMPOSING that bundle is what registers it. So the choice above is no longer
// available, and the trust gate is now part of what any real user traverses —
// which makes routing through it correct rather than merely tolerable.
//
// A red that is really a trust decision is still the hazard the old comment
// named. It is now DISTINGUISHABLE rather than avoided: a withheld server never
// starts, so the call log is ABSENT rather than empty, and probeMCPCallLog
// reports those as different findings.
func probeMCPBundleBlock(binaryPath, fixtureDir string) string {
	var b strings.Builder
	b.WriteString("mcp:\n  " + probeMCPServerName + ":\n")
	fmt.Fprintf(&b, "    command: %q\n", binaryPath)
	b.WriteString("    args:\n")
	fmt.Fprintf(&b, "      - %q\n", fixtureDir)
	return b.String()
}

// --- reading the evidence ------------------------------------------------------

// probeMCPCall is one line of the server's call log.
type probeMCPCall struct {
	Event  string         `json:"event"`
	PID    int            `json:"pid"`
	Detail map[string]any `json:"detail"`
}

// probeMCPCallLog reads the server's evidence. It reports the parsed records and
// whether the log FILE EXISTED at all, because those are two different findings:
// no file means no server process ever started (registration never reached the
// engine, or the engine never launched it), while an empty or tool-call-free log
// means the server ran and the engine never called the tool. Collapsing them
// would merge "ctxloom did not register it" with "the model did not use it".
func probeMCPCallLog(path string) (calls []probeMCPCall, existed bool, err error) {
	b, readErr := os.ReadFile(path)
	if os.IsNotExist(readErr) {
		return nil, false, nil
	}
	if readErr != nil {
		return nil, false, fmt.Errorf("capability probe P2: reading the fixture MCP server's call log %s: %w", path, readErr)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var c probeMCPCall
		if uerr := json.Unmarshal([]byte(line), &c); uerr != nil {
			// A malformed line is a fixture fault, not an engine finding, and
			// must not be silently dropped into "the tool was never called".
			return nil, true, fmt.Errorf("capability probe P2: the fixture MCP server's call log holds an unparseable record (%v): %s", uerr, line)
		}
		calls = append(calls, c)
	}
	return calls, true, nil
}

// probeMCPToolCallCount counts the records that are get_nonce invocations.
func probeMCPToolCallCount(calls []probeMCPCall) int {
	n := 0
	for _, c := range calls {
		if c.Event == "tool_call" {
			n++
		}
	}
	return n
}

// --- the verdict ---------------------------------------------------------------

// mcpProbeRun is one P2 cell's captured outcome: the run, plus the fixture
// server's own evidence.
type mcpProbeRun struct {
	Cell     probeCellID
	Nonce    string
	Run      probeRun
	CallLog  []probeMCPCall
	LogFound bool
}

// mcpProbeAssert is P2's whole verdict, composed from the shared vocabulary in
// probe_assert.go so an MCP format-red and a P0 format-red mean the same thing
// and read the same way.
//
// THE ORDER IS THE ATTRIBUTION, and P2 deliberately does NOT use the floor's.
// The floor asks: did it run, did it say anything, is it the right FORM, did the
// channel deliver, is it the right object. P2 puts THE TOOL PATH ahead of form,
// and the reason is measured rather than aesthetic.
//
// Run against kiro, the floor's order reported an OUTPUT-FORMAT
// failure — kiro's already-known ANSI-decoration defect, which P0's own kiro
// host/none row exists to carry — and the interesting finding was underneath it,
// visible only because the raw stdout happened to be printed: the model had
// answered "tool not found". A probe about MCP that reds on terminal colour
// codes and never mentions MCP is attributing to the wrong subsystem, and the
// shape a sweep diffs would have said "format" for a cell whose actual state was
// "the tool was never reachable".
//
// So: whether the tool was CALLED is a fact about the fixture server's own
// records, entirely independent of how the engine's stdout is shaped, and it is
// this probe's whole subject. It is therefore asked first. That reordering can
// never mislabel the other direction — an engine that DID call the tool and then
// fenced its answer still gets its OUTPUT-FORMAT failure, because the tool-path
// rung passes and the form check runs immediately after.
//
// The rung is also what stops the probe from being a tautology:
//
//   - a run whose output carries the harp but whose server was never called is
//     an engine that READ THE VALUE off a disk somewhere, and it reds here;
//   - a run whose server was called but whose output carries something else is a
//     model that did the round trip and then failed to report it, which is a
//     different bug and gets a different shape.
//
// The two are never merged, and neither is ever satisfied by the other.
func mcpProbeAssert(r mcpProbeRun) error {
	v := probeVerdict{Family: "probe-p2-mcp", Cell: r.Cell, Channel: channelMCPToolResult}

	trimmed, err := v.ran(r.Run)
	if err != nil {
		return err
	}
	if err := mcpProbeToolWasCalled(v, r, trimmed); err != nil {
		return err
	}
	got, err := v.jsonObject(trimmed)
	if err != nil {
		return err
	}
	if err := v.carriesNonce(trimmed, r.Nonce); err != nil {
		return err
	}
	return v.exactObject(got, trimmed, probeMCPExpectedKey, r.Nonce)
}

// mcpProbeToolWasCalled is the rung that makes P2 a round-trip probe rather than
// a string-matching one. It reads the fixture server's OWN record of having
// served the call, and it distinguishes the two ways that record can be missing
// because they blame different subsystems:
//
//   - NO LOG FILE: no server process ever started. Either ctxloom never wrote
//     the registration into the engine's native MCP surface, or the engine never
//     launched what it was given. A ctxloom-or-engine wiring failure.
//   - A LOG WITH NO get_nonce RECORD: the server started (so registration DID
//     reach the engine and the engine DID spawn it) and the tool was never
//     invoked. A model-behaviour finding, not a wiring one.
//
// The stdout is carried into the message in both cases, because the most
// interesting version of this red is an engine that produced the right-looking
// answer with no tool call behind it — and a message that omitted the output
// would hide exactly that.
func mcpProbeToolWasCalled(v probeVerdict, r mcpProbeRun, trimmed string) error {
	if !r.LogFound {
		return v.fail(v.Channel.Shape,
			fmt.Sprintf("%s — the fixture MCP server NEVER STARTED: it wrote no call log at all. ctxloom either did not register %q into this engine's native MCP surface, or the engine never launched the server it was given. Nothing about the model's answer bears on this.",
				v.Channel.Shape, probeMCPServerName),
			fmt.Sprintf("\nstdout:\n%s\nstderr:\n%s", trimmed, r.Run.Stderr))
	}
	if n := probeMCPToolCallCount(r.CallLog); n == 0 {
		return v.fail(v.Channel.Shape,
			fmt.Sprintf("%s — the fixture MCP server started (it logged %d record(s)) but %q was NEVER CALLED. Registration reached the engine; the round trip did not happen. Any nonce in the output below therefore came from somewhere other than the tool, which is precisely what this probe refuses to accept as a pass.",
				v.Channel.Shape, len(r.CallLog), probeMCPToolName),
			fmt.Sprintf("\nstdout:\n%s\ncall log events: %s", trimmed, probeMCPCallLogSummary(r.CallLog)))
	}
	return nil
}

// probeMCPCallLogSummary renders the log's events for a failure message, in
// order and without the details — enough to see how far the handshake got.
func probeMCPCallLogSummary(calls []probeMCPCall) string {
	if len(calls) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(calls))
	for _, c := range calls {
		if m, ok := c.Detail["method"].(string); ok && m != "" {
			parts = append(parts, c.Event+"("+m+")")
			continue
		}
		parts = append(parts, c.Event)
	}
	return strings.Join(parts, " ")
}

// probeMCPBuildServer builds cmd/probe-mcp-server to dest.
//
// A BUILD FAILURE IS A HARNESS FAULT AND MUST BE LOUD. The interpreter check
// this replaced could SKIP, because a missing python3 said nothing about the
// engine. A fixture that will not compile says nothing about the engine either,
// but it is our bug rather than an environmental one, so it errors instead of
// skipping — a silently skipped cell is the false green this ladder exists to
// prevent.
//
// CGO_ENABLED=0 is deliberate: a container cell runs this binary INSIDE the
// agent image, and a cgo-linked binary would depend on that image's libc. A
// static binary runs anywhere the GOOS/GOARCH match.
//
// KNOWN GAP, stated rather than hidden: the build targets the HOST platform. On
// a linux host driving a linux container — every cell we run today — that is
// the same platform. A macOS or Windows host driving a linux container would
// need GOOS/GOARCH overridden here, and the cell would fail with an exec-format
// error that names this function.
func probeMCPBuildServer(dest string) error {
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", dest, "github.com/ctxloom/ctxloom/cmd/probe-mcp-server")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("capability probe P2: building the fixture MCP server (%s): %w\n%s", dest, err, out)
	}
	return nil
}
