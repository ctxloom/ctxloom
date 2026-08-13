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
// THE RESIDUAL, AND WHAT CLOSES IT. The harp is a literal inside this script, and
// the script's path is named in the project's config.yaml, which an agent with
// shell and file tools could read. So the echo alone is NOT proof: an engine that
// catted the script would produce a green-looking answer without ever speaking
// MCP, and a probe that accepted it would be measuring a filesystem, not a
// protocol. Two things close that hole and neither is optional:
//
//  1. The script lives OUTSIDE the cell's workspace (probeMCPWriteFixture takes
//     a directory the caller must site outside the project), so nothing in the
//     agent's tree contains the value.
//  2. The server APPENDS A CALL RECORD for every tools/call it serves, and the
//     verdict requires one. A run that never called the tool fails on the call
//     log no matter what its stdout says.
//
// (2) is the structural one. It is what makes the probe non-tautological: the
// assertion demands the TOOL PATH, not merely the value, so planting the harp in
// composed context — the classic false green, and the mutation this file's tests
// perform deliberately — still reds the cell. The call log deliberately records
// the tool NAME and never the returned value, so the log itself can never become
// a second channel to the nonce.
//
// WHY PYTHON. The design's own words: "a self-contained stdio JSON-RPC responder,
// no deps". Python 3 is in the base image and on every dev box this suite runs
// on; the script imports only json/os/sys/time. A cell whose interpreter is
// missing SKIPS LOUDLY rather than reporting an MCP failure that is really a
// missing interpreter — a delivery red must mean delivery.
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

// probeMCPInterpreter is the stdio server's interpreter. Named so the skip
// message and the registered command cannot disagree about what was looked for.
const probeMCPInterpreter = "python3"

// probeMCPScriptName / probeMCPLogName are the fixture's two files. The log is a
// SIBLING of the script rather than a temp file the server invents, because the
// verdict must be able to read exactly the log the server wrote — a server that
// chose its own path would leave the assertion guessing, and an assertion that
// guesses at its evidence is how a probe comes to pass by reading nothing.
const (
	probeMCPScriptName = "nonce_mcp_server.py"
	probeMCPLogName    = "nonce_mcp_calls.jsonl"
)

// probeMCPNoncePlaceholder / probeMCPLogPlaceholder are substituted into the
// script source. Substitution is by textual replacement rather than
// fmt.Sprintf because the script is full of Python %-formatting, and a format
// string that also contains the program's own percent signs is a bug waiting
// for the first "%s" nobody escaped.
const (
	probeMCPNoncePlaceholder = "@@CTXLOOM_PROBE_NONCE@@"
	probeMCPLogPlaceholder   = "@@CTXLOOM_PROBE_LOG@@"
)

// probeMCPServerSource is the whole server: a line-delimited JSON-RPC 2.0
// responder over stdio, which is what MCP's stdio transport is.
//
// It answers more than the round trip strictly needs — ping, resources/list,
// prompts/list, and a proper -32601 for anything else — because four different
// vendor clients drive it and a server that hangs up on a handshake method it
// did not expect would red a cell for the FIXTURE's incompleteness while
// reporting an MCP-delivery failure. The one behaviour that is not politeness:
// a message with no "id" is a NOTIFICATION and is never answered, because
// answering one is a protocol violation that some clients treat as fatal.
const probeMCPServerSource = `#!/usr/bin/env python3
"""ctxloom capability-probe P2 fixture: a stdio MCP server serving one tool.

The nonce below is a harp minted by the acceptance fixture for exactly one cell.
It is the ONLY copy that an engine can reach through a protocol, which is the
entire point of the probe: an engine that produces this string called the tool.
"""
import json
import os
import sys
import time

NONCE = "@@CTXLOOM_PROBE_NONCE@@"
CALL_LOG = "@@CTXLOOM_PROBE_LOG@@"
TOOL = "get_nonce"
PROTOCOL_FALLBACK = "2025-06-18"


def record(event, detail):
    """Append one evidence line. O_APPEND + one write() per line keeps records
    whole even if a client starts the server more than once."""
    try:
        line = json.dumps({"ts": time.time(), "pid": os.getpid(),
                           "event": event, "detail": detail}) + "\n"
        fd = os.open(CALL_LOG, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
        try:
            os.write(fd, line.encode("utf-8"))
        finally:
            os.close(fd)
    except Exception:
        # Evidence must never take the server down: a crashed server would be
        # reported as an MCP-delivery failure, which is a different finding.
        pass


def send(msg):
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()


def reply(rid, result):
    send({"jsonrpc": "2.0", "id": rid, "result": result})


def fail(rid, code, message):
    send({"jsonrpc": "2.0", "id": rid, "error": {"code": code, "message": message}})


def handle(req):
    method = req.get("method")
    rid = req.get("id")
    record("request", {"method": method, "has_id": rid is not None})

    # A JSON-RPC NOTIFICATION carries no id and MUST NOT be answered.
    if rid is None:
        return

    if method == "initialize":
        params = req.get("params") or {}
        version = params.get("protocolVersion") or PROTOCOL_FALLBACK
        reply(rid, {
            "protocolVersion": version,
            "capabilities": {"tools": {"listChanged": False}},
            "serverInfo": {"name": "ctxloom-probe-nonce", "version": "1.0.0"},
        })
    elif method == "tools/list":
        reply(rid, {"tools": [{
            "name": TOOL,
            "description": (
                "Returns this session's nonce string. The value exists nowhere "
                "else: it cannot be guessed, recalled, or found in context."
            ),
            "inputSchema": {"type": "object", "properties": {}},
        }]})
    elif method == "tools/call":
        params = req.get("params") or {}
        name = params.get("name")
        # A client may present the tool under its own namespaced name.
        called = name == TOOL or (isinstance(name, str) and name.endswith(TOOL))
        if called:
            # THE EVIDENCE LINE. The verdict requires one of these; without it
            # an echoed nonce proves only that something read a file. The
            # returned VALUE is deliberately not recorded — a log carrying the
            # nonce would become a second channel to it.
            record("tool_call", {"name": name})
            reply(rid, {"content": [{"type": "text", "text": NONCE}],
                        "isError": False})
        else:
            record("tool_call_unknown", {"name": name})
            fail(rid, -32602, "unknown tool: " + repr(name))
    elif method == "ping":
        reply(rid, {})
    elif method == "resources/list":
        reply(rid, {"resources": []})
    elif method == "prompts/list":
        reply(rid, {"prompts": []})
    else:
        fail(rid, -32601, "method not found: " + repr(method))


def main():
    record("start", {"argv": sys.argv[1:]})
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except ValueError:
            record("unparseable", {"bytes": len(line)})
            continue
        try:
            handle(req)
        except Exception as exc:  # never die mid-session: report and continue
            record("handler_error", {"error": repr(exc)})
    record("eof", {})


if __name__ == "__main__":
    main()
`

