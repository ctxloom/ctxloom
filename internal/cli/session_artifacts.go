package cli

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// session artifacts — the sub-noun for what a session PRODUCED, as opposed to
// what it recorded. Today that is the distilled essence: the readable summary
// `session distill` writes and `session show` prints.
//
// It is separated from the transcript because the two have opposite recovery
// properties, and a caller deciding what to throw away needs to know which is
// which. An artifact is DERIVED — while the transcript survives, distillation
// can produce it again. A transcript is not derived from anything.

var sessionArtifactsCmd = groupNodeDefault(&cobra.Command{
	Use:   "artifacts",
	Short: "What a session produced — its distilled essence: list it, destroy it",
	Long: `A session's artifacts are what ctxloom derived from it, at
~/.ctxloom/sessions/<harp>/essence.md.

  list    which sessions have been distilled, and how large the result is
  purge   destroy the essence, reporting first

Artifacts are recoverable in a way a transcript is not: while the transcript
is still on disk, 'ctxloom session distill' produces the essence again.`,
}, "list")

// sessionArtifactsListAll widens the listing past the current project, the
// same way `session list --all` does.
var sessionArtifactsListAll bool

// sessionArtifactRow is the listing's rendering projection — never the domain
// type, matching cli.SessionRow's convention (see session_row.go).
type sessionArtifactRow struct {
	Harp      string `json:"harp"           label:"Harp"      col:"HARP"`
	Distilled bool   `json:"distilled"      label:"Distilled" col:"DISTILLED"`
	Bytes     int64  `json:"bytes"          label:"Bytes"     col:"BYTES"`
	Path      string `json:"path,omitempty" label:"Path"      col:"PATH"`
}

// sessionArtifactReport is `session artifacts list`'s payload. Artifacts is
// normalized nil -> [] so JSON renders [] not null, for the reason
// loadSessionEntries gives.
type sessionArtifactReport struct {
	Artifacts []sessionArtifactRow `json:"artifacts"`
}

var sessionArtifactsListCmd = &cobra.Command{
	Use:   "list [<harp-name>]",
	Short: "List which sessions have been distilled, and how large each essence is",
	Long: `Names every recorded session and whether it has been distilled yet, with
the essence's size on disk. An undistilled session is listed saying so —
omitting it would make "nothing has been distilled" indistinguishable from
"there are no sessions".

Naming a harp restricts the listing to that one session.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSessionArtifactsList,
}

func init() {
	sessionArtifactsListCmd.Flags().BoolVar(&sessionArtifactsListAll, "all", false,
		"Include sessions from every project (default: filter to cwd)")
	sessionArtifactsCmd.AddCommand(sessionArtifactsListCmd)
	sessionCmd.AddCommand(sessionArtifactsCmd)
}

func runSessionArtifactsList(cmd *cobra.Command, args []string) error {
	entries, err := sessionEntriesForHarpArg(args, sessionArtifactsListAll)
	if err != nil {
		return err
	}

	rep := sessionArtifactReport{Artifacts: make([]sessionArtifactRow, 0, len(entries))}
	for _, e := range entries {
		rep.Artifacts = append(rep.Artifacts, newSessionArtifactRow(e.HarpName))
	}
	return emit(cmd, rep, func() error {
		return renderSessionArtifacts(cmd.OutOrStdout(), rep)
	})
}

// newSessionArtifactRow answers "has this been distilled, and how big is the
// result" for one harp, from the FILE rather than from the index's Summary.
// The index carries a Summary as soon as anything syncs a picker line, which
// happens long before a real essence is ever written — reading that instead
// would report sessions as distilled that have nothing to show.
func newSessionArtifactRow(harp string) sessionArtifactRow {
	row := sessionArtifactRow{Harp: harp}
	path, err := paths.HarpEssencePath(harp)
	if err != nil {
		return row
	}
	info, statErr := os.Stat(path)
	if statErr != nil || !info.Mode().IsRegular() {
		return row
	}
	row.Distilled = true
	row.Bytes = info.Size()
	row.Path = path
	return row
}

// renderSessionArtifacts is the human render: a table, or the explicit empty
// line a table cannot carry.
func renderSessionArtifacts(w io.Writer, rep sessionArtifactReport) error {
	if len(rep.Artifacts) == 0 {
		ew := iox.NewErrWriter(w)
		ew.Println("(no sessions)")
		return ew.Err()
	}
	return clifmt.Render(w, rep.Artifacts, clifmt.FormatText)
}
