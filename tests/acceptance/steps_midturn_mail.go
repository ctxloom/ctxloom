//go:build acceptance

// MID-TURN MAIL — the live proof that a delegated child in ONE long,
// multi-tool turn is handed its parent's message at a tool call and acts on it
// before that turn ends (alpha issue 1: a binding reply sat unread through a
// child's whole turn, because a child's mail was only ever delivered as its
// NEXT turn).
//
// It reuses j002300's fixture (bundle, profile, per-engine config, the
// session owner, the owner-spool reader) and P6's axis checks, and adds three
// steps: the gate, the parent's message, and the two judgements.
//
// WHAT PROVES "MID-TURN". Mail reaches the model by exactly two channels: as a
// turn's own prompt (the runner's turn pump) or as hook context inside a turn
// (the mail-peek hook). The child is told to acknowledge the code it is sent
// by calling agent_send with it. The child's canonical transcript records,
// in order, every tool call and a `complete` accounting record at the end of
// every turn. The acknowledgement's tool call landing BEFORE the first
// `complete` means it was made inside the turn the child was already running
// when the message was sent — so the message reached it there, at a tool call.
// Delivered as a new turn instead, the first turn's `complete` would come
// first.
package acceptance

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"

	pb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// midTurnAgent is the delegated child's agent name in every mid-turn cell.
const midTurnAgent = "delegate"

// midTurnState is one cell's fixture: the code minted for it, which only the
// parent's mid-turn message carries.
type midTurnState struct {
	code string
}

func midTurnOf(w *World) *midTurnState {
	if w.midTurn == nil {
		w.midTurn = &midTurnState{}
	}
	return w.midTurn
}

// midTurnTranscriptRecord is the slice of a canonical transcript line these
// steps read (internal/adapters/transcript/record.go).
type midTurnTranscriptRecord struct {
	Seq   int    `json:"seq"`
	Kind  string `json:"kind"`
	Entry *struct {
		Type      string          `json:"type"`
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	} `json:"entry"`
}

// midTurnOrder reads harp's canonical transcript and returns the position of
// the first tool call whose input carries code and of the first turn's
// `complete` record (-1 for either that is absent).
func midTurnOrder(homeDir, harp, code string) (ack, firstComplete int, err error) {
	path := filepath.Join(homeDir, ".ctxloom", "sessions", harp, "transcripts", "transcript.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return -1, -1, err
	}
	defer f.Close()
	ack, firstComplete = -1, -1
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for i := 0; sc.Scan(); i++ {
		var r midTurnTranscriptRecord
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		switch {
		case r.Kind == "complete" && firstComplete < 0:
			firstComplete = i
		case r.Kind == "entry" && r.Entry != nil && r.Entry.Type == "tool_use" && ack < 0 &&
			strings.Contains(string(r.Entry.ToolInput), code):
			ack = i
		}
	}
	return ack, firstComplete, sc.Err()
}

func registerMidTurnMailSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a real "([^"]*)" engine on runtime "([^"]*)" with workspace "([^"]*)" is available for a mid-turn-mail child$`,
		func(c context.Context, engine, runtime, workspace string) error {
			w := worldFrom(c)
			j002300 := j002300Of(w)
			if !p6FixtureBuilds(runtime, workspace) {
				return fmt.Errorf("mid-turn mail: this fixture builds %s, but the row asks for runtime=%q workspace=%q", p6BuildableAxes(), runtime, workspace)
			}
			key := backendTypeToLiveKey(engine)
			a, ok := liveAgents[key]
			if !ok {
				return fmt.Errorf("mid-turn mail: %q is not a known live engine (registry keys: %v)", engine, liveAgentOrder)
			}
			status := probeEngine(key, a)
			w.docStepMaterialized = formatLiveEngineReport([]engineStatus{status})
			if !status.available {
				fmt.Printf("SKIP midturn-mail [engine=%s runtime=%s workspace=%s]: %s\n", engine, runtime, workspace, status.reason)
				return godog.ErrSkip
			}
			var b [6]byte
			if _, err := rand.Read(b[:]); err != nil {
				return err
			}
			midTurnOf(w).code = "MIDTURN-CODE-" + strings.ToUpper(hex.EncodeToString(b[:]))

			spec := &j002300AgentSpec{
				Name:     midTurnAgent,
				Profile:  midTurnAgent + "-profile",
				Bundle:   "bundle-" + midTurnAgent,
				Fragment: "marker",
				Guidance: "You are a careful worker in a mid-turn mail test. Follow the task you are given exactly.",
			}
			j002300.specs[spec.Name] = spec
			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			if err := j002300WriteAgent(w, spec); err != nil {
				return err
			}
			if err := w.env.WriteFile(".ctxloom/config.yaml", j002300PerEngineConfigYAML(a, key, spec, runtime, workspace)); err != nil {
				return err
			}
			if err := w.env.WriteHomeFile(".ctxloom/config.yaml", p6SpoolHomeConfigYAML()); err != nil {
				return err
			}
			if workspace == "worktree" {
				if err := w.env.GitCommit("mid-turn mail fixture"); err != nil {
					return err
				}
			}
			return seedLiveCredentials(a, w.env.SetChildEnv)
		})

	// The parent's message: a genuine agent_send addressed by the child's
	// runtime-minted harp, carrying the cell's code, sent while the child is
	// inside its long turn (the scenario sends it only once the child has
	// said it started).
	ctx.Step(`^the agent calls tool "agent_send" addressed to "([^"]*)"'s session carrying this cell's mid-turn code$`,
		func(c context.Context, name string) error {
			w := worldFrom(c)
			harp, ok := j002300Of(w).harps[name]
			if !ok {
				return fmt.Errorf("mid-turn mail: no session harp remembered for %q", name)
			}
			body := "Message from your parent, sent while you are working: the code is " + midTurnOf(w).code + ". Acknowledge it now, as your task says."
			w.docStepMaterialized = fmt.Sprintf("agent_send -> %s (harp %s)\n  body: %s", name, harp, body)
			return callTool(c, "agent_send", map[string]any{"to_agent_id": harp, "text": body, "kind": pb.MessageKind_MESSAGE_KIND_MESSAGE.String()})
		})

	ctx.Step(`^the coordinator's own spool receives "([^"]*)"'s acknowledgement of this cell's mid-turn code$`,
		func(c context.Context, name string) error {
			w := worldFrom(c)
			childHarp, ok := j002300Of(w).harps[name]
			if !ok {
				return fmt.Errorf("mid-turn mail: no session harp remembered for %q", name)
			}
			code := midTurnOf(w).code
			deadline := time.Now().Add(eventBudget())
			for {
				mail, err := j002300OwnerMail(w)
				if err != nil {
					return err
				}
				var seen []string
				for _, m := range mail {
					if m.FromHarp != childHarp {
						continue
					}
					seen = append(seen, m.Body)
					if strings.Contains(m.Body, code) {
						w.docStepMaterialized = "acknowledged over the bus: " + m.Body
						fmt.Printf("NOTE midturn-mail: %s acknowledged %s: %q\n", name, code, m.Body)
						return nil
					}
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("mid-turn mail: %s never acknowledged %s; its mail so far:\n  %s", name, code, strings.Join(seen, "\n  ---\n  "))
				}
				time.Sleep(250 * time.Millisecond)
			}
		})

	ctx.Step(`^"([^"]*)" acknowledged this cell's mid-turn code inside the turn it was already running$`,
		func(c context.Context, name string) error {
			w := worldFrom(c)
			childHarp, ok := j002300Of(w).harps[name]
			if !ok {
				return fmt.Errorf("mid-turn mail: no session harp remembered for %q", name)
			}
			code := midTurnOf(w).code
			// The child ends its turn right after acknowledging, so its first
			// `complete` record follows shortly. Waiting for it is what makes
			// the order a measurement: a transcript that never records a
			// `complete` could not tell a new turn from the same one.
			var ack, complete int
			deadline := time.Now().Add(eventBudget())
			for {
				var err error
				ack, complete, err = midTurnOrder(w.env.HomeDir, childHarp, code)
				if err != nil {
					return fmt.Errorf("mid-turn mail: reading %s's canonical transcript: %w", name, err)
				}
				if complete >= 0 || time.Now().After(deadline) {
					break
				}
				time.Sleep(250 * time.Millisecond)
			}
			evidence := fmt.Sprintf("canonical transcript of %s: the tool call carrying %s is record #%d; the first turn's complete record is #%d", childHarp, code, ack, complete)
			w.docStepMaterialized = evidence
			fmt.Printf("NOTE midturn-mail: %s\n", evidence)
			switch {
			case ack < 0:
				return fmt.Errorf("mid-turn mail: no tool call in %s's transcript carries %s (%s)", name, code, evidence)
			case complete < 0:
				return fmt.Errorf("mid-turn mail: %s's first turn never recorded its complete record, so the order cannot be judged (%s)", name, evidence)
			case complete < ack:
				return fmt.Errorf("mid-turn mail: %s acted on the code only AFTER its first turn completed — it was delivered as a new turn, not mid-turn (%s)", name, evidence)
			}
			return nil
		})
}
