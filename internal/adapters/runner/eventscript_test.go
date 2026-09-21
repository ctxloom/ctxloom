package runner

import (
	"context"
	"encoding/json"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// eventScript is an engine.Instance whose driver replays a fixed ChatEvent
// script per turn — relayed as engine.Event exactly as a real driver relays
// its native stream — and answers with the scripted result. A nil script
// with hold set is an engine that never answers: the turn's process parks
// until the run is cancelled.
type eventScript struct {
	script  []agent.ChatEvent
	result  engine.TurnResult
	hold    bool
	running chan struct{} // closed when the first turn's process starts
}

func (e *eventScript) Exec([]present.Presentation) (engine.Exec, error) {
	return engine.Exec{Binary: "script"}, nil
}
func (e *eventScript) Drivers() []engine.StructuredDriver { return []engine.StructuredDriver{e} }
func (e *eventScript) Resume(string) error                { return nil }

func (e *eventScript) Turn(ctx context.Context, _ engine.Exec, _ engine.Turn, out chan<- engine.Event) (engine.TurnResult, error) {
	if e.running != nil {
		select {
		case <-e.running:
		default:
			close(e.running)
		}
	}
	if e.hold {
		<-ctx.Done()
		return engine.TurnResult{}, ctx.Err()
	}
	for _, ev := range e.script {
		payload, err := json.Marshal(ev)
		if err != nil {
			return engine.TurnResult{}, err
		}
		select {
		case out <- engine.Event{Kind: "event", Payload: payload}:
		case <-ctx.Done():
			return engine.TurnResult{}, ctx.Err()
		}
	}
	return e.result, nil
}
