// Package acceptance: P16, STRICT MCP CONNECTORS — the capability ladder's
// rung for whether --strict-mcp-config keeps claude.ai's connectors out of a
// claude -p session (conformance cell T1).
//
// UNTAGGED, like probe_p12_permission_hook.go: the cell is @live and paid, so
// the verdict and the argv below are the only part of this rung a hermetic
// test can execute (probe_p16_strict_mcp_connectors_test.go). The godog
// plumbing lives in steps_p16_strict_mcp_connectors.go behind the acceptance
// tag.
//
// WHAT THIS RUNG PINS. An untrusted repository's child is launched with
// --setting-sources user --strict-mcp-config (P13), so the only MCP servers
// it reaches are the ones ctxloom hands it on --mcp-config. claude.ai's
// connectors are a server source of their own, fetched for the account
// rather than read from any settings file, and --setting-sources does not
// remove them. Whether --strict-mcp-config does is the question: if it does
// not, every child reaches the account's connectors whatever posture ctxloom
// chose.
//
// ONE CELL, TWO TURNS. The control turn runs without the flag and must list
// at least one connector, or the account has none to suppress and the flag
// arm measured nothing; the strict turn adds the flag and must list none.
// Both are read from the init frame's mcp_servers, written before the model
// says anything, so the prompt is a one-word reply.
package acceptance

import (
	"fmt"
	"strings"
)

// p16Family is this rung's name in a skip line, a failure message and the
// evidence line.
const p16Family = "strict-mcp-connectors"

const (
	// p16StrictFlag is the flag under test.
	p16StrictFlag = "--strict-mcp-config"
	// p16ConnectorPrefix is how claude names a claude.ai connector in the init
	// frame ("claude.ai Gmail").
	p16ConnectorPrefix = "claude.ai "
	// p16Prompt asks for nothing a tool could answer.
	p16Prompt = "Reply with the single word ok and nothing else."
)

// p16MCPServer is one init-frame mcp_servers entry.
type p16MCPServer struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// p16Args is one arm's argv after the binary; the arms differ by the flag
// alone.
func p16Args(strict bool) []string {
	args := []string{"-p", p16Prompt, "--output-format", "stream-json", "--verbose", "--model", liveClaudeModel}
	if strict {
		args = append(args, p16StrictFlag)
	}
	return args
}

// p16Connectors is every server in the init frame named as a claude.ai
// connector, whatever its status: a connector that failed to connect was
// still loaded from the account.
func p16Connectors(s p12Stream) []string {
	var names []string
	for _, srv := range s.MCPServers {
		if strings.HasPrefix(srv.Name, p16ConnectorPrefix) {
			names = append(names, srv.Name+"("+srv.Status+")")
		}
	}
	return names
}

// p16Arm is one of the cell's two turns.
type p16Arm struct {
	Started, TimedOut bool
	Run               probeRun
}

// p16Outcome is everything a P16 verdict may look at.
type p16Outcome struct {
	Cell            probeCellID
	Control, Strict p16Arm
}

// The shapes this rung adds.
const (
	// shapeConnectorsAbsent: the control turn loaded no claude.ai connector,
	// so the account has none to suppress and the strict arm measured nothing.
	shapeConnectorsAbsent probeShape = "CONNECTORS-ABSENT failure"
	// shapeConnectorsLeaked: a claude.ai connector was loaded despite
	// --strict-mcp-config.
	shapeConnectorsLeaked probeShape = "CONNECTORS-LEAKED failure"
)

func (o p16Outcome) verdict() probeVerdict {
	return probeVerdict{Family: p16Family, Cell: o.Cell, Channel: channelInitMCPServers}
}

// summary is the one-line measurement the evidence line prints.
func (o p16Outcome) summary() string {
	c, _ := p12Decode(o.Control.Run.Stdout)
	s, _ := p12Decode(o.Strict.Run.Stdout)
	return fmt.Sprintf("control mcp_servers=%v connectors=%v | strict mcp_servers=%v connectors=%v",
		c.MCPServers, p16Connectors(c), s.MCPServers, p16Connectors(s))
}

func (o p16Outcome) evidence() string {
	arm := func(name string, a p16Arm) string {
		return fmt.Sprintf("\n[%s] exit=%d runErr=%v timedOut=%t\nstdout:\n%s\nstderr:\n%s", name, a.Run.ExitCode, a.Run.Err, a.TimedOut, a.Run.Stdout, a.Run.Stderr)
	}
	return "\n" + o.summary() + arm("control", o.Control) + arm(p16StrictFlag, o.Strict)
}

// decode is the half both arms share: the turn ran and reached a result frame.
func (o p16Outcome) decode(v probeVerdict, name string, a p16Arm) (p12Stream, error) {
	if !a.Started || a.TimedOut {
		return p12Stream{}, v.fail(shapeRunFailed, fmt.Sprintf("the %s turn did not complete (started=%t timedOut=%t)", name, a.Started, a.TimedOut), o.evidence())
	}
	trimmed, err := v.ran(a.Run)
	if err != nil {
		return p12Stream{}, err
	}
	s, err := p12Decode(trimmed)
	if err != nil {
		return s, v.fail(shapeOutputFormat, fmt.Sprintf("the %s turn: %v", name, err), o.evidence())
	}
	if !s.SawResult {
		return s, v.fail(shapeOutputFormat, fmt.Sprintf("the %s turn's stream carried no result frame", name), o.evidence())
	}
	return s, nil
}

// p16Assert judges the cell: the control loaded a connector, the strict arm
// loaded none.
func p16Assert(o p16Outcome) error {
	v := o.verdict()
	control, err := o.decode(v, "control", o.Control)
	if err != nil {
		return err
	}
	strict, err := o.decode(v, p16StrictFlag, o.Strict)
	if err != nil {
		return err
	}
	if len(p16Connectors(control)) == 0 {
		return v.fail(shapeConnectorsAbsent, fmt.Sprintf("the control turn's init frame listed no %q server (mcp_servers=%v), so there was nothing for %s to suppress", p16ConnectorPrefix, control.MCPServers, p16StrictFlag), o.evidence())
	}
	if leaked := p16Connectors(strict); len(leaked) > 0 {
		return v.fail(shapeConnectorsLeaked, fmt.Sprintf("under %s the init frame still listed %v", p16StrictFlag, leaked), o.evidence())
	}
	return nil
}
