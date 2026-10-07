package kit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// LineMapper maps one stdout line of a per-turn engine process to 0..N
// neutral events. One mapper per turn (it may join across lines); a
// malformed or unknown line maps to nil, never an error. End is called once
// at stdout's end and may emit what only the whole stream decides.
type LineMapper interface {
	Map(line []byte) []agent.ChatEvent
	End() []agent.ChatEvent
}

// ErrTurnProcessDied is a turn whose engine process ended WITHOUT a
// completion and exited in failure: it died mid-turn, which ends the run.
var ErrTurnProcessDied = errors.New("the turn's process died before it answered")

// ProcessTurn is an engine.StructuredDriver over one engine process per
// turn: argv from the Exec, the prompt written to stdin, stdout read as
// lines and mapped to events, every event relayed, and the turn's native
// key, answer and exit classified. The engine supplies only what differs:
// its argv, its stdin protocol and its line codec.
//
// ctx ending is an INTERRUPT, not a teardown: the transport asks the process
// to stop and kills it after its grace, while the driver keeps reading, so
// what the process says on its way out (its completion, its session) is
// still relayed. The turn then returns ctx's error: it was cut short.
type ProcessTurn struct {
	// Name names the engine's process in the driver's errors; it is
	// wording only, never compared.
	Name string
	// Argv is the per-turn argv: the Exec's args plus the engine's protocol
	// flags and the turn's resume key.
	Argv func(ex engine.Exec, in engine.Turn) ([]string, error)
	// WritePrompt writes the turn's prompt to stdin, which is then closed.
	WritePrompt func(w io.Writer, prompt string) error
	// NewMapper returns a fresh LineMapper for each turn.
	NewMapper func() LineMapper
	// Open and Now are seams: nil spawns the real process (Spawn, under
	// Grace) and stamps with time.Now.
	Open TransportFunc
	Now  func() time.Time
	// Grace is the interrupt's grace; 0 is DefaultInterruptGrace.
	Grace time.Duration
}

var _ engine.StructuredDriver = ProcessTurn{}

// Turn spawns the turn's process with the Exec's binary, env and work dir,
// writes the prompt, relays every event, and returns the native key the next
// turn resumes by with the answer. A nil out relays nothing. A process that
// ends with no completion and exits in failure died mid-turn
// (ErrTurnProcessDied, wrapped with its own account).
func (d ProcessTurn) Turn(ctx context.Context, ex engine.Exec, in engine.Turn, out chan<- engine.Event) (engine.TurnResult, error) {
	argv, err := d.Argv(ex, in)
	if err != nil {
		return engine.TurnResult{}, err
	}
	tr, err := d.open()(ctx, ex.Binary, argv, ex.Env, ex.WorkDir)
	if err != nil {
		return engine.TurnResult{}, err
	}
	events := make(chan agent.ChatEvent, 64)
	mapper, now := d.NewMapper(), d.now()
	go func() {
		readEvents(tr.Stdout, mapper, events, now)
		close(events)
	}()
	if err := d.WritePrompt(tr.Stdin, in.Prompt); err != nil {
		_ = tr.Close()
		for range events {
			// drained, so the reader can return
		}
		return engine.TurnResult{}, err
	}
	_ = tr.Stdin.Close()
	return d.relay(ctx, tr, events, out)
}

func (d ProcessTurn) grace() time.Duration {
	if d.Grace > 0 {
		return d.Grace
	}
	return DefaultInterruptGrace
}

func (d ProcessTurn) open() TransportFunc {
	if d.Open != nil {
		return d.Open
	}
	grace := d.grace()
	return func(ctx context.Context, binary string, args []string, env map[string]string, workDir string) (*Transport, error) {
		return spawnGrace(ctx, binary, args, env, workDir, grace)
	}
}

func (d ProcessTurn) now() func() time.Time {
	if d.Now != nil {
		return d.Now
	}
	return time.Now
}

// readEvents reads newline-delimited lines from stdout (no line-length cap:
// tool outputs can be large) and maps each to events on out until EOF or a
// read error, then sends the mapper's End. It is NOT bounded by the turn's
// context: an interrupted process still has things to say on its way out,
// and the transport guarantees the EOF (a kill after its grace). Each entry
// is stamped with a receipt time (stampEntryTime).
func readEvents(stdout io.Reader, m LineMapper, out chan<- agent.ChatEvent, now func() time.Time) {
	br := bufio.NewReaderSize(stdout, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		for _, ev := range m.Map(line) {
			out <- stampEntryTime(ev, now)
		}
		if err != nil {
			break // EOF or read error (e.g. transport closed)
		}
	}
	for _, ev := range m.End() {
		out <- stampEntryTime(ev, now)
	}
}

// stampEntryTime records receipt time on an entry event that arrived without
// a timestamp (a stream that carries no per-event time always lands here); an
// entry that has one is left untouched.
func stampEntryTime(ev agent.ChatEvent, now func() time.Time) agent.ChatEvent {
	if ev.Entry != nil && ev.Entry.Timestamp.IsZero() {
		ev.Entry.Timestamp = now()
	}
	return ev
}

