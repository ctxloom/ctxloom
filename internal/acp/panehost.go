package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// PaneHost is localTerminals.host's production caller: it owns one live pane
// per run (keyed by harp) and lends that pane out to any number of attached
// viewers.
//
// It lives in this package rather than alongside the coordinator because the
// pane IS a tmux window, and every operation on one — hosting it, fanning its
// bytes out, typing into it, pasting into it — is a tmux command. Putting the
// owner anywhere else would mean exporting the whole tmux vocabulary; keeping
// it here means the only thing that crosses the package line is a byte stream.
//
// LIFETIME, which is the invariant most easily broken by accident: a pane
// belongs to the RUN, never to a viewer of it. Start creates it, Stop destroys
// it, and Detach does neither — a human closing their terminal must not kill
// the agent they were watching. Every other rule here follows from that one.
type PaneHost struct {
	terms *localTerminals

	// pollEvery is how often a pane's capture file is re-read for new bytes.
	// This is a FILE TAIL interval, not an injection-timing knob: pipe-pane
	// appends to a file and there is no readiness signal on it, so something
	// has to look. Injection itself never waits for anything.
	pollEvery time.Duration

	mu    sync.Mutex
	panes map[string]*pane
}

// PaneSpec describes the command a run's pane hosts. It is hostSpec's
// exported face, so a caller outside this package can start a pane without
// the tmux vocabulary leaking out with it.
type PaneSpec struct {
	Command string
	Args    []string
	Cwd     string
	Env     map[string]string
}

// PaneClient is one attached viewer. Both methods are called from the pane's
// own goroutine and MUST NOT block: a slow client would stall the fanout for
// every other client sharing the pane.
type PaneClient interface {
	// Output delivers pane bytes as rendered, escapes included. The slice is
	// only valid for the duration of the call.
	Output(p []byte)
	// Closed reports the pane's command exiting, with its true exit code.
	Closed(exitCode int32, message string)
}

// ErrNoPane is returned when a harp names no live pane. It is typed so a
// caller can tell "that run is not hosted in a pane" from a tmux failure —
// the two want different remedies, and prose is not a contract.
var ErrNoPane = errors.New("no live pane for this run")

// NewPaneHost builds a pane host over a tmux runner. tmpDir holds the capture
// and paste-buffer files.
func NewPaneHost(runner tmuxRunner, tmpDir string) *PaneHost {
	return &PaneHost{
		terms:     newLocalTerminals(runner, tmpDir),
		pollEvery: 25 * time.Millisecond,
		panes:     map[string]*pane{},
	}
}

// pane is one hosted run's window plus its attached viewers.
type pane struct {
	harp string
	term *tmuxTerminal
	host *PaneHost

	// writeMu SERIALIZES every write into the pty. Input from concurrent
	// viewers and an injected paste are all tmux commands against one window;
	// interleaving them would splice two people's keystrokes into each other.
	writeMu sync.Mutex

	mu      sync.Mutex
	clients map[uint64]PaneClient
	nextID  uint64
	closed  bool

	stopTail context.CancelFunc
	tailDone chan struct{}
}

// Start hosts spec's command in a fresh pane for harp. It is the production
// call into localTerminals.host.
//
// Starting a harp that already has a live pane is refused rather than
// silently replacing it: the second pane would orphan the first, whose
// viewers would then watch a window nothing writes to any more.
func (h *PaneHost) Start(ctx context.Context, harp string, spec PaneSpec) error {
	if harp == "" {
		return errors.New("pane host: a pane must name the run it belongs to")
	}
	h.mu.Lock()
	if _, exists := h.panes[harp]; exists {
		h.mu.Unlock()
		return fmt.Errorf("pane host: %q already has a live pane", harp)
	}
	h.mu.Unlock()

	term, err := h.terms.host(ctx, hostSpec(spec))
	if err != nil {
		return err
	}

	p := &pane{harp: harp, term: term, host: h, clients: map[uint64]PaneClient{}}
	h.mu.Lock()
	// Re-check under the lock: two concurrent Starts both pass the check
	// above. Losing the race must not leak the window this one just made.
	if _, exists := h.panes[harp]; exists {
		h.mu.Unlock()
		h.terms.killWindow(ctx, term)
		h.terms.releaseWindow(ctx, term)
		return fmt.Errorf("pane host: %q already has a live pane", harp)
	}
	h.panes[harp] = p
	h.mu.Unlock()

	tailCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	p.stopTail, p.tailDone = cancel, make(chan struct{})
	go p.tail(tailCtx)
	return nil
}

