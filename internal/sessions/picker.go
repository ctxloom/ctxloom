package sessions

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
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
	Entries        []Entry
	In             io.Reader  // line-buffered input source
	Out            io.Writer  // rendering target (typically os.Stderr)
	HorizonCount   int        // 0 → DefaultHorizonCount
	HorizonDays    int        // 0 → DefaultHorizonDays
	Now            func() time.Time // injectable clock for tests; nil → time.Now
	checkSession   []bool     // [s] state per visible row
	checkTasks     []bool     // [t] state per visible row
	revealAll      bool       // toggled by `m` keystroke
	scanner        *bufio.Scanner
	horizonReveal  int        // how many `m` keystrokes have been pressed (additive horizon)
}

// Run loops render → read → handle until a Decision is produced.
func (p *Picker) Run() (Decision, error) {
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
	// Pre-size checkbox state to the full entry slice; visible-row logic
	// indexes into this by entry position, never by row position.
	p.checkSession = make([]bool, len(p.Entries))
	p.checkTasks = make([]bool, len(p.Entries))
	for i, e := range p.Entries {
		p.checkSession[i] = true
		// [t] defaults to true only if there's likely a task snapshot.
		// We don't have direct visibility from here, so be conservative:
		// default-true and let resume logic ignore if there's nothing to
		// restore. Tests cover the "no snapshot exists" case at the call
		// site (run.go).
		p.checkTasks[i] = true
		_ = e
	}

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
	switch low {
	case "q", "quit", "exit":
		return Decision{Action: QuitAction}, actionResolved
	case "n", "new":
		return Decision{Action: NewAction}, actionResolved
	case "m", "more":
		p.horizonReveal++
		return Decision{}, actionLoop
	}

	// Letter-prefix commands: s<N>, t<N>, d<N>[s|t].
	if len(low) >= 2 {
		prefix := low[0]
		rest := strings.TrimSpace(low[1:])
		switch prefix {
		case 's', 't':
			if n, ok := parseRowNumber(rest); ok {
				p.toggleCheck(n, prefix == 's')
				return Decision{}, actionLoop
			}
		case 'd':
			// Distillation is not wired in this commit; surface a hint.
			fmt.Fprintln(p.Out, "(distill not yet available — coming with the compactor revision)")
			return Decision{}, actionLoop
		}
	}

	if n, ok := parseRowNumber(low); ok {
		visible := p.visible()
		if n < 1 || n > len(visible) {
			fmt.Fprintf(p.Out, "row %d out of range (1..%d)\n", n, len(visible))
			return Decision{}, actionLoop
		}
		entryIdx := visible[n-1]
		return Decision{
			Action:         ResumeAction,
			FromHarp:       p.Entries[entryIdx].HarpName,
			RestoreSession: p.checkSession[entryIdx],
			RestoreTasks:   p.checkTasks[entryIdx],
		}, actionResolved
	}

	fmt.Fprintf(p.Out, "unrecognized input: %q. Try a row number, n, m, s<N>, t<N>, or q.\n", line)
	return Decision{}, actionLoop
}

// toggleCheck flips the [s] (session) or [t] (tasks) box on visible row n
// (1-based). isSession=true toggles [s]; false toggles [t].
func (p *Picker) toggleCheck(n int, isSession bool) {
	visible := p.visible()
	if n < 1 || n > len(visible) {
		fmt.Fprintf(p.Out, "row %d out of range (1..%d)\n", n, len(visible))
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

// render writes the current picker state to p.Out.
func (p *Picker) render() {
	visible := p.visible()
	fmt.Fprintln(p.Out, "")
	for i, entryIdx := range visible {
		e := p.Entries[entryIdx]
		fmt.Fprintf(p.Out, " [%d] [s%s] [t%s] %s   %s\n",
			i+1,
			checkbox(p.checkSession[entryIdx]),
			checkbox(p.checkTasks[entryIdx]),
			e.HarpName,
			e.StartedAt.Local().Format("2006-01-02 15:04"),
		)
		if e.Summary != "" {
			fmt.Fprintf(p.Out, "       session: %s\n", e.Summary)
		} else {
			fmt.Fprintf(p.Out, "       session: (no summary)\n")
		}
	}
	if len(visible) < len(p.Entries) {
		fmt.Fprintf(p.Out, "\n  (%d older sessions hidden; press m to reveal)\n", len(p.Entries)-len(visible))
	}
	fmt.Fprintln(p.Out, "")
	fmt.Fprintf(p.Out, "Choose [1-%d] resume · n new · s<N>/t<N> toggle · m more · q quit\n> ", len(visible))
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
