package tui

import (
	"bytes"
	"context"
	"image/color"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/termemu"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/fakeclock"
	"github.com/ctxloom/ctxloom/internal/testsupport/frontendfake"
)

// selfHarp is the split view's own session in these tests.
const selfHarp = "root-otter"

// engineSink is what the engine receives from the split view: the human's
// re-encoded input and the emulator's replies. Every write wakes waitFor.
type engineSink struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	wake chan struct{}
	// sizes are the engine terminal's resizes, rows x cols.
	sizes [][2]uint16
}

func newEngineSink() *engineSink { return &engineSink{wake: make(chan struct{})} }

func (e *engineSink) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	close(e.wake)
	e.wake = make(chan struct{})
	return e.buf.Write(p)
}

// Read never yields: the drive copies the engine's output, not the tests.
func (e *engineSink) Read([]byte) (int, error) { select {} }

func (e *engineSink) Resize(rows, cols uint16) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	close(e.wake)
	e.wake = make(chan struct{})
	e.sizes = append(e.sizes, [2]uint16{rows, cols})
	return nil
}

func (e *engineSink) String() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.buf.String()
}

func (e *engineSink) take() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.buf.String()
	e.buf.Reset()
	return s
}

func (e *engineSink) resizes() [][2]uint16 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([][2]uint16(nil), e.sizes...)
}

// waitFor blocks until the engine has received want.
func (e *engineSink) waitFor(t *testing.T, want string) {
	t.Helper()
	e.wait(t, "the engine to receive "+strconv.Quote(want), func() bool { return strings.Contains(e.buf.String(), want) })
}

// waitForMatch blocks until what the engine received matches re.
func (e *engineSink) waitForMatch(t *testing.T, re *regexp.Regexp) {
	t.Helper()
	e.wait(t, "the engine to receive "+re.String(), func() bool { return re.MatchString(e.buf.String()) })
}

// waitSized blocks until the engine's terminal has been resized n times.
func (e *engineSink) waitSized(t *testing.T, n int) {
	t.Helper()
	e.wait(t, "the engine to be sized", func() bool { return len(e.sizes) >= n })
}

// wait re-checks cond, under the sink's lock, on every write and resize.
func (e *engineSink) wait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	expired := testsupport.Expiry(t)
	for {
		e.mu.Lock()
		ok, wake, got := cond(), e.wake, e.buf.String()
		e.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-wake:
		case <-expired:
			t.Fatalf("never saw %s; got %q", what, got)
		}
	}
}

var _ frontend.Terminal = (*engineSink)(nil)

// splitPort is a frontend port over a roster the test sets: it counts run
// reads, hands out one run watch, opens per-harp feeds the test pushes into,
// and records control requests.
type splitPort struct {
	mu       sync.Mutex
	sessions []frontend.Session
	runs     []coord.RunInfo
	listRuns int
	watchErr error
	events   chan coord.Event
	feeds    map[string]chan frontend.FeedEvent
	watched  []string
	// releases carries each feed watch's harp as its context ends.
	releases  chan string
	controls  []coord.ControlRequest
	controlFn func(coord.ControlRequest) (coord.ControlResult, error)
	queue     coord.ApprovalSource
}

func newSplitPort() *splitPort {
	return &splitPort{
		sessions: []frontend.Session{{Harp: selfHarp, Engine: "claude-code", State: frontend.SessionLive}},
		events:   make(chan coord.Event, 16),
		feeds:    map[string]chan frontend.FeedEvent{},
		releases: make(chan string, 16),
	}
}

// child adds a run the coordinator holds, under parent.
func (p *splitPort) child(harp, role, parent, phase string) *coord.RunInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runs = append(p.runs, coord.RunInfo{Agent: &coord.AgentIdentity{AgentID: harp, Role: role}, ParentHarp: parent, Phase: phase})
	return &p.runs[len(p.runs)-1]
}

func (p *splitPort) setRuns(f func(runs []coord.RunInfo)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p.runs)
}

func (p *splitPort) runReads() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.listRuns
}

func (p *splitPort) controlled() []coord.ControlRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]coord.ControlRequest(nil), p.controls...)
}

func (p *splitPort) watchedHarps() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.watched...)
}

// released waits for the next feed watch to be released and names it.
func (p *splitPort) released(t *testing.T) string {
	t.Helper()
	select {
	case harp := <-p.releases:
		return harp
	case <-testsupport.Expiry(t):
		t.Fatal("no feed watch was released")
		return ""
	}
}

func (p *splitPort) port() frontend.Port {
	return &frontendfake.Port{
		ListSessionsFn: func(context.Context) ([]frontend.Session, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			return append([]frontend.Session(nil), p.sessions...), nil
		},
		ListRunsFn: func(context.Context) (coord.RunsSnapshot, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.listRuns++
			return coord.RunsSnapshot{Runs: append([]coord.RunInfo(nil), p.runs...)}, nil
		},
		WatchRunsFn: func(context.Context, []string) (frontend.RunWatch, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.watchErr != nil {
				return frontend.RunWatch{}, p.watchErr
			}
			return frontend.RunWatch{Snapshot: coord.RunsSnapshot{Runs: append([]coord.RunInfo(nil), p.runs...)}, Events: p.events}, nil
		},
		WatchFeedFn: func(ctx context.Context, harp string) (frontend.Feed, error) {
			ch := make(chan frontend.FeedEvent, 16)
			p.mu.Lock()
			p.watched = append(p.watched, harp)
			p.feeds[harp] = ch
			p.mu.Unlock()
			context.AfterFunc(ctx, func() { p.releases <- harp })
			return frontend.Feed{Source: "live", Events: ch, Errs: make(chan error, 1)}, nil
		},
		ControlFn: func(_ context.Context, req coord.ControlRequest) (coord.ControlResult, error) {
			p.mu.Lock()
			p.controls = append(p.controls, req)
			fn := p.controlFn
			p.mu.Unlock()
			if fn != nil {
				return fn(req)
			}
			return coord.ControlResult{Verb: req.Verb, Delivery: coord.DeliveryQueued, Changed: true}, nil
		},
		ApprovalSource: p.queue,
	}
}

