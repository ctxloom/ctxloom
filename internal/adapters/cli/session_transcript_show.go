package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// errNoCapturedTranscript refuses a dump of a session ctxloom holds no
// transcript for and could not capture one from its engine's log.
var errNoCapturedTranscript = errors.New("no captured transcript")

var sessionTranscriptShowCmd = &cobra.Command{
	Use:   "show <session-name>",
	Short: "Print a session's whole transcript, then exit",
	Long: `Prints every turn of a session's transcript in order and exits, so it can be
redirected to a file or piped. The transcript is refreshed from the engine's
log first; a refresh that fails is warned on stderr and the session's copy is
printed. Nothing but the transcript is written to stdout.

The output is watch's: text pretty-prints each turn, draws a rule closing
each one and marks a subagent's entries with "↳"; --format json is watch's
NDJSON, an {"entry": {...}} line per turn and a {"boundary": {...}} line
closing each one, so one jq filter reads both. Off a terminal the default is
the configured non_tty_format (json unless set).

It errors when ctxloom holds no transcript for the session and its engine's
log yields none.`,
	Example: `  ctxloom session transcript show amber-swift-owl --format text > session.txt
  ctxloom session transcript show amber-swift-owl --format json | jq -r 'select(.entry.type == "user") | .entry.content'`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionTranscriptShow,
}

// runSessionTranscriptShow refreshes the session, reads its canonical
// transcript once and renders it through watch's renderers as one finished
// feed.
func runSessionTranscriptShow(cmd *cobra.Command, args []string) error {
	harp := args[0]
	format, err := streamFormat(cmd)
	if err != nil {
		return err
	}
	if format != formatText && format != formatJSON {
		return unknownFormatError(string(format))
	}
	entry, err := operations.RefreshForRead(cmd.Context(), safefs.New(), App().Engines(), harp)
	if err != nil {
		return err
	}
	if entry == nil {
		return errNoSession(harp)
	}
	if entry.CanonicalTranscriptPath == "" {
		return fmt.Errorf("session %q: %w", harp, errNoCapturedTranscript)
	}
	sess, err := transcript.ParseTranscriptFile(entry.CanonicalTranscriptPath, harp)
	if err != nil {
		return err
	}
	events, errs := replayFeed(sess.Entries)
	return streamWatchEvents(cmd.OutOrStdout(), format, events, errs)
}

// replayFeed is a whole transcript as a feed that has already ended: every
// event buffered, both channels closed.
func replayFeed(entries []agent.SessionEntry) (<-chan operations.SessionFeedEvent, <-chan error) {
	replayed := transcript.ReplayEvents(entries)
	events := make(chan operations.SessionFeedEvent, len(replayed))
	for _, ev := range replayed {
		events <- operations.SessionFeedEvent{Event: ev}
	}
	close(events)
	errs := make(chan error)
	close(errs)
	return events, errs
}