// Attach registers c as a viewer of harp's pane and returns its detach.
//
// Concurrent attaches SHARE the one pane: output fans out to every client and
// input is serialized into the single pty. DETACH NEVER KILLS THE PANE — it
// removes this viewer and nothing else, so the last human leaving a run does
// not end it.
func (h *PaneHost) Attach(harp string, c PaneClient) (detach func(), err error) {
	if c == nil {
		return nil, errors.New("pane host: attach needs a client to deliver to")
	}
	h.mu.Lock()
	p := h.panes[harp]
	h.mu.Unlock()
	if p == nil {
		return nil, fmt.Errorf("pane host: %q: %w", harp, ErrNoPane)
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("pane host: %q: %w", harp, ErrNoPane)
	}
	p.nextID++
	id := p.nextID
	p.clients[id] = c
	p.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.clients, id)
			p.mu.Unlock()
		})
	}, nil
}

// Input types raw bytes into harp's pane, exactly as read from a viewer's
// tty. send-keys -H delivers them as literal bytes rather than as tmux key
// names, so a keystroke that happens to spell "Enter" stays four characters.
func (h *PaneHost) Input(ctx context.Context, harp string, b []byte) error {
	p, err := h.pane(harp)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	args := []string{"send-keys", "-t", p.term.window, "-H"}
	for _, by := range b {
		args = append(args, fmt.Sprintf("%02x", by))
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err = h.terms.runner.Run(ctx, args...)
	return err
}

// Resize sets the pane's dimensions to the attached viewer's.
func (h *PaneHost) Resize(ctx context.Context, harp string, cols, rows int) error {
	p, err := h.pane(harp)
	if err != nil {
		return err
	}
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("pane host: resize %q: %dx%d is not a terminal size", harp, cols, rows)
	}
	_, err = h.terms.runner.Run(ctx, "resize-window", "-t", p.term.window,
		"-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows))
	return err
}

// Stop destroys harp's pane and tells every attached viewer it closed. This
// is the ONLY path that ends a pane; Detach never does.
func (h *PaneHost) Stop(ctx context.Context, harp string) error {
	h.mu.Lock()
	p := h.panes[harp]
	delete(h.panes, harp)
	h.mu.Unlock()
	if p == nil {
		return fmt.Errorf("pane host: %q: %w", harp, ErrNoPane)
	}
	h.terms.killWindow(ctx, p.term)
	// Stop the tail BEFORE releasing, and wait for it: releaseWindow deletes
	// the capture file, and a tail still reading it would otherwise race the
	// delete and could deliver bytes after Closed.
	if p.stopTail != nil {
		p.stopTail()
		<-p.tailDone
	}
	p.finish(-1, "pane stopped")
	h.terms.releaseWindow(ctx, p.term)
	return nil
}

func (h *PaneHost) pane(harp string) (*pane, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p := h.panes[harp]; p != nil {
		return p, nil
	}
	return nil, fmt.Errorf("pane host: %q: %w", harp, ErrNoPane)
}

// tail streams the pane's capture file to every attached client.
//
// It reads the FILE pipe-pane appends to rather than tmux's scrollback,
// which is the same choice host() makes and for the same reason: a dead
// pane's scrollback is overwritten by tmux's "Pane is dead" placeholder,
// while the file keeps what the program actually wrote.
func (p *pane) tail(ctx context.Context) {
	defer close(p.tailDone)

	var offset int64
	buf := make([]byte, 32*1024)
	tick := time.NewTicker(p.host.pollEvery)
	defer tick.Stop()

	for {
		// Drain everything available before checking for exit, so the last
		// bytes a command wrote are delivered rather than lost to the race
		// between its final write and its exit.
		for {
			n, err := readAt(p.term.outputPath, buf, offset)
			if n > 0 {
				offset += int64(n)
				p.broadcast(buf[:n])
			}
			if err != nil || n < len(buf) {
				break
			}
		}
		if st := readStatus(p.term); st != nil {
			var code int32
			if st.ExitCode != nil {
				code = int32(*st.ExitCode)
			}
			p.finish(code, "")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// readAt reads up to len(buf) bytes from path starting at off. A missing file
// is not an error: pipe-pane creates it lazily, so "not there yet" and "no new
// bytes" are the same answer to this caller.
func readAt(path string, buf []byte, off int64) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	n, err := f.ReadAt(buf, off)
	if n > 0 {
		return n, nil
	}
	return 0, err
}

func (p *pane) broadcast(b []byte) {
	p.mu.Lock()
	cs := make([]PaneClient, 0, len(p.clients))
	for _, c := range p.clients {
		cs = append(cs, c)
	}
	p.mu.Unlock()
	for _, c := range cs {
		c.Output(b)
	}
}

// finish tells every viewer the pane closed, exactly once.
func (p *pane) finish(code int32, msg string) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	cs := make([]PaneClient, 0, len(p.clients))
	for _, c := range p.clients {
		cs = append(cs, c)
	}
	p.mu.Unlock()
	for _, c := range cs {
		c.Closed(code, msg)
	}
}
