package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// PaneInjector writes text into a run's live pane.
//
// TIMING. It writes IMMEDIATELY. There is no quiet window, no output clock,
// and no wait-for-idle: bracketed paste (tmux's `paste-buffer -p`) delivers a
// paste EVENT with explicit begin/end markers, so the program in the pane
// knows the bytes are pasted content and not typing. That is what makes a
// mid-turn write safe, and it is why the heuristic that used to guard this —
// waiting for the terminal to fall quiet, then hoping — is gone rather than
// ported. Any timing knob added here would be re-introducing the guess that
// the mechanism removed.
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
