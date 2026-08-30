// Command ptyprobe measures what byte sequence actually causes an idle
// claude-code TUI to SUBMIT injected input. Throwaway measurement harness.
//
// It owns both ends of a pty: it spawns the real claude binary as a child on
// the slave side and drives the master side itself, so no human terminal is
// needed. Success is detected MECHANICALLY -- the injected prompt asks for a
// DERIVED nonce (a word reversed) that cannot appear merely by rendering the
// prompt in the input box -- so "turn started" is distinguishable from "text
// staged in the input box".
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// The DECSET sequences that decide whether the paste-coalescing theory in
// terminalinject.go can apply at all.
const (
	bracketedPasteOn  = "\x1b[?2004h"
	bracketedPasteOff = "\x1b[?2004l"
	pasteStart        = "\x1b[200~"
	pasteEnd          = "\x1b[201~"
)

// term is a capture of everything the child wrote to the pty, readable
// concurrently with the child still running.
type term struct {
	mu  sync.Mutex
	buf bytes.Buffer
	// lastWrite is when the child last produced output; the harness's idle
	// signal, mirroring quietTap in terminalinject.go.
	lastWrite time.Time
}

func (t *term) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	t.lastWrite = time.Now()
	return len(p), nil
}

func (t *term) snapshot() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

// mark returns the current length, so a later read can look only at bytes
// produced AFTER an injection rather than at the whole scrollback.
func (t *term) mark() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.Len()
}

func (t *term) since(mark int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.buf.String()
	if mark > len(s) {
		return ""
	}
	return s[mark:]
}

func (t *term) idleFor() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastWrite.IsZero() {
		// Never idle before the child has produced ANYTHING. Reporting idle
		// here let trials fire against a TUI that had not drawn yet, which
		// silently converts a startup race into a "no submit" reading.
		return 0
	}
	return time.Since(t.lastWrite)
}