// probeMCPFixture is a written-out fixture server: where its script is, where
// its call log will be, and which nonce it will hand back.
type probeMCPFixture struct {
	Dir     string
	Script  string
	CallLog string
	Nonce   string
}

// probeMCPWriteFixture materializes the server for one cell.
//
// dir MUST be sited OUTSIDE the cell's workspace by the caller. This function
// cannot check that for itself — it does not know where the workspace is — so
// the requirement is stated here and enforced at the one call site that matters
// (the Given step, which passes a sibling of the project directory). Putting the
// script inside the tree the agent can list would hand the agent the nonce
// through a channel this probe does not test.
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
		Dir:     dir,
		Script:  filepath.Join(dir, probeMCPScriptName),
		CallLog: filepath.Join(dir, probeMCPLogName),
		Nonce:   nonce,
	}
	src := strings.ReplaceAll(probeMCPServerSource, probeMCPNoncePlaceholder, nonce)
	src = strings.ReplaceAll(src, probeMCPLogPlaceholder, f.CallLog)
	if strings.Contains(src, probeMCPNoncePlaceholder) || strings.Contains(src, probeMCPLogPlaceholder) {
		return probeMCPFixture{}, fmt.Errorf("capability probe P2: the fixture server template still holds an unsubstituted placeholder — the server would serve a literal placeholder instead of the minted harp")
	}
	if err := os.WriteFile(f.Script, []byte(src), 0o700); err != nil {
		return probeMCPFixture{}, fmt.Errorf("capability probe P2: writing the fixture MCP server: %w", err)
	}
	// The log is NOT pre-created. Its absence and its emptiness must be
	// distinguishable from each other in the verdict: "the server never ran"
	// and "the server ran and the tool was never called" are different findings
	// about different subsystems, and a fixture that touched the file first
	// would erase the first one.
	return f, nil
}

// probeMCPConfigYAML is the registration: a top-level `mcp.servers` entry in the
// project's config.yaml.
//
// THIS IS A PRODUCTION SURFACE, chosen deliberately over the bundle route. Both
// exist (wire.MCPConfig from config, BundleMCP from a bundle's `mcp:` block) and
// both converge on the same per-engine writers, but a BUNDLE's MCP servers pass
// through the executable trust gate (extractMCPFromBundle → bundles.Decide),
// which withholds an unsigned local bundle's arbitrary command. A probe that
// tripped that gate would red with an MCP-delivery failure that is really a
// trust decision — the exact channel confusion this ladder's failure taxonomy
// exists to prevent. Config-level servers are ungated by design (wire.MCPServer's
// own security note: config.yaml is trusted local configuration), so this route
// tests the MCP path and nothing else.
//
// From here the value is production's all the way down: config → ManagedConfig.MCP
// → each engine's native surface — claude's --mcp-config scratch file,
// codex's config.toml [mcp_servers], kiro's .kiro/settings/mcp.json,
// opencode's opencode.json `mcp`. Nothing in this fixture writes an engine file.
func probeMCPConfigYAML(interpreter, scriptPath string) string {
	var b strings.Builder
	b.WriteString("mcp:\n  servers:\n    " + probeMCPServerName + ":\n")
	fmt.Fprintf(&b, "      command: %q\n", interpreter)
	b.WriteString("      args:\n")
	fmt.Fprintf(&b, "        - %q\n", scriptPath)
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
// THE ORDER IS THE ATTRIBUTION, and P2 adds one rung the floor does not have.
// The floor asks: did it run, did it say anything, is it the right form, did the
// channel deliver, is it the right object. P2 inserts THE TOOL PATH between form
// and delivery, because for this probe "the channel delivered" is not a property
// of the output at all — it is a property of the server having been called. That
// rung is what stops the probe from being a tautology:
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
	got, err := v.jsonObject(trimmed)
	if err != nil {
		return err
	}
	if err := mcpProbeToolWasCalled(v, r, trimmed); err != nil {
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

// probeMCPInterpreterAvailable reports whether the fixture's interpreter is on
// PATH, and why not when it is not. A cell with no interpreter must SKIP, never
// red: an MCP-delivery failure has to mean MCP delivery failed, and a missing
// python3 would otherwise be reported as a capability the engine lacks.
func probeMCPInterpreterAvailable() (string, string) {
	path, err := exec.LookPath(probeMCPInterpreter)
	if err != nil {
		return "", fmt.Sprintf("the fixture MCP server needs %s on PATH and it is not there (%v) — skipping rather than reporting an MCP-delivery failure that is really a missing interpreter", probeMCPInterpreter, err)
	}
	return path, ""
}
