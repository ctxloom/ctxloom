package cli

import (
	"fmt"
	"sort"

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
type llmEntry struct {
	Label    string `json:"label"`
	Default  bool   `json:"default"`
	Authored bool   `json:"authored"`
}

// llmListEntries pairs each available LLM name with its default marker and its
// authored marker. An empty defaultLabel (config unavailable) marks nothing as
// default; a nil authored predicate (same case) marks nothing as authored,
// which is the truthful answer — with no config, nothing could have been
// authored in one.
func llmListEntries(names []string, defaultLabel string, authored func(string) bool) []llmEntry {
	entries := make([]llmEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, llmEntry{
			Label:    name,
			Default:  name != "" && name == defaultLabel,
			Authored: authored != nil && authored(name),
		})
	}
	return entries
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
	names, defaultLabel, authored := availableLLMsWithDefault()
	entries := llmListEntries(names, defaultLabel, authored)
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
		}
		fmt.Fprintln(out, "\n[configured] declared in your config.yaml   [built-in] supplied by ctxloom")
		return nil
	})
}

// availableLLMsWithDefault enumerates the LLM labels, the configured default,
// and the predicate answering which labels the user authored. With no usable
// config it degrades (CLAUDE.md fault tolerance) to the built-in backends, no
// default, and a predicate that authors NOTHING — custom labels, the primary,
// and authorship itself all come from config, and with no config to have
// written anything in, "not authored" is the fact, not a fallback.
func availableLLMsWithDefault() ([]string, string, func(string) bool) {
	cfg, err := GetConfig()
	if err != nil {
		names := backends.List()
		sort.Strings(names)
		return names, "", noneAuthored
	}
	// Reuse the same name set and default identity `llm default` reports:
	// built-ins unioned with configured labels, and the primary *label*
	// (not the backend type) marked as default, so the two commands agree.
	return operations.AvailableLLMNames(cfg), cfg.PrimaryLabel(), cfg.IsLLMUserAuthored
}

func init() {
	llmCmd.AddCommand(llmListCmd)
}