// waitIdle blocks until the child has been silent for quiet, or bound elapses.
// Reports whether real quiet was reached rather than the bound.
func (t *term) waitIdle(quiet, bound time.Duration) bool {
	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		if t.idleFor() >= quiet {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// waitForSince polls bytes produced after mark for needle until bound elapses,
// matching against ANSI-STRIPPED output. Stripping is mandatory, not tidiness:
// claude's TUI positions each word with an absolute-column escape
// (ESC[5Gmanual ESC[12Gmode), so a raw substring match finds nothing.
// Reports whether it matched and how long it took.
func (t *term) waitForSince(mark int, needle string, bound time.Duration) (bool, time.Duration) {
	start := time.Now()
	deadline := start.Add(bound)
	for time.Now().Before(deadline) {
		if strings.Contains(stripANSI(t.since(mark)), needle) {
			return true, time.Since(start)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false, time.Since(start)
}

// stripANSI removes CSI/OSC escape sequences and control bytes, leaving the
// printable text the TUI drew. It does not reconstruct the screen -- absolute
// cursor moves make that impossible -- but a single contiguous TOKEN survives
// intact, which is all the nonce detection needs.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) {
			switch s[i+1] {
			case '[':
				j := i + 2
				for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3f {
					j++
				}
				if j < len(s) {
					j++
				}
				i = j
				continue
			case ']':
				j := i + 2
				for j < len(s) && s[j] != 0x07 && !(s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\') {
					j++
				}
				if j < len(s) && s[j] == 0x1b {
					j++
				}
				if j < len(s) {
					j++
				}
				i = j
				continue
			default:
				i += 2
				continue
			}
		}
		if s[i] == '\r' || s[i] == '\n' || s[i] == '\t' {
			b.WriteByte(' ')
			i++
			continue
		}
		if s[i] < 0x20 {
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// session is one live claude process under a pty the harness owns.
type session struct {
	cmd *exec.Cmd
	ptm *os.File
	out *term
}

func start(dir string, extraArgs ...string) (*session, error) {
	cmd := exec.Command("claude", extraArgs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptm, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return nil, err
	}
	s := &session{cmd: cmd, ptm: ptm, out: &term{}}
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := ptm.Read(buf)
			if n > 0 {
				_, _ = s.out.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return s, nil
}

func (s *session) close() {
	_ = s.ptm.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.cmd.Wait()
}

// writeAll writes one chunk to the pty master as a single write syscall.
func (s *session) writeAll(b string) error {
	_, err := s.ptm.WriteString(b)
	return err
}

// writeSmall writes b as many small writes, to test whether write granularity
// on the producer side changes anything (matrix axis 4).
func (s *session) writeSmall(b string, chunk int, gap time.Duration) error {
	for i := 0; i < len(b); i += chunk {
		end := i + chunk
		if end > len(b) {
			end = len(b)
		}
		if _, err := s.ptm.WriteString(b[i:end]); err != nil {
			return err
		}
		time.Sleep(gap)
	}
	return nil
}

// cellFrom/cellTo bound which cell indices a run executes, so the matrix can
// be split across several bounded invocations instead of one long one.
var cellFrom, cellTo = 0, 9999

// trial is one cell of the matrix: how the frame is framed and written, what
// byte is used to submit it, and how long the two are separated.
type trial struct {
	framing   string        // "raw" | "paste"
	submit    string        // "\r" | "\n" | "\r\n" | "" (none: control)
	gap       time.Duration // producer-side separation between frame and submit
	writeMode string        // "one" | "small" | "type"
}

func (t trial) label() string {
	sub := map[string]string{"\r": "CR", "\n": "LF", "\r\n": "CRLF", "": "none"}[t.submit]
	return fmt.Sprintf("%-5s %-4s gap=%-6s %s", t.framing, sub, t.gap, t.writeMode)
}

// nonceFor builds a prompt whose correct ANSWER token cannot appear merely by
// rendering the prompt: the two fragments are separated in the prompt text and
// only a model that ACTUALLY RAN can emit them joined. This is what makes
// "turn started" distinguishable from "text staged in the input box".
func nonceFor(i int) (prompt, want string) {
	a := fmt.Sprintf("Q%02dZ", i)
	b := fmt.Sprintf("K%02dM", i)
	prompt = fmt.Sprintf("Reply with exactly one word and nothing else: the fragment %s followed immediately by the fragment %s .", a, b)
	return prompt, a + b
}

// runTrial injects one candidate and reports whether a turn actually started.
func runTrial(s *session, t trial, i int, observe time.Duration) (bool, time.Duration, error) {
	if !s.out.waitIdle(700*time.Millisecond, 25*time.Second) {
		return false, 0, fmt.Errorf("engine never went quiet before trial")
	}
	prompt, want := nonceFor(i)
	frame := prompt
	if t.framing == "paste" {
		frame = pasteStart + prompt + pasteEnd
	}
	mark := s.out.mark()

	// "joined" is the counterfactual the whole nudgeReader split exists to
	// avoid: frame and submit in ONE write, hence one read on claude's side.
	// If this submits, the paste-coalescing theory has no case to answer.
	if t.writeMode == "joined" {
		if err := s.writeAll(frame + t.submit); err != nil {
			return false, 0, err
		}
		ok, took := s.out.waitForSince(mark, want, observe)
		return ok, took, nil
	}
	// joinedsmall: frame AND submit in one logical burst, but chunked across
	// many writes -- the realistic shape if paste framing were adopted, since
	// a frame larger than one buffer already spans several reads.
	if t.writeMode == "joinedsmall" {
		if err := s.writeSmall(frame+t.submit, 16, 2*time.Millisecond); err != nil {
			return false, 0, err
		}
		ok, took := s.out.waitForSince(mark, want, observe)
		return ok, took, nil
	}

	switch t.writeMode {
	case "one":
		if err := s.writeAll(frame); err != nil {
			return false, 0, err
		}
	case "small":
		if err := s.writeSmall(frame, 16, 2*time.Millisecond); err != nil {
			return false, 0, err
		}
	case "type":
		// Emulate a human at a keyboard: one byte per write, ~12ms apart.
		if err := s.writeSmall(frame, 1, 12*time.Millisecond); err != nil {
			return false, 0, err
		}
	}

	if t.submit != "" {
		time.Sleep(t.gap)
		if err := s.writeAll(t.submit); err != nil {
			return false, 0, err
		}
	}
	ok, took := s.out.waitForSince(mark, want, observe)
	return ok, took, nil
}

// clearInput empties the input box after a trial that did NOT submit, so a
// later trial cannot inherit staged text. ESC then Ctrl-U (kill line).
func (s *session) clearInput() {
	_ = s.writeAll("\x1b")
	time.Sleep(120 * time.Millisecond)
	for i := 0; i < 6; i++ {
		_ = s.writeAll("\x15")
		time.Sleep(60 * time.Millisecond)
	}
	s.out.waitIdle(500*time.Millisecond, 5*time.Second)
}

func main() {
	mode := flag.String("mode", "dump", "dump | matrix")
	dir := flag.String("dir", ".", "working directory for claude")
	settle := flag.Duration("settle", 12*time.Second, "how long to observe startup in dump mode")
	repeats := flag.Int("repeats", 3, "how many times to run each cell")
	observe := flag.Duration("observe", 20*time.Second, "how long to watch for a turn after injecting")
	only := flag.String("only", "", "run only cells whose label contains this substring")
	from := flag.Int("from", 0, "first cell index to run (inclusive)")
	to := flag.Int("to", 9999, "last cell index to run (inclusive)")
	flag.Parse()
	cellFrom, cellTo = *from, *to

	switch *mode {
	case "dump":
		runDump(*dir, *settle)
	case "matrix":
		runMatrix(*dir, *repeats, *observe, *only)
	default:
		fmt.Fprintln(os.Stderr, "unknown mode:", *mode)
		os.Exit(2)
	}
}

func matrixCells() []trial {
	var cells []trial
	// POSITIVE CONTROL, and the most important cell in the run: bytes written
	// exactly as a human keyboard produces them. If this does not submit, the
	// harness cannot detect submission and every negative below is worthless.
	cells = append(cells, trial{"raw", "\r", 150 * time.Millisecond, "type"})
	// Axis 1+2: submit byte x framing, at a generous gap, one write.
	for _, framing := range []string{"raw", "paste"} {
		for _, sub := range []string{"\r", "\n", "\r\n"} {
			cells = append(cells, trial{framing, sub, 300 * time.Millisecond, "one"})
		}
	}
	// Axis 2: the gap sweep, on the production framing/byte (raw + CR).
	for _, ms := range []int{0, 50, 150, 300, 600, 1000, 2000} {
		cells = append(cells, trial{"raw", "\r", time.Duration(ms) * time.Millisecond, "one"})
	}
	// Axis 4: write granularity at the production gap.
	cells = append(cells, trial{"raw", "\r", 150 * time.Millisecond, "small"})
	cells = append(cells, trial{"raw", "\n", 150 * time.Millisecond, "small"})
	// NEGATIVE CONTROL: frame with no submit at all must never register.
	cells = append(cells, trial{"raw", "", 0, "one"})
	// Appended AFTER the negative control so earlier indices stay stable
	// across runs. These are the counterfactual: no producer-side split at
	// all, frame and submit in a single write.
	cells = append(cells, trial{"raw", "\r", 0, "joined"})
	cells = append(cells, trial{"paste", "\r", 0, "joined"})
	cells = append(cells, trial{"raw", "\r\n", 0, "joined"})
	cells = append(cells, trial{"raw", "\n", 0, "joined"})
	// Fine sweep below 50ms: 50ms already submitted 3/3, so the real
	// threshold is somewhere under it and the production 150ms is margin,
	// not the operative variable.
	for _, ms := range []int{5, 10, 20, 30} {
		cells = append(cells, trial{"raw", "\r", time.Duration(ms) * time.Millisecond, "one"})
	}
	// CONTROL for the paste result: paste framing with NO submit byte. If the
	// end marker alone submitted, the CR would not be what does the work and
	// the whole reading of cell 18 would be wrong.
	cells = append(cells, trial{"paste", "", 0, "one"})
	// Paste framing under chunked writes.
	cells = append(cells, trial{"paste", "\r", 0, "joinedsmall"})
	return cells
}

func runMatrix(dir string, repeats int, observe time.Duration, only string) {
	cells := matrixCells()
	s, err := start(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start:", err)
		os.Exit(1)
	}
	defer s.close()
	if !s.out.waitIdle(1500*time.Millisecond, 90*time.Second) {
		fmt.Fprintln(os.Stderr, "warning: engine never settled at startup")
	}
	// Refuse to measure anything but a live TUI. A directory claude has not
	// been trusted in stops at a "Quick safety check" prompt that never
	// enables bracketed paste -- and every trial run against THAT reads as a
	// clean "NO SUBMIT", which is a false negative that looks like data.
	if !strings.Contains(s.out.snapshot(), bracketedPasteOn) {
		fmt.Fprintln(os.Stderr, "REFUSING: claude never enabled bracketed paste; it is not at an interactive prompt")
		fmt.Fprintf(os.Stderr, "screen was: %.400s\n", stripANSI(s.out.snapshot()))
		os.Exit(1)
	}
	fmt.Printf("bracketed paste enabled by claude at startup: true\n\n")

	fmt.Printf("%-4s %-42s %s\n", "IDX", "CELL", "RESULT")
	i := 0
	for idx, c := range cells {
		if only != "" && !strings.Contains(c.label(), only) {
			continue
		}
		if idx < cellFrom || idx > cellTo {
			continue
		}
		var subs int
		var lat []string
		for r := 0; r < repeats; r++ {
			i++
			ok, took, err := runTrial(s, c, i, observe)
			if err != nil {
				lat = append(lat, "ERR")
				continue
			}
			if ok {
				subs++
				lat = append(lat, fmt.Sprintf("%.1fs", took.Seconds()))
			} else {
				lat = append(lat, "-")
			}
			s.clearInput()
		}
		verdict := "NO SUBMIT"
		if subs == repeats {
			verdict = "SUBMITS"
		} else if subs > 0 {
			verdict = "FLAKY"
		}
		fmt.Printf("%-4d %-42s %-10s %d/%d  [%s]\n", idx, c.label(), verdict, subs, repeats, strings.Join(lat, " "))
	}
	_ = os.MkdirAll("_measure/out", 0o755)
	_ = os.WriteFile("_measure/out/matrix.raw", []byte(s.out.snapshot()), 0o644)
}

func runDump(dir string, settle time.Duration) {
	s, err := start(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start:", err)
		os.Exit(1)
	}
	defer s.close()

	time.Sleep(settle)
	raw := s.out.snapshot()

	_ = os.MkdirAll("_measure/out", 0o755)
	_ = os.WriteFile("_measure/out/startup.raw", []byte(raw), 0o644)

	fmt.Printf("captured %d bytes\n", len(raw))
	fmt.Printf("bracketed paste ENABLE  (ESC[?2004h) present: %v (count=%d)\n",
		strings.Contains(raw, bracketedPasteOn), strings.Count(raw, bracketedPasteOn))
	fmt.Printf("bracketed paste DISABLE (ESC[?2004l) present: %v (count=%d)\n",
		strings.Contains(raw, bracketedPasteOff), strings.Count(raw, bracketedPasteOff))
	fmt.Printf("first ESC[?2004h at byte offset: %d\n", strings.Index(raw, bracketedPasteOn))

	// Report every DECSET/DECRST private mode the TUI set, so the paste answer
	// is read in context rather than in isolation.
	fmt.Println("--- private modes set/reset ---")
	for _, m := range privateModes(raw) {
		fmt.Println("  ", m)
	}
	fmt.Println("--- last 800 bytes, escaped ---")
	tail := raw
	if len(tail) > 800 {
		tail = tail[len(tail)-800:]
	}
	fmt.Printf("%q\n", tail)
}

// privateModes extracts ESC[?<n>h / ESC[?<n>l occurrences with counts.
func privateModes(raw string) []string {
	seen := map[string]int{}
	for i := 0; i+3 < len(raw); i++ {
		if raw[i] != 0x1b || raw[i+1] != '[' || raw[i+2] != '?' {
			continue
		}
		j := i + 3
		for j < len(raw) && ((raw[j] >= '0' && raw[j] <= '9') || raw[j] == ';') {
			j++
		}
		if j < len(raw) && (raw[j] == 'h' || raw[j] == 'l') {
			seen[raw[i+3:j+1]]++
		}
	}
	out := make([]string, 0, len(seen))
	for k, v := range seen {
		out = append(out, fmt.Sprintf("ESC[?%s  x%d", k, v))
	}
	return out
}
