package cli

import (
	"io"
	"path/filepath"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// session artifacts — the sub-noun for what a session PRODUCED, as opposed to
// what it recorded. Today that is the compacted essence: the readable summary
// `session compact` writes and `session show` prints.
//
// It is separated from the transcript because the two have opposite recovery
// properties, and a caller deciding what to throw away needs to know which is
// which. An artifact is DERIVED — while the transcript survives, compaction
// can produce it again. A transcript is not derived from anything.

var sessionArtifactsCmd = groupNodeDefault(&cobra.Command{
	Use:   "artifacts",
	Short: "What a session produced — its compacted essence: list it, destroy it",
	Long: `A session's artifacts are what ctxloom derived from it: essence.md in
the session's output dir (<Documents>/ctxloom/<project>/<harp>/ unless the
output_dir config key says otherwise). No sweep or clean ever removes it;
purge here is the one command that does.

  list    which sessions have been compacted, and how large the result is
  purge   destroy the essence, reporting first

Artifacts are recoverable in a way a transcript is not: while the transcript
is still on disk, 'ctxloom session compact' produces the essence again.`,
}, "list")

// sessionArtifactsListAll widens the listing past the current project, the
// same way `session list --all` does.
var sessionArtifactsListAll bool

// sessionArtifactRow is the listing's rendering projection — never the domain
// type, matching cli.SessionRow's convention (see session_row.go).
type sessionArtifactRow struct {
	Harp      string `json:"harp"           label:"Harp"      col:"HARP"`
	Compacted bool   `json:"compacted"      label:"Compacted" col:"COMPACTED"`
	Bytes     int64  `json:"bytes"          label:"Bytes"     col:"BYTES"`
	Path      string `json:"path,omitempty" label:"Path"      col:"PATH"`
}

// sessionArtifactReport is `session artifacts list`'s payload.
type sessionArtifactReport struct {
	Artifacts []sessionArtifactRow `json:"artifacts"`
}

var sessionArtifactsListCmd = &cobra.Command{
	Use:   "list [<harp-name>]",
	Short: "List which sessions have been compacted, and how large each essence is",
	Long: `Names every recorded session and whether it has been compacted yet, with
the essence's size on disk. An uncompacted session is listed saying so —
omitting it would make "nothing has been compacted" indistinguishable from
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

// newSessionArtifactRow answers "has this been compacted, and how big is the
// result" for one harp, from the FILE rather than from the index's Summary.
// The index carries a Summary as soon as anything syncs a summary line, which
// happens long before a real essence is ever written — reading that instead
// would report sessions as compacted that have nothing to show.
func newSessionArtifactRow(harp string) sessionArtifactRow {
	row := sessionArtifactRow{Harp: harp}
	if path, size, ok := statHarpFile(afero.NewOsFs(), harp, essencePath); ok {
		row.Compacted = true
		row.Bytes = size
		row.Path = path
	}
	return row
}

// essencePath is harp's current essence, in its recorded output dir.
func essencePath(harp string) (string, error) {
	out, err := sessions.OutputDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(out, paths.EssenceFileName), nil
}

// renderSessionArtifacts is the human render: a table, or the explicit empty
// line a table cannot carry.
func renderSessionArtifacts(w io.Writer, rep sessionArtifactReport) error {
	if len(rep.Artifacts) == 0 {
		ew := errwriter.New(w)
		ew.Println("(no sessions)")
		return ew.Err()
	}
	return clifmt.Render(w, rep.Artifacts, clifmt.FormatText)
}
