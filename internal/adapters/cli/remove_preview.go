package cli

import (
	"fmt"
	"io"
)

// Every `remove` leaf (bundle, fragment, command, profile, agent, remote, mcp
// server) shares one safety posture: bare invocation REPORTS what it would
// destroy and destroys NOTHING, exiting 0 — the report is the command's
// semantics, not a refusal. Only --yes applies it. This file is the one place
// that posture is rendered, so all seven leaves say it identically instead of
// drifting across seven hand-written messages.

// removePreviewResult is the --format json payload for a `remove` command's
// bare (report-only) path. Applied is always false here — a machine reader
// checking this field never mistakes a preview for a completed removal, the
// same distinction printRemovePreview draws in text.
type removePreviewResult struct {
	Status  string   `json:"status"`
	Applied bool     `json:"applied"`
	Target  string   `json:"target"`
	Detail  []string `json:"detail,omitempty"`
	Apply   string   `json:"apply"`
}

// newRemovePreviewResult builds the JSON payload for a bare `remove`: always
// Status "preview", Applied false.
func newRemovePreviewResult(target string, detail []string, applyCmd string) removePreviewResult {
	return removePreviewResult{Status: "preview", Applied: false, Target: target, Detail: detail, Apply: applyCmd}
}

// printRemovePreview writes the bare `remove` report to out: what would be
// destroyed, then — as the LAST thing printed, after the detail, never a
// header a long list can scroll past — an unmissable statement that nothing
// was removed plus the exact command that applies it. A user who only reads
// the last line still learns the truth: nothing happened, and here is how to
// make it happen.
func printRemovePreview(out io.Writer, target string, detail []string, applyCmd string) {
	if len(detail) == 0 {
		fmt.Fprintf(out, "Would remove %s.\n", target)
	} else {
		fmt.Fprintf(out, "Would remove %s:\n", target)
		for _, d := range detail {
			fmt.Fprintf(out, "  %s\n", d)
		}
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Nothing was removed. Re-run with --yes to apply:")
	fmt.Fprintf(out, "  %s\n", applyCmd)
}
