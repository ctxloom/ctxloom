//go:build acceptance

// DURABLE HOLDS ACROSS A COORDINATOR RESTART (j002100).
//
// A hold (a child parked on its rate limit) and a pause (the human's) are the
// coordinator's own state, journaled in runs.jsonl. These steps kill the
// session owner's `ctxloom run` — the process that hosts the coordinator — the
// way a crash would (SIGKILL: no drain; a host child's runner dies with it, by
// its parent-death signal), resume the session with `ctxloom run --session`,
// and read the result where a user would: the roster tool, and the journal.
//
// The human's pause is the OVERLAY's (Ctrl-] in the owner's terminal, select
// the child, `p`): the only surface that controls a child as the human.
package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// j002100Hold is one roster entry's hold, as the roster tool returns it.
type j002100Hold struct {
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	UntilUnix int64  `json:"until_unix,string"`
}

// j002100RosterHold reads the roster tool and returns harp's hold (nil when
// harp carries none, or is not on the roster).
func j002100RosterHold(c context.Context, harp string) (*j002100Hold, error) {
	if err := callTool(c, "roster", map[string]any{"include_terminal": true}); err != nil {
		return nil, err
	}
	w := worldFrom(c)
	if w.lastInnerErr != nil {
		return nil, fmt.Errorf("roster result could not be unwrapped: %v; result:\n%s", w.lastInnerErr, w.lastTool.JSON())
	}
	raw, err := json.Marshal(w.lastInner["runs"])
	if err != nil {
		return nil, err
	}
	var runs []struct {
		Agent struct {
			AgentID string `json:"agent_id"`
		} `json:"agent"`
		Hold *json.RawMessage `json:"hold"`
	}
	if err := json.Unmarshal(raw, &runs); err != nil {
		return nil, fmt.Errorf("decode the roster's runs: %w; result:\n%s", err, w.lastTool.JSON())
	}
	for _, r := range runs {
		if r.Agent.AgentID != harp || r.Hold == nil {
			continue
		}
		var h j002100Hold
		if err := json.Unmarshal(*r.Hold, &h); err != nil {
			// until_unix is an int64, which protojson renders as a string —
			// or omits at zero; accept a bare number too.
			var n struct {
				Kind      string `json:"kind"`
				Source    string `json:"source"`
				UntilUnix int64  `json:"until_unix"`
			}
			if err2 := json.Unmarshal(*r.Hold, &n); err2 != nil {
				return nil, fmt.Errorf("decode %s's hold: %w", harp, err)
			}
			h = j002100Hold(n)
		}
		return &h, nil
	}
	return nil, nil
}

