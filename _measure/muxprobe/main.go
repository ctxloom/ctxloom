// Command muxprobe measures whether a terminal multiplexer can submit a turn to
// a real claude-code TUI, and — the question that decides the architecture —
// whether it still works while the TUI is BUSY RENDERING.
//
// Why the busy arm is the whole point. ctxloom currently splices bytes into the
// engine's stdin itself and must guess when the terminal is quiet first
// (quietTap, outputGate, terminalInjectSubmitGap). That guessing IS the defect
// filed as shrill-veto, and the gap constant is tingly-frisk. If a multiplexer
// lands a turn mid-burst, none of that machinery is needed; if it does not,
// adopting one buys far less than it appears to.
//
// Method is inherited deliberately from the sibling ptyprobe: success is
// detected MECHANICALLY by a DERIVED nonce. The prompt carries two fragments
// SEPARATED, and only a model that actually ran can emit them JOINED — so
// "a turn started" stays distinguishable from "text is staged in the input
// box", which is the exact confusion this investigation keeps meeting.
//
// Throwaway measurement harness. The multiplexer owns the pty, so unlike
// ptyprobe this program needs none of its own.
//
// KNOWN LIMITS OF THIS INSTRUMENT — read before believing a number it prints:
//
//  1. IT CANNOT SEE A TURN BOUNDARY. awaitToken only asks whether the nonce
//     EVENTUALLY appeared, so a turn that was QUEUED behind an in-flight turn
//     is indistinguishable from one accepted mid-render. It was read the wrong
//     way once: the burst arm was reported as "submits mid-render" when
//     instrumented re-runs showed the prime turn completing FIRST. If you need
//     that distinction, track the prime's own completion marker and compare.
//  2. IT DROPPED TWO GUARDS ptyprobe HAS, and both produce false data rather
//     than errors. ptyprobe REFUSES when the engine never enabled bracketed
//     paste (i.e. it is not at an interactive prompt — `claude
//     --dangerously-skip-permissions` stops at a "Bypass Permissions mode"
//     modal, and waitQuiet here is purely size-based so it calls that modal
//     "settled" and runs the whole trial against a dialog). ptyprobe also
//     marks a log offset and matches only AFTER it; this file matches against
//     the whole accumulated log.
//  3. n=3 IS UNDERPOWERED for retiring a defence: 3/3 leaves the 95% lower
//     bound on the success rate at ~0.37.
//
// Run: go run ./_measure/muxprobe -mux tmux -dir /path/to/repo
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// nonceFor is ptyprobe's criterion, kept identical on purpose: the fragments
// are separated in the prompt and joined in the required answer, so text merely
// rendered in the input box cannot forge a pass.
func nonceFor(i int) (prompt, want string) {
	a := fmt.Sprintf("Q%02dZ", i)
	b := fmt.Sprintf("K%02dM", i)
	prompt = fmt.Sprintf("Reply with exactly one word and nothing else: the fragment %s followed immediately by the fragment %s .", a, b)
	return prompt, a + b
}

// stripANSI drops CSI/OSC sequences and control bytes. It does not rebuild the
// screen — absolute cursor moves make that impossible — but one contiguous
// token survives, which is all the nonce needs.
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
				for j < len(s) && s[j] != 0x07 && s[j] != 0x1b {
					j++
				}
				i = j + 1
				continue
			}
		}
		if s[i] >= 0x20 || s[i] == '\n' {
			b.WriteByte(s[i])
		}
		i++
	}
	return b.String()
}

func sh(bin string, args ...string) (string, error) {
	out, err := exec.Command(bin, args...).CombinedOutput()
	return string(out), err
}

// mux is the multiplexer under test. The two differ in more than spelling:
// tmux can start streaming a live pane with pipe-pane, screen must be told to
// log at creation; and their injection verbs are not the same mechanism —
// send-keys delivers Enter as a KEY EVENT, stuff writes a newline BYTE.
type mux interface {
	name() string
	start(sess, dir, log string) error
	send(sess, text string, gap time.Duration) error
	kill(sess string)
}

