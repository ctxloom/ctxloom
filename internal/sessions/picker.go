package sessions

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/ctxloom/shared/iox"
)

// Decision is what the picker returns: either resume an existing session,
// start fresh, or quit.
type Decision struct {
	Action         Action
	FromHarp       string // when Action == ResumeAction
	RestoreSession bool   // [s] checkbox state at resume
	RestoreTasks   bool   // [t] checkbox state at resume
}

// Action is the user's chosen disposition.
type Action int

const (
	// NewAction means start a fresh session with no carry-over. Equivalent
	// to typing `n` or having an empty index for this project.
	NewAction Action = iota
	// ResumeAction means resume the named session per Decision.From* fields.
	ResumeAction
	// QuitAction means the user explicitly aborted (`q`); the caller should
	// exit without launching the backend.
	QuitAction
)

// DefaultHorizonCount and DefaultHorizonDays cap the picker's initial
// display: show at most N rows AND at most rows within D days. Whichever
// is more restrictive wins. The `m` keystroke expands beyond the horizon
// in steps; an unbounded horizon (after enough `m` presses) shows everything.
const (
	DefaultHorizonCount = 10
	DefaultHorizonDays  = 7
)

// Picker drives the pre-launch resume UI. The transport is plain
// stderr/stdin — no alt-screen, no raw mode. Decoupled from os.* via
// in/out fields so it's testable end-to-end.
type Picker struct {
	Entries       []Entry
	In            io.Reader        // line-buffered input source
	Out           io.Writer        // rendering target (typically os.Stderr)
	Distill       DistillFunc      // optional: invoked by the `d<N>` keystroke
	Adopt         AdoptFunc        // optional: invoked when a raw (unadopted) row is selected
	OpenIndex     OpenIndexFunc    // optional: index source for the post-distill reload; nil → Open("") (the user-global index)
	HorizonCount  int              // 0 → DefaultHorizonCount
	HorizonDays   int              // 0 → DefaultHorizonDays
	Now           func() time.Time // injectable clock for tests; nil → time.Now
	checkSession  []bool           // [s] state per visible row
	checkTasks    []bool           // [t] state per visible row
	scanner       *bufio.Scanner
	horizonReveal int // how many `m` keystrokes have been pressed (additive horizon)
}

// Run loops render → read → handle until a Decision is produced.
func (p *Picker) Run() (Decision, error) {
	p.applyDefaults()
	p.initCheckboxes()

	// Special case: empty index → fall through to NewAction immediately.
	if len(p.visible()) == 0 {
		return Decision{Action: NewAction}, nil
	}

	for {
		p.render()
		if !p.scanner.Scan() {
			// EOF or non-interactive — fall through to new.
			return Decision{Action: NewAction}, nil
		}
		line := strings.TrimSpace(p.scanner.Text())
		switch dec, kind := p.handle(line); kind {
		case actionResolved:
			return dec, nil
		case actionLoop:
			continue
		}
	}
}