// relay folds and relays the turn's events until the process's stdout ends,
// then classifies how the turn ended. A process whose stdout outlives the
// interrupt by twice the grace (a grandchild holding stdout open) is torn
// down, so an interrupted turn always returns.
func (d ProcessTurn) relay(ctx context.Context, tr *Transport, events <-chan agent.ChatEvent, out chan<- engine.Event) (engine.TurnResult, error) {
	grace := d.grace()
	finished := make(chan struct{})
	defer close(finished)
	go closeOverdue(ctx, finished, tr, 2*grace)
	r := turnRelay{ctx: ctx, out: out, grace: grace, name: d.Name}
	var res engine.TurnResult
	var acc turnAccumulator
	var relayErr error
	for ev := range events {
		acc.absorb(&res, &ev)
		if err := r.send(ev); err != nil && relayErr == nil {
			relayErr = err
			_ = tr.Close()
		}
	}
	res.Answer = acc.answer()
	exitErr := tr.Wait()
	res.ExitCode = engineExit(ctx, tr, exitErr)
	switch {
	case relayErr != nil:
		return res, relayErr
	case ctx.Err() != nil:
		return res, ctx.Err()
	case acc.results == 0 && exitErr != nil:
		return res, fmt.Errorf("%s: %w: %w", d.Name, ErrTurnProcessDied, exitErr)
	}
	return res, nil
}

// closeOverdue tears tr down when the turn is still unfinished bound after
// ctx ended.
func closeOverdue(ctx context.Context, finished <-chan struct{}, tr *Transport, bound time.Duration) {
	select {
	case <-finished:
		return
	case <-ctx.Done():
	}
	select {
	case <-finished:
	case <-time.After(bound):
		_ = tr.Close()
	}
}

// turnAccumulator folds one turn's events, which may span more than one
// completion (one process can answer more than once): the answer is the LAST
// completion's text, while the denials are EVERY completion's, each joined
// with the reason its Denied event gave (a completion's own denials carry
// none). The runner keeps the last completion, so absorb rewrites each
// completion's Denials to the turn's so far.
type turnAccumulator struct {
	segment  []string // assistant text since the last completion
	last     string   // the last completion's text
	results  int
	reasons  map[string]string // tool call id → its Denied reason
	denials  []agent.PermissionDenial
	seenCall map[string]bool
}

// absorb records ev's native key on res and folds ev into the turn; a
// completion's Denials are rewritten in place before it is relayed.
func (a *turnAccumulator) absorb(res *engine.TurnResult, ev *agent.ChatEvent) {
	switch {
	case ev.Session != nil:
		if ev.Session.SessionID != "" {
			res.NativeKey = ev.Session.SessionID
		}
	case ev.Entry != nil:
		if ev.Entry.Type == agent.EntryTypeAssistant {
			a.segment = append(a.segment, ev.Entry.Content)
		}
	case ev.Denied != nil:
		if a.reasons == nil {
			a.reasons = map[string]string{}
		}
		a.reasons[ev.Denied.ToolCallID] = ev.Denied.Reason
	case ev.Complete != nil:
		a.complete(ev.Complete)
	}
}

// complete closes one completion: its text becomes the answer so far, and
// its Denials are replaced by the turn's, each first-seen call once.
func (a *turnAccumulator) complete(m *agent.TurnMeta) {
	a.last = strings.Join(a.segment, "")
	a.segment = nil
	a.results++
	for _, d := range m.Denials {
		if a.seenCall[d.ToolCallID] {
			continue
		}
		if a.seenCall == nil {
			a.seenCall = map[string]bool{}
		}
		a.seenCall[d.ToolCallID] = true
		if d.Reason == "" {
			d.Reason = a.reasons[d.ToolCallID]
		}
		a.denials = append(a.denials, d)
	}
	m.Denials = slices.Clone(a.denials)
}

// answer is the turn's answer: the last completion's text, or (a process
// that ended with no completion) everything it said.
func (a *turnAccumulator) answer() string {
	if a.results == 0 {
		return strings.Join(a.segment, "")
	}
	return a.last
}

// turnRelay sends a turn's events on out (a nil out relays nothing). Before
// the interrupt a send waits for the consumer; after it, the consumer still
// gets the process's last words, but a send waits no longer than the grace in
// all: a consumer that stopped reading must not hold the turn open.
type turnRelay struct {
	ctx      context.Context
	out      chan<- engine.Event
	grace    time.Duration
	name     string
	deadline <-chan time.Time // armed at the interrupt
	expired  bool
}

// send relays ev; the only error is one ev cannot be encoded.
func (r *turnRelay) send(ev agent.ChatEvent) error {
	if r.out == nil || r.expired {
		return nil
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("%s: encoding a turn event: %w", r.name, err)
	}
	e := engine.Event{Kind: ev.Kind(), Payload: payload}
	if r.ctx.Err() == nil {
		select {
		case r.out <- e:
			return nil
		case <-r.ctx.Done():
		}
	}
	if r.deadline == nil {
		r.deadline = time.After(r.grace)
	}
	select {
	case r.out <- e:
	case <-r.deadline:
		r.expired = true
	}
	return nil
}
