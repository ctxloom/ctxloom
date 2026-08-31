package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrEngineUnmeasured refuses injection into an engine whose paste behaviour
// nobody has measured. It is typed so a caller can tell "this engine is out of
// scope" from a tmux failure: the first is answered by measuring the engine
// and widening pasteMeasuredEngines, the second by looking at the server.
var ErrEngineUnmeasured = errors.New("pane injection is not measured for this engine")

// pasteMeasuredEngines lists the engines whose response to `paste-buffer -p`
// has actually been OBSERVED, and is the whole allowlist.
//
// Membership is an empirical claim, not a guess from a name or a version: an
// engine belongs here once someone has watched a bracketed paste land in it
// correctly. claude qualifies because it advertises bracketed-paste mode
// (ESC[?2004h), never disables it, and was measured submitting 5/5 on an
// explicitly delimited paste followed by a carriage return.
//
// Do NOT add an engine here to make a test or a demo pass. The failure this
// list prevents is silent — an unbracketed paste is not rejected by the
// receiving TUI, it is accepted as literal text — so a wrong entry corrupts
// input rather than erroring, and nothing downstream will report it.
var pasteMeasuredEngines = map[string]bool{
	"claude": true,
}

// PaneInjector writes text into a run's live pane.
//
// ENGINE SCOPE, which is the rule most easily lost by generalizing this type:
// injection is refused for every engine except the ones it has been MEASURED
// against. `paste-buffer -p` brackets a paste ONLY IF the application in the
// pane has enabled bracketed-paste mode (DECSET 2004). Against an engine that
// has not, tmux does not error and does not fall back — it emits the raw text
// unbracketed, which lands in that engine's input as literal garbage, silently.
// The brackets are therefore a property of the APPLICATION, not of tmux, and
// cannot be assumed from this side. An unmeasured engine gets a loud refusal
// naming its remedy; it does not get a blind paste.
//
// TIMING. For a measured engine it writes IMMEDIATELY. There is no quiet
// window, no output clock, and no wait-for-idle: the paste arrives as an EVENT
// with explicit begin/end markers, so the program knows the bytes are pasted
// content and not typing. That is what makes a mid-turn write safe, and it is
// why the heuristic that used to guard this — waiting for the terminal to fall
// quiet, then hoping — is gone rather than ported. Any timing knob added here
// would be re-introducing the guess that the mechanism removed. Note that the
// guess is removed by MEASUREMENT plus refusal, not by the escape sequence:
// that is precisely why the unmeasured case must refuse rather than degrade.
//
// AUTHORIZATION. None is performed here, deliberately. Who may write into
// whose pane is settled UPSTREAM by Coordinator.controlTarget, on the
// coordinator side, before a delivery route is ever chosen; this type is the
// route, not the gate. Giving it a policy field would be a second answer to a
// question that already has one, and the two would drift.
type PaneInjector struct{ host *PaneHost }

// Injector returns the pane-injection route for this host. It is a distinct
// type rather than a method on PaneHost so that a caller which may only
// DELIVER into a pane cannot also start, resize, or destroy one.
func (h *PaneHost) Injector() *PaneInjector { return &PaneInjector{host: h} }

// Inject pastes text into harp's pane, optionally submitting it.
//
// paste-buffer -p wraps the text in the paste-start/paste-end sequences, so
// the program in the pane receives a paste EVENT with explicit boundaries
// instead of a run of synthesized keystrokes whose extent it has to guess. A
// TUI told where a paste ends does not swallow the following carriage return
// as literal text — the failure the old quiet-then-type path existed to dodge.
//
// submit is therefore a SEPARATE actuation (a trailing Enter issued after the
// paste ends), not a newline inside the pasted text: inside a bracketed paste
// a newline is literal content, which is exactly the property that lets
// multi-line text be injected without a TUI acting on the first line.
func (p *PaneInjector) Inject(ctx context.Context, harp, text string, submit bool) error {
	h := p.host
	pn, err := h.pane(harp)
	if err != nil {
		return err
	}

	// Refuse BEFORE staging anything. An unmeasured engine must leave no
	// buffer, no file and no partial paste behind — a refusal that had
	// already written half of itself into the pane would be the silent
	// corruption this gate exists to prevent.
	if !pasteMeasuredEngines[pn.engine] {
		return fmt.Errorf("pane inject: %q runs engine %q: %w; measure that engine's response to a bracketed paste and add it to pasteMeasuredEngines, or deliver this text by a route that does not paste",
			harp, pn.engine, ErrEngineUnmeasured)
	}

	// load-buffer takes a FILE, not stdin, because tmuxRunner deliberately
	// exposes only argv — a stdin seam would exist solely for this one call
	// and would have to be threaded through every fake.
	buf := filepath.Join(h.terms.tmpDir, "ctxloom-paste-"+pn.term.channel)
	if werr := os.WriteFile(buf, []byte(text), 0o600); werr != nil {
		return fmt.Errorf("pane inject: stage paste for %q: %w", harp, werr)
	}
	defer func() { _ = os.Remove(buf) }()

	name := "ctxloom-" + pn.term.channel

	pn.writeMu.Lock()
	defer pn.writeMu.Unlock()
	if _, err := h.terms.runner.Run(ctx, "load-buffer", "-b", name, buf); err != nil {
		return fmt.Errorf("pane inject: load paste buffer for %q: %w", harp, err)
	}
	// -d deletes the buffer after pasting, so a paste cannot be replayed by
	// whatever else reads tmux buffers; -p is the bracketing itself.
	if _, err := h.terms.runner.Run(ctx, "paste-buffer", "-d", "-p", "-b", name, "-t", pn.term.window); err != nil {
		return fmt.Errorf("pane inject: paste into %q: %w", harp, err)
	}
	if !submit {
		return nil
	}
	if _, err := h.terms.runner.Run(ctx, "send-keys", "-t", pn.term.window, "Enter"); err != nil {
		return fmt.Errorf("pane inject: submit paste in %q: %w", harp, err)
	}
	return nil
}