// j002100AwaitHold polls the roster until want reports true for harp's hold.
func j002100AwaitHold(c context.Context, harp string, within time.Duration, want func(*j002100Hold) bool, what string) error {
	deadline := time.Now().Add(within)
	var last *j002100Hold
	for {
		h, err := j002100RosterHold(c, harp)
		if err != nil {
			return err
		}
		if want(h) {
			return nil
		}
		last = h
		if time.Now().After(deadline) {
			return fmt.Errorf("within %s the roster never showed %s %s (last hold: %+v; roster:\n%s\nterminals:\n%s)", within, harp, what, last, worldFrom(c).lastTool.JSON(), j002100Terminals(worldFrom(c)))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// j002100Harp is name's remembered harp.
func j002100Harp(w *World, name string) (string, error) {
	harp, ok := j002100Of(w).harps[name]
	if !ok || harp == "" {
		return "", fmt.Errorf("j002100: no session harp remembered for %q", name)
	}
	return harp, nil
}

// resumeSessionOwner stands the owner again on its OWN session (`ctxloom run
// --session <harp>`) after the previous one died, and reads the endpoint this
// launch minted: the delivered engine config that appeared with it (the
// session-layout sweep, not the test, decides what the dead launch left).
func (w *World) resumeSessionOwner(harp string) error {
	before, err := deliveredConfigs(w.env.HomeDir)
	if err != nil {
		return err
	}
	sess, err := w.env.RunPTYFrom(w.env.AppBinary, 100, 30, []string{"CTXLOOM_MOCK_ECHO_STDIN=1"},
		"run", "--llm", sessionOwnerLabel, "--session", harp, "-f", sessionOwnerFragment)
	if err != nil {
		return fmt.Errorf("resume the session owner: %w", err)
	}
	owner := &sessionOwner{sess: sess, harp: harp}
	w.owner = owner
	if _, err := sess.Write([]byte(sessionOwnerSentinel + "\n")); err != nil {
		return fmt.Errorf("resumed session owner: type the readiness sentinel: %w", err)
	}
	if !sess.WaitForOutput(sessionOwnerReadyTimeout, func(out string) bool {
		return strings.Contains(out, "mock echo: "+sessionOwnerSentinel)
	}) {
		return fmt.Errorf("resumed session owner never echoed %q within %s; output:\n%s", sessionOwnerSentinel, sessionOwnerReadyTimeout, sess.Output())
	}
	after, err := deliveredConfigs(w.env.HomeDir)
	if err != nil {
		return err
	}
	var fresh []string
	for p, at := range after {
		if was, ok := before[p]; !ok || at.After(was) {
			fresh = append(fresh, p)
		}
	}
	if len(fresh) != 1 {
		return fmt.Errorf("expected the resumed launch to deliver exactly one engine config, found %v (all: %v)", fresh, after)
	}
	owner.endpoint, err = readEndpointFile(fresh[0])
	return err
}

// deliveredConfigs is every delivered mock MCP config in the session store,
// with when it was written.
func deliveredConfigs(home string) (map[string]time.Time, error) {
	want := string(filepath.Separator) + filepath.Join(mock.ConfigDirName, mockMCPFileName)
	out := map[string]time.Time{}
	err := filepath.WalkDir(filepath.Join(home, filepath.FromSlash(harpSessionsRel)), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, want) {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out[p] = fi.ModTime()
		return nil
	})
	return out, err
}

func registerJ002100HoldSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the agent calls tool "agent_run" for "([^"]*)" with a briefing that hits a rate limit resetting in (\d+) seconds$`,
		func(c context.Context, role string, secs int) error {
			resets := time.Now().Add(time.Duration(secs) * time.Second).Unix()
			return callTool(c, "agent_run", map[string]any{
				"role": role,
				// No worktree: this journey's repository has no commit to
				// branch one from, and isolation is not what is under test.
				"input": map[string]any{"prompt": fmt.Sprintf("do the work mock:rate-limited=%d", resets), "workspace": "none"},
			})
		})

	ctx.Step(`^the agent calls tool "agent_run" for "([^"]*)" with a briefing whose credential is refused$`,
		func(c context.Context, role string) error {
			return callTool(c, "agent_run", map[string]any{
				"role":  role,
				"input": map[string]any{"prompt": "do the work mock:credential-rejected", "workspace": "none"},
			})
		})

	ctx.Step(`^within (\d+)s the roster shows "([^"]*)" held with kind "([^"]*)"$`,
		func(c context.Context, secs int, name, kind string) error {
			harp, err := j002100Harp(worldFrom(c), name)
			if err != nil {
				return err
			}
			return j002100AwaitHold(c, harp, time.Duration(secs)*time.Second,
				func(h *j002100Hold) bool { return h != nil && h.Kind == kind }, "held with kind "+kind)
		})

	ctx.Step(`^the roster shows "([^"]*)" held with kind "([^"]*)"$`, func(c context.Context, name, kind string) error {
		harp, err := j002100Harp(worldFrom(c), name)
		if err != nil {
			return err
		}
		h, err := j002100RosterHold(c, harp)
		if err != nil {
			return err
		}
		if h == nil || h.Kind != kind {
			return fmt.Errorf("the roster shows %s's hold as %+v, want kind %q; roster:\n%s", name, h, kind, worldFrom(c).lastTool.JSON())
		}
		return nil
	})

	// Released means released: gone, and not before the deadline the hold
	// showed. A held child whose run ended still shows its hold (the hold
	// covers the harp), so a missing hold is a release, which the journal
	// step after this one attributes to the backoff.
	ctx.Step(`^within (\d+)s the roster shows "([^"]*)" released on time$`, func(c context.Context, secs int, name string) error {
		w := worldFrom(c)
		harp, err := j002100Harp(w, name)
		if err != nil {
			return err
		}
		until := time.Unix(j002100Of(w).holdUntil, 0)
		if err := j002100AwaitHold(c, harp, time.Duration(secs)*time.Second,
			func(h *j002100Hold) bool { return h == nil }, "with no hold"); err != nil {
			return err
		}
		if now := time.Now(); now.Before(until) {
			return fmt.Errorf("%s's hold was gone at %s, before its deadline %s; %s; terminals:\n%s\nresumed owner output (tail):\n%q", name, now.Format(time.RFC3339), until.Format(time.RFC3339),
				j002100Of(w).restartDiag, j002100Terminals(w), tail(w.owner.sess.Output(), 4000))
		}
		return nil
	})

	ctx.Step(`^"([^"]*)"'s hold deadline is remembered$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		harp, err := j002100Harp(w, name)
		if err != nil {
			return err
		}
		h, err := j002100RosterHold(c, harp)
		if err != nil {
			return err
		}
		if h == nil || h.UntilUnix == 0 {
			return fmt.Errorf("%s carries no hold with a deadline to remember (%+v)", name, h)
		}
		j002100Of(w).holdUntil = h.UntilUnix
		return nil
	})

	ctx.Step(`^"([^"]*)"'s hold deadline is unchanged$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		harp, err := j002100Harp(w, name)
		if err != nil {
			return err
		}
		h, err := j002100RosterHold(c, harp)
		if err != nil {
			return err
		}
		if h == nil || h.UntilUnix != j002100Of(w).holdUntil {
			return fmt.Errorf("%s's hold is %+v after the restart; its deadline was %d before it", name, h, j002100Of(w).holdUntil)
		}
		return nil
	})

	// The human pauses a child the way a human does: the overlay in the
	// owner's own terminal (Ctrl-]), the child's row selected, `p`.
	ctx.Step(`^the human pauses "([^"]*)" from the overlay$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		harp, err := j002100Harp(w, name)
		if err != nil {
			return err
		}
		if w.owner == nil {
			return errors.New("no session owner is standing to open the overlay in")
		}
		return overlayPause(w.owner.sess, harp)
	})

	// A crash, not a shutdown: SIGKILL ends the process that hosts the
	// coordinator with no drain (its host runners die with it: they carry a
	// parent-death signal); then the session is resumed and the agent's MCP
	// session re-dialed to the endpoint the resumed launch minted.
	ctx.Step(`^the session's coordinator dies and the session is resumed$`, func(c context.Context) error {
		w := worldFrom(c)
		if w.owner == nil {
			return errors.New("no session owner is standing to kill")
		}
		harp := w.owner.harp
		runners := testenv.RunnerChildrenOf(w.owner.sess.PID())
		host, err := os.FindProcess(w.owner.sess.PID())
		if err == nil {
			err = host.Kill()
		}
		if err != nil {
			return fmt.Errorf("kill the session owner: %w", err)
		}
		if exited, _ := w.owner.sess.Wait(15 * time.Second); !exited {
			return errors.New("the killed session owner never exited")
		}
		if w.mcp != nil {
			_ = w.mcp.Close() // its endpoint died with the owner
			w.mcp = nil
		}
		w.owner.stop()
		w.owner = nil
		err = w.resumeSessionOwner(harp)
		var alive []string
		for _, pid := range runners {
			p, ferr := os.FindProcess(pid)
			alive = append(alive, fmt.Sprintf("%d:%v", pid, ferr == nil && p.Signal(syscall.Signal(0)) == nil))
		}
		j002100Of(w).restartDiag = fmt.Sprintf("the killed owner's runner children (pid:alive after resume): %v", alive)
		return err
	})

	ctx.Step(`^the journal records "([^"]*)"'s hold released by its own backoff$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		raw, err := j002100JournalRaw(w)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
			var f struct {
				Kind string `json:"kind"`
				Data struct {
					Cause string `json:"cause"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(line), &f) == nil && f.Kind == "hold.released" && f.Data.Cause == "backoff" {
				w.docStepMaterialized = "runs.jsonl — " + line
				return nil
			}
		}
		return fmt.Errorf("runs.jsonl records no hold.released with cause backoff for %s; journals:\n%s", name, j002100Terminals(w))
	})
}

