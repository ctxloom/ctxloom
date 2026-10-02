//go:build acceptance

// The owner's turn-start mail delivery (`ctxloom hook mail-drain`, the S2
// scenario in j002300_cross_engine_delegation.feature).
//
// Every assertion here reads one of two things: the hook's STDOUT, parsed as
// the envelope the engine parses (steps_session_hooks.go's rule — a diagnostic
// on that channel is a corrupted payload, not a warning), or the owner's spool
// directories ON DISK in the scenario's isolated home. Nothing is read off an
// in-process struct, and no step calls agent_recv: the point of the scenario
// is that nothing has to.
package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"

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

	// The report is written by the child's runner AFTER agent_run returns, so
	// the wait is on the disk state the hook will read — never on a receive.
	ctx.Step(`^the coordinator's own spool holds "([^"]*)"'s report within (\d+)s$`,
		func(c context.Context, name string, secs int) error {
			w := worldFrom(c)
			harp, ok := j002300Of(w).harps[name]
			if !ok {
				return fmt.Errorf("no session harp remembered for %q", name)
			}
			deadline := time.Now().Add(time.Duration(secs) * time.Second)
			for {
				msgs, err := ownerSpoolMessages(w, spool.DirIn)
				if err != nil {
					return err
				}
				if reportFrom(msgs, harp) != nil {
					return nil
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("after %ds the owner's in/ holds no result from %s (harp %s); it holds %d message(s)", secs, name, harp, len(msgs))
				}
				time.Sleep(100 * time.Millisecond)
			}
		})

	ctx.Step(`^the drained turn context carries "([^"]*)"'s report with its own guidance, not "([^"]*)"'s$`,
		func(c context.Context, self, other string) error {
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
			env, err := hookStdoutEnvelope(w)
			if err != nil {
				return err
			}
			if env.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
				return fmt.Errorf("the envelope names event %q; the engine only injects UserPromptSubmit context into the turn", env.HookSpecificOutput.HookEventName)
			}
			text := env.HookSpecificOutput.AdditionalContext
			w.docStepMaterialized = fmt.Sprintf("hook mail-drain — turn context:\n%s", text)
			// The provenance header is the coordinator's own attribution of
			// who sent the body; asserting it is what makes "the guidance
			// appeared" a claim about THIS child's report rather than about
			// any text that happened to contain the marker.
			header := "[coordinator-delivered message from=" + harp + " kind=result]"
			if !strings.Contains(text, header) {
				return fmt.Errorf("the turn context carries no report from %s under the coordinator's header %q; it carried:\n%s", self, header, text)
			}
			if !strings.Contains(text, selfSpec.Guidance) {
				return fmt.Errorf("%s's report in the turn context does not carry its OWN guidance %q; context:\n%s", self, selfSpec.Guidance, text)
			}
			if strings.Contains(text, otherSpec.Guidance) {
				return fmt.Errorf("CONTEXT LEAK: the turn context carries %s's guidance %q; context:\n%s", other, otherSpec.Guidance, text)
			}
			return nil
		})

	ctx.Step(`^the coordinator's own spool shows "([^"]*)"'s report consumed, with nothing pending$`,
		func(c context.Context, name string) error {
			w := worldFrom(c)
			harp, ok := j002300Of(w).harps[name]
			if !ok {
				return fmt.Errorf("no session harp remembered for %q", name)
			}
			consumed, err := ownerSpoolMessages(w, spool.DirInConsumed)
			if err != nil {
				return err
			}
			if reportFrom(consumed, harp) == nil {
				return fmt.Errorf("%s's report is not in the owner's in/consumed/ — the hook delivered without acknowledging, or never delivered; consumed/ holds %d message(s)", name, len(consumed))
			}
			for _, dir := range []spool.Dir{spool.DirIn, spool.ClaimedDirName} {
				left, err := ownerSpoolMessages(w, dir)
				if err != nil {
					return err
				}
				if len(left) != 0 {
					return fmt.Errorf("%d message(s) still sit in the owner's %s/ after the drain; the next turn would be handed them again", len(left), dir)
				}
			}
			return nil
		})
}
