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

// acpEntriesCmd is the real home (CLI-primary reorg plan, Decision 5: "acp
// agents" → "acp entries") of the ACP-server entry lister — renamed because
// "agents" collided with ctxloom's own unrelated "agent" noun (engine↔profile
// bindings) while this command is entirely about the SERVER direction's
// editor-config entries.
var acpEntriesCmd = &cobra.Command{
	Use:   "entries",
	Short: "List the ACP agent-server entries to configure in an editor",
	Long: `List this project's advertisable ACP agent-server entries: the plain ctxloom
entry plus one per agent binding (ACP has no in-protocol selection — a client
picks by choosing which command it launches, so each binding advertises as its
own agent-server entry).

The output includes a ready-to-paste Zed settings.json "agent_servers" block;
other ACP clients configure the same command/args in their own format.`,
	Args: cobra.NoArgs,
	RunE: runACPEntries,
}

// acpAgentsCmd is a Deprecated alias for `ctxloom acp entries`.
var acpAgentsCmd = &cobra.Command{
	Use:        "agents",
	Short:      "List the ACP agent-server entries to configure in an editor",
	Deprecated: "use `ctxloom acp entries` instead",
	Args:       cobra.NoArgs,
	RunE:       runACPEntries,
}

func runACPEntries(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	entries := buildACPAgentEntries(operations.ListAgents(cfg), ctxloomExecutable())
	return emit(cmd, entries, func() error {
		return renderACPAgents(cmd.OutOrStdout(), entries)
	})
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
// agent first, then one per agent (already name-sorted by ListAgents). The
// emitted args invoke the reorged 'acp server' subcommand, not the deprecated
// bare form.
func buildACPAgentEntries(subs []operations.AgentEntry, exe string) []acpAgentEntry {
	entries := []acpAgentEntry{{Name: "ctxloom", Command: exe, Args: []string{"acp", "server"}}}
	for _, s := range subs {
		entries = append(entries, acpAgentEntry{
			Name:     "ctxloom: " + s.Name,
			Command:  exe,
			Args:     []string{"acp", "server", "--agent", s.Name},
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
	w.Printf("ACP agent-server entries (%d):\n", len(entries))
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
		w.Println("No agent bindings defined — each binding would advertise as its own agent-server entry.")
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
		// Both errors are discarded because neither operand can produce one:
		// encoding/json reports a failure only for channels, funcs, complex
		// values, cyclic pointers, NaN/Inf, or a Marshaler that errors, and
		// these are a string and a struct of string + []string. Invalid UTF-8
		// is COERCED to U+FFFD, not rejected. Widening acpAgentEntry with a
		// type Marshal can fail on breaks that, which is what
		// TestZedAgentServersBlock_IsAlwaysValidJSON exists to notice.
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
	acpCmd.AddCommand(acpEntriesCmd)
	acpCmd.AddCommand(acpAgentsCmd)
}
