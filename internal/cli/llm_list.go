package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// llmEntry is one row of `llm list --format json`: an LLM config label (the
// value `-l`/`--llm` and `agent set --engine` accept), whether it is the
// configured default (the primary label), and whether the label is one the
// user AUTHORED.
//
// Authored is not decoration. This listing prints the UNION of the registered
// backends and config.yaml's labels, and mergeDefaultConfig's whole-registry
// fallback additionally merges the embedded default registry into the READ
// view of any project that declared no llm.configs at all — so a bare engine
// name arrives here indistinguishable from a label the team wrote and
// maintains. The same conflation is why `llm remove claude-code` on such a
// project used to report success and delete nothing (see
// config.Config.IsLLMUserAuthored, which answers this per label and is the
// ONE definition of the distinction; do not grow a second predicate).
// Runtimes/ContainerWithheld carry the per-engine RUNTIME OFFER: which
// `agent create --runtime` values this label may be offered, and why the
// container axes are absent when they are. They are here rather than on a
// command of their own because the agent-creating setup interview already
// enumerates engines with `llm list` (phase 1), and the runtime question it
// must ask per agent is answerable only per ENGINE — an interview that offered
// a container axis for an engine with no container auth would collect a choice
// `agent create` then refuses. See operations.RuntimeOffer.
type llmEntry struct {
	Label             string   `json:"label"`
	Default           bool     `json:"default"`
	Authored          bool     `json:"authored"`
	Runtimes          []string `json:"runtimes,omitempty"`
	ContainerWithheld string   `json:"container_withheld,omitempty"`
}

// llmListEntries pairs each available LLM name with its default marker, its
// authored marker, and its runtime offer. An empty defaultLabel (config
// unavailable) marks nothing as default; a nil authored predicate (same case)
// marks nothing as authored, which is the truthful answer — with no config,
// nothing could have been authored in one. A nil offer predicate leaves the
// runtime columns empty for the same reason: the axes a label may be offered
// are resolved through its backend, and with no config there is no resolution.
func llmListEntries(names []string, defaultLabel string, authored func(string) bool, offer func(string) operations.RuntimeOffer) []llmEntry {
	entries := make([]llmEntry, 0, len(names))
	for _, name := range names {
		e := llmEntry{
			Label:    name,
			Default:  name != "" && name == defaultLabel,
			Authored: authored != nil && authored(name),
		}
		if offer != nil {
			o := offer(name)
			// The offer carries the TYPED axis; this entry is the wire/text
			// shape, so the spelling is taken OUT of the vocabulary here
			// rather than the vocabulary being asserted back in anywhere.
			for _, r := range o.Runtimes {
				e.Runtimes = append(e.Runtimes, string(r))
			}
			e.ContainerWithheld = o.ContainerWithheld
		}
		entries = append(entries, e)
	}
	return entries
}

// llmRuntimeLine renders one entry's runtime offer for the text listing, or ""
// when there is no offer to render (the degraded, config-less path). The
// withheld REASON is printed, never elided: an option silently missing from a
// menu is indistinguishable from ctxloom having decided against it, and the
// real fact — this engine cannot authenticate inside a container — is both
// specific and actionable.
func llmRuntimeLine(e llmEntry) string {
	if len(e.Runtimes) == 0 {
		return ""
	}
	line := "      runtimes: " + strings.Join(e.Runtimes, ", ")
	if e.ContainerWithheld != "" {
		line += "\n      no container runtime: " + e.ContainerWithheld
	}
	return line
}

// noneAuthored is the authorship predicate for a run whose config could not be
// loaded at all. It is a named function rather than an inline closure so the
// DIRECTION of the degraded default can be pinned by a test: with no config to
// have written anything in, "not authored" is the fact, and answering the
// other way would present ctxloom's own built-ins as the user's work — the
// exact claim this listing exists to stop making.
func noneAuthored(string) bool { return false }

// llmOriginMarker renders the authored/fallback distinction for the text
// listing. BOTH kinds are marked explicitly, rather than marking one and
// leaving the other bare: an unmarked row would be read as "no information",
// and the whole point here is that a reader can tell the two apart at a
// glance without knowing which convention is in force.
func llmOriginMarker(authored bool) string {
	if authored {
		return "[configured]"
	}
	return "[built-in]"
}

var llmListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List available LLMs",
	Long:    `Lists the available LLM backends.`,
	RunE:    runLLMList,
}

func runLLMList(cmd *cobra.Command, args []string) error {
	names, defaultLabel, authored, offer := availableLLMsWithDefault()
	entries := llmListEntries(names, defaultLabel, authored, offer)
	return emit(cmd, entries, func() error {
		out := cmd.OutOrStdout()
		fmt.Fprintln(out, "Available LLMs:")
		for _, e := range entries {
			label := e.Label
			if e.Default {
				// The `(default)` marker stays immediately after the label:
				// it is the substring existing callers and specs match on.
				label += " (default)"
			}
			fmt.Fprintf(out, "  %s %s\n", label, llmOriginMarker(e.Authored))
			if line := llmRuntimeLine(e); line != "" {
				fmt.Fprintln(out, line)
			}
		}
		fmt.Fprintln(out, "\n[configured] declared in your config.yaml   [built-in] supplied by ctxloom")
		fmt.Fprintln(out, "runtimes: the `agent create --runtime` values that engine can be given. There is")
		fmt.Fprintln(out, "no default — an agent created without one inherits whatever the project says,")
		fmt.Fprintln(out, "which is a default rather than a decision.")
		return nil
	})
}

// availableLLMsWithDefault enumerates the LLM labels, the configured default,
// the predicate answering which labels the user authored, and the per-label
// runtime offer. With no usable config it degrades (CLAUDE.md fault tolerance)
// to the built-in backends, no default, a predicate that authors NOTHING, and
// NO runtime offer — custom labels, the primary, authorship, and the
// label→backend resolution the offer needs all come from config, and with no
// config to have written anything in, "not authored" is the fact, not a
// fallback.
func availableLLMsWithDefault() ([]string, string, func(string) bool, func(string) operations.RuntimeOffer) {
	cfg, err := GetConfig()
	if err != nil {
		names := backends.List()
		sort.Strings(names)
		return names, "", noneAuthored, nil
	}
	// Reuse the same name set and default identity `llm default` reports:
	// built-ins unioned with configured labels, and the primary *label*
	// (not the backend type) marked as default, so the two commands agree.
	return operations.AvailableLLMNames(cfg), cfg.PrimaryLabel(), cfg.IsLLMUserAuthored,
		func(label string) operations.RuntimeOffer { return operations.AgentRuntimeOffer(cfg, label) }
}

func init() {
	llmCmd.AddCommand(llmListCmd)
}