// splitHarness drives a Split through Update, hermetically: the manual
// clock's timers post into a queue the harness drains, one-shot commands run
// inline and their results are fed back, and the standing receives (frames,
// pane events, run events, feed events) are delivered by the test itself.
type splitHarness struct {
	t      testing.TB
	s      *Split
	clk    *fakeclock.Clock
	port   *splitPort
	engine *engineSink
	pane   *termemu.XVT
	posted []tea.Msg
	// out is every command result the harness does not feed back: raw
	// output, clipboard writes, the renderer's own requests.
	out []tea.Msg
}

type harnessOpt func(*SplitOptions)

func newSplitHarness(t testing.TB, port *splitPort, opts ...harnessOpt) *splitHarness {
	t.Helper()
	h := &splitHarness{t: t, clk: fakeclock.New(), port: port, engine: newEngineSink()}
	h.pane = termemu.NewXVT(80, 24, h.engine, termemu.Callbacks{})
	t.Cleanup(func() { _ = h.pane.Close() })
	prefix, err := ParsePrefixKey("ctrl-]")
	require.NoError(t, err)
	o := SplitOptions{
		Self: selfHarp, Term: h.engine, Prefix: prefix, Clock: h.clk,
		Header: Header{Harp: selfHarp, Engine: "claude-code", Model: "haiku"},
	}
	if port != nil {
		o.Port = port.port()
	}
	for _, opt := range opts {
		opt(&o)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.s = newSplit(ctx, o, h.pane, func(msg tea.Msg) { h.posted = append(h.posted, msg) }, nil)
	h.s.driven = true
	// The tests deliver approval ticks themselves; the view's own then fires
	// at once and is dropped.
	h.s.apprTick = 0
	h.run(h.s.Init())
	return h
}

// sized gives the split its terminal size and the human's colours, and lets
// the engine's first frame land.
func (h *splitHarness) sized(w, rows int) *splitHarness {
	h.send(tea.WindowSizeMsg{Width: w, Height: rows})
	h.send(tea.BackgroundColorMsg{Color: color.Black})
	h.frame()
	return h
}

// send delivers msg, runs what comes back, then drains what timers posted.
func (h *splitHarness) send(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.s.Update(msg)
	h.run(cmd)
}

func (h *splitHarness) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			h.run(c)
		}
	case approvalsTickMsg:
		// Recorded, never delivered: delivered, an immediate tick re-arms
		// itself forever.
		h.out = append(h.out, msg)
	case rosterWatchMsg, rosterMsg, rosterErrMsg, feedOpenedMsg, feedErrMsg, controlResultMsg,
		answerResultMsg, revokeResultMsg, queueEventMsg, tea.ModeReportMsg:
		h.send(msg)
	default:
		h.out = append(h.out, msg)
	}
}

// advance moves the clock and delivers what its timers posted.
func (h *splitHarness) advance(d time.Duration) {
	h.t.Helper()
	h.clk.Advance(d)
	h.drainPosted()
}

func (h *splitHarness) drainPosted() {
	for len(h.posted) > 0 {
		msg := h.posted[0]
		h.posted = h.posted[1:]
		h.send(msg)
	}
}

// frame delivers the pane's latest committed frame, as the frame pump would.
func (h *splitHarness) frame() { h.send(frameMsg{}) }

// engineWrites feeds the engine's output into the pane and lands the frame.
func (h *splitHarness) engineWrites(s string) {
	_, _ = h.pane.Write([]byte(s))
	h.frame()
}

// typeBytes decodes b as the human's terminal sent it and delivers each event.
func (h *splitHarness) typeBytes(b string) {
	h.t.Helper()
	for _, msg := range decodeInput(b) {
		h.send(msg)
	}
}

// keys delivers named key presses (keyMsg's vocabulary).
func (h *splitHarness) keys(ks ...string) {
	h.t.Helper()
	for _, k := range ks {
		h.send(splitKey(k))
	}
}

// screen is the split's rendered rows, styles stripped.
func (h *splitHarness) screen() []string {
	rows := h.s.render()
	for i, r := range rows {
		rows[i] = strings.TrimRight(ansi.Strip(r), " ")
	}
	return rows
}

// raws are the raw writes the split asked bubbletea for.
func (h *splitHarness) raws() []string {
	var out []string
	for _, m := range h.out {
		if r, ok := m.(tea.RawMsg); ok {
			out = append(out, r.Msg.(string))
		}
	}
	return out
}

// splitKey is keyMsg plus the keys the split view binds beyond it.
func splitKey(k string) tea.KeyPressMsg {
	switch k {
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "alt+enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}
	}
	return keyMsg(k)
}

// panelHas reports whether the panel says text, wherever its word wrap broke
// it: spaces are ignored on both sides.
func (h *splitHarness) panelHas(text string) bool {
	var b strings.Builder
	for _, r := range h.screen() {
		b.WriteString(strings.SplitN(r, "│", 2)[0])
	}
	squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
	return strings.Contains(squash(b.String()), squash(text))
}
