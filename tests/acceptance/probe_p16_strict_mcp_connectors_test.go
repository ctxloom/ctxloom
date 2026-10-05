package acceptance

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The P16 verdict, exercised without an engine.

// p16TestStream is a finished turn whose init frame lists servers.
func p16TestStream(t *testing.T, servers ...p16MCPServer) string {
	t.Helper()
	if servers == nil {
		servers = []p16MCPServer{}
	}
	return strings.Join([]string{
		p12Line(t, map[string]any{"type": "system", "subtype": "init", "mcp_servers": servers}),
		p12Line(t, map[string]any{"type": "result", "subtype": "success"}),
	}, "\n")
}

var p16TestConnector = p16MCPServer{Name: p16ConnectorPrefix + "Gmail", Status: "connected"}

func p16TestOutcome(control, strict string) p16Outcome {
	return p16Outcome{
		Cell:    probeCellID{Probe: probeP16, Engine: "claude-code", Runtime: "host", Workspace: "none"},
		Control: p16Arm{Started: true, Run: probeRun{Stdout: control}},
		Strict:  p16Arm{Started: true, Run: probeRun{Stdout: strict}},
	}
}

func TestP16_Verdict(t *testing.T) {
	other := p16MCPServer{Name: "fixture", Status: "connected"}

	t.Run("connectors in the control and none under the flag is green", func(t *testing.T) {
		require.NoError(t, p16Assert(p16TestOutcome(p16TestStream(t, p16TestConnector, other), p16TestStream(t))))
	})
	t.Run("a connector under the flag is CONNECTORS-LEAKED", func(t *testing.T) {
		p12RequireShape(t, p16Assert(p16TestOutcome(p16TestStream(t, p16TestConnector), p16TestStream(t, p16TestConnector))), shapeConnectorsLeaked)
	})
	t.Run("a connector that failed to connect still counts as loaded", func(t *testing.T) {
		failed := p16MCPServer{Name: p16TestConnector.Name, Status: "failed"}
		p12RequireShape(t, p16Assert(p16TestOutcome(p16TestStream(t, p16TestConnector), p16TestStream(t, failed))), shapeConnectorsLeaked)
	})
	t.Run("no connector in the control is CONNECTORS-ABSENT: the flag measured nothing", func(t *testing.T) {
		p12RequireShape(t, p16Assert(p16TestOutcome(p16TestStream(t, other), p16TestStream(t))), shapeConnectorsAbsent)
	})
	t.Run("a server merely named like one is not a connector", func(t *testing.T) {
		lookalike := p16MCPServer{Name: "claude.aiGmail", Status: "connected"}
		p12RequireShape(t, p16Assert(p16TestOutcome(p16TestStream(t, lookalike), p16TestStream(t))), shapeConnectorsAbsent)
	})
	t.Run("an arm with no result frame is OUTPUT-FORMAT", func(t *testing.T) {
		initOnly := strings.Split(p16TestStream(t), "\n")[0]
		p12RequireShape(t, p16Assert(p16TestOutcome(p16TestStream(t, p16TestConnector), initOnly)), shapeOutputFormat)
	})
	t.Run("an arm that failed to run is RUN", func(t *testing.T) {
		o := p16TestOutcome(p16TestStream(t, p16TestConnector), p16TestStream(t))
		o.Control.Run.Err = errors.New("exit 1")
		p12RequireShape(t, p16Assert(o), shapeRunFailed)
		o = p16TestOutcome(p16TestStream(t, p16TestConnector), p16TestStream(t))
		o.Strict.TimedOut = true
		p12RequireShape(t, p16Assert(o), shapeRunFailed)
	})
}

func TestP16_Args(t *testing.T) {
	control, strict := p16Args(false), p16Args(true)
	assert.NotContains(t, control, p16StrictFlag)
	assert.Equal(t, append(control, p16StrictFlag), strict, "the arms differ by the flag alone")
	assert.Contains(t, control, liveClaudeModel)
}
