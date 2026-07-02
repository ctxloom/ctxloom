package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// acpAgentEntry is one advertisable ACP agent-server entry: the command an ACP
// client (editor) configures to run ctxloom as that agent.
type acpAgentEntry struct {
	Name     string   `json:"name"`
	Command  string   `json:"command"`
	Args     []string `json:"args"`
	Agent    string   `json:"agent,omitempty"`
	Engine   string   `json:"engine,omitempty"`
	Profiles []string `json:"profiles,omitempty"`
}

var acpAgentsCmd = &cobra.Command{
	Use:   "agents",
	Short: "List the ACP agent entries to configure in an editor",
	Long: `List this project's advertisable ACP agent entries: the plain ctxloom agent
plus one entry per agent (ACP has no in-protocol agent selection — a client
picks the agent by choosing which command it launches, so each agent
advertises as its own agent entry).

The output includes a ready-to-paste Zed settings.json "agent_servers" block;
other ACP clients configure the same command/args in their own format.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		entries := buildACPAgentEntries(operations.ListAgents(cfg), ctxloomExecutable())
		return emit(cmd, entries, func() error {
			return renderACPAgents(cmd.OutOrStdout(), entries)
		})
	},
}

// ctxloomExecutable resolves the running binary's path for the emitted
// entries; a resolution failure degrades to the bare command name (PATH
// lookup on the editor's side).
func ctxloomExecutable() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return "ctxloom"
	}
	return exe
}

// buildACPAgentEntries builds the advertisable entries: the plain default
// agent first, then one per agent (already name-sorted by ListAgents).
func buildACPAgentEntries(subs []operations.AgentEntry, exe string) []acpAgentEntry {
	entries := []acpAgentEntry{{Name: "ctxloom", Command: exe, Args: []string{"acp"}}}
	for _, s := range subs {
		entries = append(entries, acpAgentEntry{
			Name:     "ctxloom: " + s.Name,
			Command:  exe,
			Args:     []string{"acp", "--agent", s.Name},
			Agent:    s.Name,
			Engine:   s.Engine,
			Profiles: s.Profiles,
		})
	}
	return entries
}

// zedAgentServer is the value shape of one Zed `agent_servers` entry.
type zedAgentServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// renderACPAgents writes the human-readable entry list plus the Zed paste
// block. Extracted from RunE so the formatting is testable without cobra.
func renderACPAgents(out io.Writer, entries []acpAgentEntry) error {
	w := iox.NewErrWriter(out)
	w.Printf("ACP agent entries (%d):\n", len(entries))
	for _, e := range entries {
		w.Printf("  %s\n", e.Name)
		if e.Agent != "" {
			if e.Engine != "" {
				w.Printf("    engine: %s\n", e.Engine)
			} else {
				w.Printf("    engine: (project default)\n")
			}
			if len(e.Profiles) > 0 {
				w.Printf("    profiles: %s\n", strings.Join(e.Profiles, ", "))
			}
		}
		w.Printf("    command: %s\n", e.Command+" "+strings.Join(e.Args, " "))
	}
	if len(entries) == 1 {
		w.Println()
		w.Println("No agents defined — each agent would advertise as its own agent entry.")
		w.Println("Define one under 'agents:' in .ctxloom/config.yaml or as .ctxloom/agents/<name>.yaml.")
	}
	w.Println()
	w.Println(`Zed settings.json — merge into "agent_servers" (other ACP clients configure the same command/args):`)
	w.Println(zedAgentServersBlock(entries))
	return w.Err()
}

// zedAgentServersBlock renders the entries as a ready-to-paste Zed
// "agent_servers" JSON object. Hand-assembled so the entry order stays stable
// (a Go map would randomize it); names and values marshal individually, so
// the output is still valid JSON.
func zedAgentServersBlock(entries []acpAgentEntry) string {
	var b strings.Builder
	b.WriteString("{\n")
	for i, e := range entries {
		key, _ := json.Marshal(e.Name)
		val, _ := json.Marshal(zedAgentServer{Command: e.Command, Args: e.Args})
		fmt.Fprintf(&b, "  %s: %s", key, val)
		if i < len(entries)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}")
	return b.String()
}

func init() {
	acpCmd.AddCommand(acpAgentsCmd)
}
