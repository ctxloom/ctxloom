//go:build acceptance

// The owner's woken turn-start mail delivery (`ctxloom hook mail-drain`, the
// @R2 scenario in j002300_cross_engine_delegation.feature), and the F4 guard
// that a delegated child's spool is never claimed by that hook.
//
// Every assertion here reads one of two things: the session owner's
// TERMINAL (the woken turn's echo), or spool directories ON DISK in the
// scenario's isolated home. Nothing is read off an in-process struct, and no
// step receives through a tool: the point of the scenario is that nothing
// has to.
package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// ownerSpoolDir is one of the owner's spool directories in the scenario's
// home. The owner is the harp the scenario forced into the coordinator's
// environment, so the path is derived from that forced value rather than
// from a second literal that could drift from it.
func ownerSpoolDir(w *World, dir spool.Dir) (string, error) {
	owner := w.env.ChildEnv("CTXLOOM_SESSION_HARP")
	if owner == "" {
		return "", fmt.Errorf("the scenario never pinned the coordinator's own harp (\"the session harp is\"), so there is no owner spool to read")
	}
	return filepath.Join(w.env.HomeDir, filepath.FromSlash(harpSessionsRel), owner, "persist", "spool", filepath.FromSlash(string(dir))), nil
}

// ownerSpoolMessages parses every plain file in one of the owner's spool
// directories. A directory that does not exist yet is empty, not an error:
// the spool is created on the first write.
func ownerSpoolMessages(w *World, dir spool.Dir) ([]*spool.Message, error) {
	path, err := ownerSpoolDir(w, dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*spool.Message
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(path, e.Name()))
		if err != nil {
			return nil, err
		}
		msg, err := spool.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("%s/%s is not a spool message: %w", dir, e.Name(), err)
		}
		out = append(out, msg)
	}
	return out, nil
}

// reportFrom returns the child's turn RESULT among msgs, by the child's harp.
// The kind filter is the correctness condition, as in j002300FindMessageFrom:
// one child can have several messages in one directory.
func reportFrom(msgs []*spool.Message, harp string) *spool.Message {
	for _, m := range msgs {
		if m.FromHarp == harp && m.Kind == "result" {
			return m
		}
	}
	return nil
}

func registerMailDrainSteps(ctx *godog.ScenarioContext) {
	// F4: only the turn-start hook's spool.Claim creates in/claimed/, so a
	// child whose spool has one had its turn's mail claimed a second time.
	ctx.Step(`^"([^"]*)"'s spool was never claimed by a turn-start hook$`,
		func(c context.Context, name string) error {
			w := worldFrom(c)
			harp, ok := j002300Of(w).harps[name]
			if !ok {
				return fmt.Errorf("no session harp remembered for %q", name)
			}
			claimed := filepath.Join(w.env.HomeDir, filepath.FromSlash(harpSessionsRel), harp, "persist", "spool", filepath.FromSlash(string(spool.ClaimedDirName)))
			if _, err := os.Stat(claimed); err == nil {
				return fmt.Errorf("%s's in/claimed/ exists: a turn-start hook claimed from the child's spool, so the mail its runner handed it as a turn was delivered twice", name)
			} else if !os.IsNotExist(err) {
				return err
			}
			return nil
		})

	// The owner is idle: nothing types into it after the readiness sentinel.
	// A wake is a line posted to the mock's own socket, taken exactly as a
	// typed line — so the woken turn is visible on the owner's terminal as
	// the echo of the wake text (engine.WakeNonce recognises it).
	ctx.Step(`^the session owner is woken within (\d+)s$`,
		func(c context.Context, secs int) error {
			w := worldFrom(c)
			if w.owner == nil {
				return fmt.Errorf("no session owner is standing")
			}
			var wake string
			if !w.owner.sess.WaitForOutput(time.Duration(secs)*time.Second, func(out string) bool {
				wake = wakeLineIn(out)
				return wake != ""
			}) {
				return fmt.Errorf("the session owner was never woken within %ds; its terminal:\n%s", secs, w.owner.sess.Output())
			}
			w.docStepMaterialized = "session owner's terminal — the woken turn:\n  " + wake
			return nil
		})

	// The woken turn's hook acknowledged the report: in/consumed/ holds it.
	// The kind filter (reportFrom) is the correctness condition, and the
	// guidance pins it as THIS child's own words.
	ctx.Step(`^the coordinator's own spool shows "([^"]*)"'s report consumed within (\d+)s, carrying its own guidance, not "([^"]*)"'s$`,
		func(c context.Context, self string, secs int, other string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			selfSpec, ok := j002300.specs[self]
			if !ok {
				return fmt.Errorf("unknown agent %q", self)
			}
			otherSpec, ok := j002300.specs[other]
			if !ok {
				return fmt.Errorf("unknown agent %q", other)
			}
			harp, ok := j002300.harps[self]
			if !ok {
				return fmt.Errorf("no session harp remembered for %q", self)
			}
			deadline := time.Now().Add(time.Duration(secs) * time.Second)
			for {
				consumed, err := ownerSpoolMessages(w, spool.DirInConsumed)
				if err != nil {
					return err
				}
				if r := reportFrom(consumed, harp); r != nil {
					if !strings.Contains(r.Body, selfSpec.Guidance) {
						return fmt.Errorf("%s's consumed report does not carry its OWN guidance %q:\n%s", self, selfSpec.Guidance, r.Body)
					}
					if strings.Contains(r.Body, otherSpec.Guidance) {
						return fmt.Errorf("CONTEXT LEAK: %s's report carries %s's guidance %q:\n%s", self, other, otherSpec.Guidance, r.Body)
					}
					return nil
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("after %ds %s's report (harp %s) is not in the owner's in/consumed/ — the woken turn's hook never delivered it", secs, self, harp)
				}
				time.Sleep(100 * time.Millisecond)
			}
		})
}

// wakeLineIn is the first echoed wake line in a session owner's terminal
// output, "" when there is none.
func wakeLineIn(out string) string {
	for _, line := range strings.Split(out, "\n") {
		echoed, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "mock echo: ")
		if !ok {
			continue
		}
		if _, isWake := engine.WakeNonce(echoed); isWake {
			return echoed
		}
	}
	return ""
}