type tmuxMux struct{}

func (tmuxMux) name() string { return "tmux" }

func (tmuxMux) start(sess, dir, log string) error {
	_, _ = sh("tmux", "kill-session", "-t", sess)
	if out, err := sh("tmux", "new-session", "-d", "-s", sess, "-x", "200", "-y", "50",
		"-c", dir, "claude", "--dangerously-skip-permissions"); err != nil {
		return fmt.Errorf("new-session: %v: %s", err, out)
	}
	if out, err := sh("tmux", "pipe-pane", "-o", "-t", sess, "cat >> "+log); err != nil {
		return fmt.Errorf("pipe-pane: %v: %s", err, out)
	}
	return nil
}

func (tmuxMux) send(sess, text string, gap time.Duration) error {
	if out, err := sh("tmux", "send-keys", "-t", sess, "-l", text); err != nil {
		return fmt.Errorf("send-keys -l: %v: %s", err, out)
	}
	if gap > 0 {
		time.Sleep(gap)
	}
	if out, err := sh("tmux", "send-keys", "-t", sess, "Enter"); err != nil {
		return fmt.Errorf("send-keys Enter: %v: %s", err, out)
	}
	return nil
}

func (tmuxMux) kill(sess string) { _, _ = sh("tmux", "kill-session", "-t", sess) }

type screenMux struct{}

func (screenMux) name() string { return "screen" }

func (screenMux) start(sess, dir, log string) error {
	_, _ = sh("screen", "-S", sess, "-X", "quit")
	if out, err := sh("screen", "-dmS", sess, "-L", "-Logfile", log,
		"bash", "-lc", "cd "+dir+" && exec claude --dangerously-skip-permissions"); err != nil {
		return fmt.Errorf("screen -dmS: %v: %s", err, out)
	}
	// screen buffers its log; without this the probe observes nothing and would
	// report a false negative.
	_, _ = sh("screen", "-S", sess, "-X", "logfile", "flush", "0")
	return nil
}

func (screenMux) send(sess, text string, gap time.Duration) error {
	if out, err := sh("screen", "-S", sess, "-X", "stuff", text); err != nil {
		return fmt.Errorf("stuff: %v: %s", err, out)
	}
	if gap > 0 {
		time.Sleep(gap)
	}
	// CR (0x0d), NOT LF. An interactive TUI holds the terminal in RAW mode, so
	// the kernel's ICRNL translation is off and the Enter KEY is carriage
	// return; LF lands as literal text that is never submitted.
	//
	// THIS WAS MEASURED WRONG ONCE AND THE NUMBER WAS BELIEVED. The first
	// version sent "\n" here while the tmux arm sent Enter (which tmux
	// delivers as 0x0d), so the two arms differed on the SUBMIT BYTE and
	// screen scored 0/6. That was read as evidence about screen; it was an
	// artifact of this line. Verified at byte level: `stuff $'\r'` emits
	// 41 42 0d, identical to what tmux send-keys Enter produces.
	//
	// The sibling ptyprobe already treats the submit byte as an explicit
	// matrix axis ("\r" | "\n" | "\r\n" | ""), so the project had ALREADY
	// established that this byte is decisive before this file was written.
	if out, err := sh("screen", "-S", sess, "-X", "stuff", "\r"); err != nil {
		return fmt.Errorf("stuff CR: %v: %s", err, out)
	}
	return nil
}

func (screenMux) kill(sess string) { _, _ = sh("screen", "-S", sess, "-X", "quit") }

type session struct {
	name string
	log  string
	m    mux
}

func (s *session) read() string {
	b, err := os.ReadFile(s.log)
	if err != nil {
		return ""
	}
	return stripANSI(string(b))
}

func (s *session) size() int64 {
	fi, err := os.Stat(s.log)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func (s *session) waitQuiet(quiet, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	last := s.size()
	stable := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if n := s.size(); n != last {
			last, stable = n, time.Now()
			continue
		}
		if time.Since(stable) >= quiet {
			return true
		}
	}
	return false
}