// j002100Terminals is every run.ended and hold fact in every coordinator
// journal, for a failure that must say what became of a child.
func j002100Terminals(w *World) string {
	files, err := filepath.Glob(filepath.Join(w.env.HomeDir, ".ctxloom", "coord", "*", "*", "runs.jsonl"))
	if err != nil {
		return err.Error()
	}
	var out []string
	for _, f := range files {
		out = append(out, "== "+f)
		raw, err := os.ReadFile(f)
		if err != nil {
			out = append(out, err.Error())
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, `"kind":"run.ended"`) || strings.Contains(line, `"kind":"hold.`) {
				out = append(out, line)
			}
		}
	}
	return strings.Join(out, "\n")
}

// tail is the last n bytes of s.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// overlayPause opens the overlay in sess, selects harp's row and pauses it,
// waiting on the overlay's own confirmation, then closes the overlay. Every
// wait reads the screen (testenv.ScreenText) and never the bytes: the renderer
// redraws only the cells that changed, so a confirmation drawn over the
// pending hint ("pausing <harp>…") reuses the cells the two share and never
// appears whole in the stream. The screen is replayed from Ctrl-], so every
// cell the renderer skips was drawn inside the replay. The row is found by
// its position in the first render (the selection starts on the first row).
func overlayPause(sess *testenv.PTYSession, harp string) error {
	origin := len(sess.Output())
	since := func(out string) string { return out[min(origin, len(out)):] }
	waitFor := func(what string) error {
		if !sess.WaitForOutput(15*time.Second, func(out string) bool {
			return strings.Contains(testenv.ScreenText(since(out)), what)
		}) {
			out := since(sess.Output())
			return fmt.Errorf("the overlay never showed %q; screen since Ctrl-]:\n%s\noutput since Ctrl-]:\n%q", what, testenv.ScreenText(out), out)
		}
		return nil
	}
	if _, err := sess.Write([]byte{0x1d}); err != nil {
		return err
	}
	if err := waitFor("j/k move"); err != nil {
		return err
	}
	if err := waitFor(harp + "·"); err != nil {
		return err
	}
	first := testenv.ScreenText(since(sess.Output()))
	row := overlayRow(first, harp)
	if row < 0 {
		return fmt.Errorf("could not find %s's row in the overlay's roster:\n%s", harp, first)
	}
	if row > 0 {
		if _, err := sess.Write([]byte(strings.Repeat("j", row))); err != nil {
			return err
		}
		if err := waitFor(harp + " ("); err != nil {
			return err
		}
	}
	if _, err := sess.Write([]byte("p")); err != nil {
		return err
	}
	if err := waitFor("paused " + harp); err != nil {
		return err
	}
	_, err := sess.Write([]byte("q"))
	return err
}

// overlayRow is harp's row index in the overlay's roster pane on screen
// (testenv.ScreenText): its rows are the lines whose pane (left of "│") names a harp and
// its role ("harp·role"); -1 when harp is not among them.
func overlayRow(screen, harp string) int {
	n := 0
	for _, line := range strings.Split(screen, "\n") {
		pane, _, isRow := strings.Cut(line, "│")
		if !isRow || !strings.Contains(pane, "·") {
			continue
		}
		if strings.Contains(pane, harp+"·") {
			return n
		}
		n++
	}
	return -1
}