// applyDefaults fills in zero-valued picker configuration.
func (p *Picker) applyDefaults() {
	if p.HorizonCount <= 0 {
		p.HorizonCount = DefaultHorizonCount
	}
	if p.HorizonDays <= 0 {
		p.HorizonDays = DefaultHorizonDays
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.scanner == nil {
		p.scanner = bufio.NewScanner(p.In)
	}
}

// initCheckboxes pre-sizes checkbox state to the full entry slice (visible-row
// logic indexes by entry position, never row position). Both default to true:
// [t] is conservatively on even without direct snapshot visibility — resume
// logic ignores it when there's nothing to restore (covered by run.go tests).
func (p *Picker) initCheckboxes() {
	p.checkSession = make([]bool, len(p.Entries))
	p.checkTasks = make([]bool, len(p.Entries))
	for i := range p.Entries {
		p.checkSession[i] = true
		p.checkTasks[i] = true
	}
}

type handleKind int

const (
	actionResolved handleKind = iota
	actionLoop
)

func (p *Picker) handle(line string) (Decision, handleKind) {
	if line == "" {
		// Empty submission re-renders. Treat as a courtesy refresh.
		return Decision{}, actionLoop
	}
	low := strings.ToLower(line)

	if dec, kind, ok := p.handleWordCommand(low); ok {
		return dec, kind
	}
	if dec, kind, ok := p.handlePrefixCommand(low); ok {
		return dec, kind
	}
	if dec, kind, ok := p.handleRowSelection(low); ok {
		return dec, kind
	}

	p.diag().Printf("unrecognized input: %q. Try a row number, n, m, s<N>, t<N>, or q.\n", line)
	return Decision{}, actionLoop
}

// handleWordCommand handles the word commands (q/quit/exit, n/new, m/more).
func (p *Picker) handleWordCommand(low string) (Decision, handleKind, bool) {
	switch low {
	case "q", "quit", "exit":
		return Decision{Action: QuitAction}, actionResolved, true
	case "n", "new":
		return Decision{Action: NewAction}, actionResolved, true
	case "m", "more":
		p.horizonReveal++
		return Decision{}, actionLoop, true
	}
	return Decision{}, 0, false
}

// handlePrefixCommand handles the letter-prefix commands s<N>, t<N>, d<N>.
func (p *Picker) handlePrefixCommand(low string) (Decision, handleKind, bool) {
	if len(low) < 2 {
		return Decision{}, 0, false
	}
	rest := strings.TrimSpace(low[1:])
	switch low[0] {
	case 's', 't':
		if n, ok := parseRowNumber(rest); ok {
			p.toggleCheck(n, low[0] == 's')
			return Decision{}, actionLoop, true
		}
	case 'd':
		if n, ok := parseRowNumber(rest); ok {
			p.distillRow(n)
			return Decision{}, actionLoop, true
		}
	}
	return Decision{}, 0, false
}

// handleRowSelection resolves a bare row number into a ResumeAction. A row
// backed by an adopted harp resumes that harp directly; a raw (unadopted)
// transcript row is adopted into a new harp first (see selectRaw), then resumed
// — so the downstream resume path distills it like any other harp.
func (p *Picker) handleRowSelection(low string) (Decision, handleKind, bool) {
	n, ok := parseRowNumber(low)
	if !ok {
		return Decision{}, 0, false
	}
	visible := p.visible()
	if n < 1 || n > len(visible) {
		p.diag().Printf("row %d out of range (1..%d)\n", n, len(visible))
		return Decision{}, actionLoop, true
	}
	entryIdx := visible[n-1]
	if isRawEntry(p.Entries[entryIdx]) {
		dec, kind := p.selectRaw(entryIdx)
		return dec, kind, true
	}
	return Decision{
		Action:         ResumeAction,
		FromHarp:       p.Entries[entryIdx].HarpName,
		RestoreSession: p.checkSession[entryIdx],
		RestoreTasks:   p.checkTasks[entryIdx],
	}, actionResolved, true
}

// selectRaw adopts the raw transcript at p.Entries[entryIdx] into a new harp via
// the injected Adopt callback, then resumes from it. On adopt failure the error
// is surfaced inline and the picker keeps looping. RestoreTasks is always false:
// a just-adopted transcript has no harp-scoped tasks to restore.
func (p *Picker) selectRaw(entryIdx int) (Decision, handleKind) {
	e := p.Entries[entryIdx]
	if p.Adopt == nil {
		p.diag().Println("(adopt not available in this context)")
		return Decision{}, actionLoop
	}
	p.diag().Printf("adopting transcript %s...\n", shortSessionID(e.SessionID))
	harp, err := p.Adopt(e.SessionID, e.TranscriptPath)
	if err != nil {
		p.diag().Printf("adopt failed: %v\n", err)
		return Decision{}, actionLoop
	}
	return Decision{
		Action:         ResumeAction,
		FromHarp:       harp,
		RestoreSession: p.checkSession[entryIdx],
		RestoreTasks:   false,
	}, actionResolved
}

// isRawEntry reports whether an entry is a raw (unadopted) backend transcript
// rather than an indexed harp session. Raw entries carry no harp name.
func isRawEntry(e Entry) bool { return e.HarpName == "" }

// DistillFunc shells out to force-distill a harp. Injected from the
// caller (cmd/run.go) so this package stays decoupled from cobra and
// the cmd binary. When nil, the keystroke surfaces a helpful error.
type DistillFunc func(harpName string) error

// AdoptFunc adopts a raw (unindexed) backend transcript into a new harp and
// returns the assigned harp name. Injected from cmd/run.go so this package
// stays decoupled from the session index's write path and the backend registry.
// When nil, selecting a raw row surfaces a helpful message instead.
type AdoptFunc func(sessionID, transcriptPath string) (harpName string, err error)

// OpenIndexFunc returns the index manager the picker reloads entries from
// after a distill. Injectable so tests can point at a hermetic index instead
// of the real user-global one.
type OpenIndexFunc func() (*Manager, error)

// distillRow invokes the picker's distill callback for visible row n
// (1-based). On success the index entry now has a summary, so the
// picker re-render will show it. On failure the error is surfaced
// inline; the picker keeps looping for the user's next keystroke.
func (p *Picker) distillRow(n int) {
	w := p.diag()
	visible := p.visible()
	if n < 1 || n > len(visible) {
		w.Printf("row %d out of range (1..%d)\n", n, len(visible))
		return
	}
	if isRawEntry(p.Entries[visible[n-1]]) {
		w.Println("(select this row to adopt the transcript first; it distills on resume)")
		return
	}
	if p.Distill == nil {
		w.Println("(distill not available in this context)")
		return
	}
	harp := p.Entries[visible[n-1]].HarpName
	w.Printf("distilling %s (this may take a moment)...\n", harp)
	if err := p.Distill(harp); err != nil {
		w.Printf("distill failed: %v\n", err)
		return
	}
	// Reload the entry from disk so the summary surfaces on re-render. A
	// failed reload only means a stale row — warn and keep looping.
	open := p.OpenIndex
	if open == nil {
		open = func() (*Manager, error) { return Open("") }
	}
	mgr, err := open()
	if err != nil {
		w.Printf("reload after distill failed: %v\n", err)
		return
	}
	updated, err := mgr.Find(harp)
	if err != nil {
		w.Printf("reload after distill failed: %v\n", err)
		return
	}
	if updated != nil {
		p.Entries[visible[n-1]] = *updated
	}
}

// toggleCheck flips the [s] (session) or [t] (tasks) box on visible row n
// (1-based). isSession=true toggles [s]; false toggles [t].
func (p *Picker) toggleCheck(n int, isSession bool) {
	visible := p.visible()
	if n < 1 || n > len(visible) {
		w := p.diag()
		w.Printf("row %d out of range (1..%d)\n", n, len(visible))
		return
	}
	entryIdx := visible[n-1]
	if isSession {
		p.checkSession[entryIdx] = !p.checkSession[entryIdx]
	} else {
		p.checkTasks[entryIdx] = !p.checkTasks[entryIdx]
	}
}

// visible returns the indices into p.Entries that should be displayed,
// after applying the horizon cap (count + days) plus any `m`-keystroke
// expansion via p.horizonReveal.
func (p *Picker) visible() []int {
	count := p.HorizonCount + (p.horizonReveal * p.HorizonCount)
	days := p.HorizonDays + (p.horizonReveal * p.HorizonDays)
	now := p.Now()
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	out := make([]int, 0, len(p.Entries))
	for i, e := range p.Entries {
		if len(out) >= count {
			break
		}
		if e.StartedAt.Before(cutoff) {
			continue
		}
		out = append(out, i)
	}
	return out
}

// diag returns a best-effort writer over p.Out for the picker's
// interactive prompts and inline messages. The picker is a transient
// stderr UI whose render/handle methods have no error-return path, so a
// failed terminal write is intentionally dropped: the iox.ErrWriter
// captures it but callers don't inspect Err() (a broken terminal at this
// point isn't something the picker can recover from anyway).
func (p *Picker) diag() *iox.ErrWriter {
	return iox.NewErrWriter(p.Out)
}

// render writes the current picker state to p.Out.
func (p *Picker) render() {
	w := p.diag()
	visible := p.visible()
	w.Println("")
	for i, entryIdx := range visible {
		e := p.Entries[entryIdx]
		if isRawEntry(e) {
			p.renderRawRow(w, i+1, entryIdx, e)
			continue
		}
		w.Printf(" [%d] [s%s] [t%s] %s   %s\n",
			i+1,
			checkbox(p.checkSession[entryIdx]),
			checkbox(p.checkTasks[entryIdx]),
			e.HarpName,
			e.StartedAt.Local().Format("2006-01-02 15:04"),
		)
		if e.Summary != "" {
			w.Printf("       session: %s\n", e.Summary)
		} else {
			w.Printf("       session: (no summary)\n")
		}
	}
	if len(visible) < len(p.Entries) {
		w.Printf("\n  (%d older sessions hidden; press m to reveal)\n", len(p.Entries)-len(visible))
	}
	w.Println("")
	w.Printf("Choose [1-%d] resume · n new · s<N>/t<N> toggle · d<N> distill · m more · q quit\n> ", len(visible))
}

// renderRawRow renders a raw (unadopted) backend transcript row. Tasks have no
// meaning before adoption, so the [t] box is shown as a dash; [s] is honored
// (the adopted session's essence is restored on resume). Selecting the row
// adopts the transcript and resumes from it.
func (p *Picker) renderRawRow(w *iox.ErrWriter, row, entryIdx int, e Entry) {
	w.Printf(" [%d] [s%s] [t-] (unadopted %s transcript)   %s\n",
		row,
		checkbox(p.checkSession[entryIdx]),
		backendLabel(e.Backend),
		e.StartedAt.Local().Format("2006-01-02 15:04"),
	)
	w.Printf("       transcript: %s (select to adopt + distill)\n", shortSessionID(e.SessionID))
}

// backendLabel returns a non-empty backend name for display.
func backendLabel(backend string) string {
	if backend == "" {
		return "backend"
	}
	return backend
}

// shortSessionID abbreviates a backend session UUID for compact display,
// keeping the leading segment that's enough to disambiguate at a glance.
func shortSessionID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8] + "…"
}

func checkbox(checked bool) string {
	if checked {
		return "✓"
	}
	return " "
}

func parseRowNumber(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}