// waitBusy blocks until the pane is actively producing output — the state the
// idle-detection machinery exists to avoid injecting into, and the one the
// existing ptyprobe never measures because runTrial waits for quiet first.
func (s *session) waitBusy(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		a := s.size()
		time.Sleep(120 * time.Millisecond)
		if s.size() > a+64 {
			return true
		}
	}
	return false
}

func (s *session) awaitToken(want string, timeout time.Duration) (bool, time.Duration) {
	start := time.Now()
	for time.Since(start) < timeout {
		if strings.Contains(s.read(), want) {
			return true, time.Since(start)
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false, time.Since(start)
}

func start(m mux, name, dir string) (*session, error) {
	log := filepath.Join(os.TempDir(), name+".log")
	_ = os.Remove(log)
	if err := m.start(name, dir, log); err != nil {
		return nil, err
	}
	return &session{name: name, log: log, m: m}, nil
}

func main() {
	which := flag.String("mux", "tmux", "tmux | screen")
	arm := flag.String("arm", "both", "idle | burst | both")
	dir := flag.String("dir", ".", "working directory for claude")
	repeats := flag.Int("repeats", 5, "trials per arm")
	gap := flag.Duration("gap", 0, "gap between text and submit (0 = none)")
	observe := flag.Duration("observe", 90*time.Second, "how long to watch for the answer")
	settle := flag.Duration("settle", 25*time.Second, "startup budget")
	flag.Parse()

	var m mux
	switch *which {
	case "tmux":
		m = tmuxMux{}
	case "screen":
		m = screenMux{}
	default:
		fmt.Fprintf(os.Stderr, "unknown -mux %q\n", *which)
		os.Exit(2)
	}
	if _, err := exec.LookPath(*which); err != nil {
		fmt.Fprintf(os.Stderr, "%s is not installed; this probe cannot run\n", *which)
		os.Exit(2)
	}
	abs, _ := filepath.Abs(*dir)

	arms := []string{"idle", "burst"}
	if *arm != "both" {
		arms = []string{*arm}
	}

	for _, a := range arms {
		pass, valid := 0, 0
		for i := 0; i < *repeats; i++ {
			name := fmt.Sprintf("muxprobe-%s-%s-%d", m.name(), a, i)
			s, err := start(m, name, abs)
			if err != nil {
				fmt.Printf("%-6s %-5s trial %d: START ERROR %v\n", m.name(), a, i, err)
				continue
			}
			if !s.waitQuiet(900*time.Millisecond, *settle) {
				fmt.Printf("%-6s %-5s trial %d: never settled at startup — INVALID, not a failure\n", m.name(), a, i)
				m.kill(name)
				continue
			}

			note := ""
			if a == "burst" {
				if err := m.send(name, "Count from 1 to 400, one number per line, nothing else.", *gap); err != nil {
					fmt.Printf("%-6s %-5s trial %d: burst prime failed: %v\n", m.name(), a, i, err)
					m.kill(name)
					continue
				}
				if !s.waitBusy(45 * time.Second) {
					fmt.Printf("%-6s %-5s trial %d: engine never got busy — INVALID, not a pass\n", m.name(), a, i)
					m.kill(name)
					continue
				}
				note = "  (injected mid-burst)"
			}

			valid++
			prompt, want := nonceFor(i)
			if err := m.send(name, prompt, *gap); err != nil {
				fmt.Printf("%-6s %-5s trial %d: send failed: %v\n", m.name(), a, i, err)
				m.kill(name)
				continue
			}
			ok, took := s.awaitToken(want, *observe)
			if ok {
				pass++
			}
			fmt.Printf("%-6s %-5s trial %d: submitted=%-5v after %-9s want=%s%s\n",
				m.name(), a, i, ok, took.Round(time.Millisecond), want, note)
			m.kill(name)
		}
		fmt.Printf("==> %s ARM %s: %d/%d submitted (%d valid trials, gap=%s)\n\n",
			m.name(), a, pass, valid, valid, *gap)
	}
}
