package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/harp"
)

var (
	harpComponents int
	harpSeparator  string
	harpMaxLength  int
	harpCount      int
	harpGroup      string
)

var harpCmd = &cobra.Command{
	Use:   "harp",
	Short: "Generate harp IDs (human-appropriate random phraselets)",
	Long: `Generate human-readable, memorable identifiers from adjective-noun
combinations, e.g. "swift-amber-falcon".

ctxloom uses harp IDs internally for sessions and tasks. This command
exposes the generator for ad-hoc use.

Examples:
  ctxloom harp                       # one name, default 3 components
  ctxloom harp -c 2                  # adj-noun
  ctxloom harp -n 5                  # five names, one per line
  ctxloom harp -g long                # draw from the long-word group
  ctxloom harp -s _ --max-len 5      # short_hawk style`,
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := harp.Options{
			Components:       harpComponents,
			MaxElementLength: harpMaxLength,
			Separator:        harpSeparator,
			Group:            harpGroup,
		}
		for range harpCount {
			fmt.Println(harp.GenerateNameWithOptions(opts))
		}
		return nil
	},
}

// harpGroupsHelp renders the available groups for the --group flag usage.
func harpGroupsHelp() string {
	g := harp.Groups()
	slices.Sort(g)
	return fmt.Sprintf("word-list group (%s)", strings.Join(g, ", "))
}

func init() {
	harpCmd.Flags().IntVarP(&harpComponents, "components", "c", 3, "number of words (2-16)")
	harpCmd.Flags().StringVarP(&harpSeparator, "separator", "s", "-", "delimiter between words")
	harpCmd.Flags().IntVar(&harpMaxLength, "max-len", 0, "max characters per word (0 = no cap)")
	harpCmd.Flags().IntVarP(&harpCount, "number", "n", 1, "how many names to generate")
	harpCmd.Flags().StringVarP(&harpGroup, "group", "g", harp.DefaultGroup, harpGroupsHelp())
	rootCmd.AddCommand(harpCmd)
}
