// Command promptprobe answers one question: when claude opens a MODAL PROMPT,
// does it emit a terminal-protocol marker that ctxloom's injector could watch
// for before writing?
//
// WHY THIS EXISTS. The wake injects a frame plus a CR into the engine's stdin.
// A CR delivered while a modal is open is consumed as "confirm the highlighted
// option" — it can answer a decision the human never made, and did so on
// 2026-08-31, selecting an option in a design ruling.
//
// The guard must key on a PROTOCOL marker, never on rendered text. Matching a
// prompt's glyphs would couple the guard to a TUI that redraws itself, and it
// would rot silently — and rotting here means quietly resuming injection into
// prompts. ESC[?2004h/l is defined by the terminal spec and is emitted
// deliberately to tell the terminal how to treat input, so it has a contract
// behind it.
//
// Throwaway harness. Owns both ends of a pty, so no human terminal is involved.
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

const (
	pasteOn  = "\x1b[?2004h"
	pasteOff = "\x1b[?2004l"
	curShow  = "\x1b[?25h"
	curHide  = "\x1b[?25l"
)

// cursorState reports the LAST cursor-visibility transition in s. A selection
// modal has no text caret and hides the cursor; a composer accepting free text
// shows it. That is the property the injector actually needs to know, and
// unlike matching a prompt's glyphs it is defined by the terminal spec.
func cursorState(s string) string {
	h, l := strings.LastIndex(s, curShow), strings.LastIndex(s, curHide)
	switch {
	case h < 0 && l < 0:
		return "NEITHER"
	case h > l:
		return "VISIBLE (text caret — composer)"
	default:
		return "HIDDEN  (no caret — modal/selection)"
	}
}

type term struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	lastWrite time.Time
}

func (t *term) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	t.lastWrite = time.Now()
	return len(p), nil
}

func (t *term) snap() string { t.mu.Lock(); defer t.mu.Unlock(); return t.buf.String() }
func (t *term) mark() int    { t.mu.Lock(); defer t.mu.Unlock(); return t.buf.Len() }

func (t *term) since(m int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.buf.String()
	if m > len(s) {
		return ""
	}
	return s[m:]
}

func (t *term) idle() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastWrite.IsZero() {
		return 0
	}
	return time.Since(t.lastWrite)
}

func (t *term) waitIdle(quiet, bound time.Duration) bool {
	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		if t.idle() >= quiet {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// transitions lists the ESC[?2004 set/reset events in order of appearance.
func transitions(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		h := strings.Index(s[i:], pasteOn)
		l := strings.Index(s[i:], pasteOff)
		switch {
		case h < 0 && l < 0:
			return out
		case l < 0 || (h >= 0 && h < l):
			out = append(out, "ON")
			i += h + len(pasteOn)
		default:
			out = append(out, "OFF")
			i += l + len(pasteOff)
		}
	}
	return out
}

func endState(s string) string {
	h, l := strings.LastIndex(s, pasteOn), strings.LastIndex(s, pasteOff)
	switch {
	case h < 0 && l < 0:
		return "NEITHER (mode 2004 never set)"
	case h > l:
		return "ON  (bracketed paste enabled)"
	default:
		return "OFF (bracketed paste disabled)"
	}
}

func main() {
	dir := flag.String("dir", ".", "working directory for claude")
	settle := flag.Duration("settle", 25*time.Second, "startup observation window")
	after := flag.Duration("after", 45*time.Second, "how long to watch after the trigger")
	model := flag.String("model", "claude-haiku-4-5-20251001", "model to drive")
	trigger := flag.String("trigger", "Please run the shell command `ls` for me.",
		"a prompt expected to open a permission modal")
	flag.Parse()

	t := &term{}
	// Deliberately NOT --dangerously-skip-permissions: the permission modal is
	// the thing being measured, and it is a far more deterministic trigger than
	// persuading the model to call a question tool.
	cmd := exec.Command("claude", "--model", *model)
	cmd.Dir = *dir
	// SCRUB THE INHERITED SESSION MARKERS. This harness runs inside a claude
	// session that itself has bypass permissions; without this the child
	// inherits CLAUDE_CODE_* and never prompts, so the modal under measurement
	// never appears and the run silently measures nothing. HOME is kept because
	// the credential lives there.
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CLAUDE_CODE") || strings.HasPrefix(kv, "CLAUDECODE") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = env
	ptm, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		fmt.Fprintln(os.Stderr, "pty start:", err)
		os.Exit(1)
	}
	defer func() {
		_ = ptm.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	go func() { _, _ = ptm.WriteTo(t) }()

	t.waitIdle(2*time.Second, *settle)
	// Clear the trust dialog UNCONDITIONALLY. Do not try to detect it by text:
	// the rendering is shot through with escape sequences, so a Contains() on
	// the visible words does not match — I tried, and the modal went unnoticed
	// while my own CR selected "No, exit" in it. Down+Enter takes "Yes"; on a
	// composer the same keys are a no-op.
	_, _ = ptm.Write([]byte("\x1b[B"))
	time.Sleep(400 * time.Millisecond)
	_, _ = ptm.Write([]byte("\r"))
	t.waitIdle(2*time.Second, 25*time.Second)
	fmt.Printf("AFTER-TRUST  cursor=%s\n", cursorState(t.snap()))
	startup := t.snap()
	fmt.Printf("STARTUP  2004=%s  cursor=%s  bytes=%d\n",
		endState(startup), cursorState(startup), len(startup))
	_ = os.WriteFile("/tmp/promptprobe-startup.raw", []byte(startup), 0o644)

	m := t.mark()
	_, _ = ptm.Write([]byte(*trigger))
	time.Sleep(400 * time.Millisecond)
	_, _ = ptm.Write([]byte("\r"))

	t.waitIdle(3*time.Second, *after)
	win := t.since(m)
	fmt.Printf("TRIGGER  2004=%s  cursor=%s  bytes=%d\n",
		endState(startup+win), cursorState(startup+win), len(win))

	// Diagnosis only — confirms a modal actually rendered. The guard must NOT
	// depend on any of these strings; that is the coupling this probe exists to
	// avoid.
	fmt.Println("modal actually rendered?")
	for _, needle := range []string{"Do you want", "permission", "❯", "Yes", "No,"} {
		fmt.Printf("  %-14q %v\n", needle, strings.Contains(win, needle))
	}

	const raw = "/tmp/promptprobe-window.raw"
	if err := os.WriteFile(raw, []byte(win), 0o644); err == nil {
		fmt.Println("raw window:", raw)
	}
}
