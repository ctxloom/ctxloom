package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// session transcript — the sub-noun for a session's recorded conversation.
//
// A transcript is a POPULATION, not a property: it has its own listing, its
// own stream, and its own destroyer. Giving it a namespace is what lets each
// of those be spelled where a reader looks for it, instead of scattering
// `session watch` and a `--transcript` flag across the parent noun.
//
// The bare form lists, through groupNodeDefault — the seam `remote` works.
// Listing is read-only and cheap, so it is safe to reach by accident, which
// is the precondition that seam demands.

var sessionTranscriptCmd = groupNodeDefault(&cobra.Command{
	Use:   "transcript",
	Short: "The recorded conversation behind a session: list it, watch it, destroy it",
	Long: `A session's transcript is ctxloom's own canonical, engine-agnostic record
of what was said, at ~/.ctxloom/sessions/<harp>/persist/transcript.jsonl.

  list      which sessions have one, and how large it is (the bare form)
  watch     stream one as structured turns, live or from the store
  purge     destroy the machine-written bulk, reporting first`,
}, "list")

// sessionTranscriptListAll widens the listing past the current project, the
// same way `session list --all` does.
var sessionTranscriptListAll bool

// sessionTranscriptRow is the listing's rendering projection — never the
// domain type, matching cli.SessionRow's convention (see session_row.go).
type sessionTranscriptRow struct {
	Harp     string `json:"harp"           label:"Harp"     col:"HARP"`
	Captured bool   `json:"captured"       label:"Captured" col:"CAPTURED"`
	Bytes    int64  `json:"bytes"          label:"Bytes"    col:"BYTES"`
	Path     string `json:"path,omitempty" label:"Path"     col:"PATH"`
}

// sessionTranscriptReport is `session transcript list`'s payload. Transcripts
// is normalized nil -> [] so JSON renders [] not null, for the reason
// loadSessionEntries gives.
type sessionTranscriptReport struct {
	Transcripts []sessionTranscriptRow `json:"transcripts"`
}

var sessionTranscriptListCmd = &cobra.Command{
	Use:   "list [<harp-name>]",
	Short: "List which sessions have a captured transcript, and how large each one is",
	Long: `Names every recorded session and whether ctxloom captured its transcript,
with the size on disk. A session whose transcript was never captured is
listed too, saying so — omitting it would make "nothing was captured"
indistinguishable from "there are no sessions".

Naming a harp restricts the listing to that one session.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSessionTranscriptList,
}

func init() {
	sessionTranscriptListCmd.Flags().BoolVar(&sessionTranscriptListAll, "all", false,
		"Include sessions from every project (default: filter to cwd)")
	sessionTranscriptCmd.AddCommand(sessionTranscriptListCmd, sessionWatchCmd)
	sessionCmd.AddCommand(sessionTranscriptCmd)
}

func runSessionTranscriptList(cmd *cobra.Command, args []string) error {
	entries, err := sessionEntriesForHarpArg(args, sessionTranscriptListAll)
	if err != nil {
		return err
	}

	rep := sessionTranscriptReport{Transcripts: make([]sessionTranscriptRow, 0, len(entries))}
	for _, e := range entries {
		rep.Transcripts = append(rep.Transcripts, newSessionTranscriptRow(e.HarpName))
	}
	return emit(cmd, rep, func() error {
		return renderSessionTranscripts(cmd.OutOrStdout(), rep)
	})
}

// newSessionTranscriptRow answers "is there a transcript, and how big" for one
// harp. Absence is a row, not an omission; a stat that fails for a reason
// other than absence leaves Captured false and Bytes 0, which is the same
// honest answer ("ctxloom cannot show you one") without inventing a size.
func newSessionTranscriptRow(harp string) sessionTranscriptRow {
	row := sessionTranscriptRow{Harp: harp}
	path, err := paths.ResolveHarpCanonicalTranscriptPath(harp)
	if err != nil {
		return row
	}
	info, statErr := os.Stat(path)
	if statErr != nil || !info.Mode().IsRegular() {
		return row
	}
	row.Captured = true
	row.Bytes = info.Size()
	row.Path = path
	return row
}

// sessionEntriesForHarpArg resolves the positional-harp arity every leaf under
// this noun shares: no argument means every session (cwd's project, or all),
// one argument means that session and errors if nothing knows it. An unknown
// harp must fail rather than return an empty listing — an empty result reads
// as "this session has nothing", which is a different and much worse answer
// than "there is no such session".
func sessionEntriesForHarpArg(args []string, all bool) ([]sessions.Entry, error) {
	if len(args) == 1 {
		entry, err := operations.GetSession(args[0])
		if err != nil {
			return nil, err
		}
		if entry == nil {
			return nil, fmt.Errorf("harp not found: %q", args[0])
		}
		return []sessions.Entry{*entry}, nil
	}
	return loadSessionEntries(all)
}

// renderSessionTranscripts is the human render: a table, or the explicit
// empty line a table cannot carry.
func renderSessionTranscripts(w io.Writer, rep sessionTranscriptReport) error {
	if len(rep.Transcripts) == 0 {
		ew := iox.NewErrWriter(w)
		ew.Println("(no sessions)")
		return ew.Err()
	}
	return clifmt.Render(w, rep.Transcripts, clifmt.FormatText)
}
