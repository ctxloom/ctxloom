package cli

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// SessionFullRow is the `--full` projection `session list` and `session
// query` render on request: SessionRow's lean metadata plus the session's
// complete distilled essence body, which SessionRow itself deliberately
// omits (see session_row.go). Embedding SessionRow rather than duplicating
// its fields means clifmt's reflective json/yaml/toml/markdown renderers
// (which walk reflect.VisibleFields, promoting embedded fields) see exactly
// SessionRow's fields plus Essence, so the two shapes can never drift apart.
type SessionFullRow struct {
	SessionRow
	// Essence is the complete essence.md body, "" when the session was never
	// distilled (EssencePath is "" too in that case — nothing else to check).
	Essence string `json:"essence" label:"Essence"`
}

// newSessionFullRow builds a SessionFullRow for e: the same projection
// newSessionRow builds, plus the session's essence body read straight off
// disk via readSessionEssence (the same resolution `session show` uses —
// harp-dir layout first, then the legacy per-session-id path).
func newSessionFullRow(e sessions.Entry, appDir string) SessionFullRow {
	row := newSessionRow(e, appDir)
	essence, _ := readSessionEssence(e.HarpName, &e)
	return SessionFullRow{SessionRow: row, Essence: essence}
}

// renderSessionFullText is the human (text/markdown) body for `--full`
// output: one block per session — harp, summary, start/end, essence path,
// then the complete essence body — rather than clifmt's reflective table,
// which packs each struct field into a table cell and has no good way to
// lay out a multi-paragraph essence body. json/yaml/toml skip this path
// entirely (see emitSessionRows) and go straight through clifmt.Render,
// which handles SessionFullRow's embedded-struct shape natively.
func renderSessionFullText(w io.Writer, rows []SessionFullRow) error {
	ew := iox.NewErrWriter(w)
	if len(rows) == 0 {
		ew.Println("(no sessions)")
		return ew.Err()
	}
	for i, r := range rows {
		if i > 0 {
			ew.Println()
			ew.Println("────────────────────────────────────────")
			ew.Println()
		}
		ew.Printf("# %s\n", r.Harp)
		ew.Printf("%s\n", r.Summary)
		ew.Printf("Start: %s\n", r.Start.String())
		if r.End != nil {
			ew.Printf("End: %s\n", r.End.String())
		}
		if r.EssencePath != "" {
			ew.Printf("Essence: %s\n", r.EssencePath)
		}
		ew.Println()
		if r.Essence != "" {
			ew.Printf("%s\n", r.Essence)
		} else {
			ew.Println("(not distilled yet)")
		}
	}
	return ew.Err()
}

// emitSessionRows is the shared render tail for `session list` and `session
// query`: the lean SessionRow projection by default, or — when full is true
// — the FULL projection carrying each session's complete essence body.
//
// FULL text/markdown output is routed through pagerWriter, which only ever
// actually pages when the command is writing to a real, TTY-attached
// os.Stdout (shouldPage); every other case — a test's captured buffer, a
// redirected file, a pipe into jq — passes the writer through unchanged.
// Structured formats (json/yaml/toml) never reach pagerWriter at all: they
// return straight out of clifmt.Render, so `--full --format json | jq` stays
// exactly as pipeable as it was before `--full` existed.
func emitSessionRows(cmd *cobra.Command, entries []sessions.Entry, full bool, appDir string) error {
	if !full {
		rows := make([]SessionRow, len(entries))
		for i, e := range entries {
			rows[i] = newSessionRow(e, appDir)
		}
		return emit(cmd, rows, func() error {
			return renderSessionRows(cmd.OutOrStdout(), rows)
		})
	}

	fullRows := make([]SessionFullRow, len(entries))
	for i, e := range entries {
		fullRows[i] = newSessionFullRow(e, appDir)
	}

	format, err := cliemit.Resolve(cmd)
	if err != nil {
		return err
	}
	// U104-F01's runtime guard (format.go) only sees emit()/outputFormatOf;
	// both branches below are the direct clifmt.Render/renderSessionFullText
	// bypass U104-F05 already names as a hand-rolled duplicate of emit()'s own
	// format branch, so this marks the guard on their behalf.
	formatWasHonored = true
	if format != clifmt.FormatText && format != clifmt.FormatMarkdown {
		return clifmt.Render(cmd.OutOrStdout(), fullRows, format)
	}

	out := cmd.OutOrStdout()
	pw, cleanup := pagerWriter(out)
	renderErr := renderSessionFullText(pw, fullRows)
	if cleanupErr := cleanup(); cleanupErr != nil && renderErr == nil {
		renderErr = cleanupErr
	}
	return renderErr
}
