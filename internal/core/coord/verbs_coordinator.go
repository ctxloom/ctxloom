package coord

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// The Coordinator implements Verbs. Each verb validates its request FIRST
// (the one site), then runs the coordinator's body for it.

// Spawn is agent_run.
func (c *Coordinator) Spawn(ctx context.Context, caller Identity, req SpawnRequest) (SpawnResult, error) {
	if err := req.Validate(); err != nil {
		return SpawnResult{}, err
	}
	workspace, dirtyTree := req.axes()
	out, err := c.AgentRun(ctx, caller, req.Agent, req.Prompt, workspace, dirtyTree)
	if err != nil {
		return SpawnResult{}, err
	}
	runtime := out.Runtime
	if runtime == "" {
		runtime = "host"
	}
	// The launch runs on its own goroutine, so "spawned" would be a claim,
	// not an observation: a child whose launch has ALREADY failed by the
	// time this answers is reported as a failure.
	return SpawnResult{
		Harp:        out.Harp,
		RunID:       out.RunID,
		Engine:      out.Engine,
		Profiles:    out.Profiles,
		Runtime:     runtime,
		Queued:      out.Queued,
		Degraded:    out.Degraded,
		Disposition: spawnDisposition(out, runtime, c.settledFailureCause(out.RunID)),
	}, nil
}

// Send is agent_send.
func (c *Coordinator) Send(_ context.Context, caller Identity, req SendRequest) (SendResult, error) {
	if err := req.Validate(); err != nil {
		return SendResult{}, err
	}
	msgID, disposition, err := c.peerSend(caller, req.To, req.Kind, req.Body, req.Structured, req.InReplyTo)
	if err != nil {
		return SendResult{}, err
	}
	return SendResult{MessageID: msgID, Disposition: disposition}, nil
}

// Recv is agent_recv: the caller's bounded long poll on its own inbox.
func (c *Coordinator) Recv(ctx context.Context, caller Identity, wait time.Duration) ([]Message, error) {
	return c.AgentRecv(ctx, caller, wait)
}

// Stop is agent_stop, in its two shapes: one child by harp, or every live
// child of the caller's session.
func (c *Coordinator) Stop(ctx context.Context, caller Identity, req StopRequest) (StopResult, error) {
	if err := req.Validate(); err != nil {
		return StopResult{}, err
	}
	if req.Harp == "" {
		stopped, err := c.stopChildren(ctx, caller, strings.TrimSpace(req.Reason))
		if err != nil {
			return StopResult{}, err
		}
		msg := fmt.Sprintf("stopped %d child(ren) of this session; their execution slots are freed (a later agent_send resumes any of them as a fresh run)", len(stopped))
		if len(stopped) == 0 {
			msg = "no live children to stop"
		}
		return StopResult{Disposition: msg, Children: stopped}, nil
	}
	disposition, err := c.AgentStop(caller, req.Harp, req.Reason)
	if err != nil {
		return StopResult{}, err
	}
	return StopResult{Disposition: disposition}, nil
}

// Report is agent_report: the caller's summary, journaled under its own harp
// and current run.
func (c *Coordinator) Report(_ context.Context, caller Identity, req ReportRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	scope, ok := ParseSummaryScope("SCOPE_" + strings.ToUpper(req.Scope))
	if !ok {
		return fmt.Errorf("%w: report: unknown scope %q", ErrInvalidRequest, req.Scope)
	}
	return c.recordSummary(caller.Harp, caller.RunID, 0, Summary{Scope: scope, Text: req.Body})
}

// FetchArtifact is agent_fetch_artifact: an artifact the caller is entitled
// to (its own, a child's, or any as a consumer), read whole from the store.
func (c *Coordinator) FetchArtifact(_ context.Context, caller Identity, req FetchRequest) (Artifact, error) {
	if err := req.Validate(); err != nil {
		return Artifact{}, err
	}
	rec, ok := c.artifactRecord(req.Harp, req.ArtifactID)
	if !ok {
		return Artifact{}, fmt.Errorf("fetch: no artifact %q for %q", req.ArtifactID, req.Harp)
	}
	if err := c.authorizeArtifactDownload(caller, req.Harp); err != nil {
		return Artifact{}, err
	}
	f, err := c.artifacts.open(rec.UploadID)
	if err != nil {
		return Artifact{}, err
	}
	defer f.Close()
	bytes, err := io.ReadAll(f)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{ID: rec.ArtifactID, Digest: rec.UploadID, Bytes: bytes}, nil
}

// Control dispatches one control verb on a target the initiator owns.
func (c *Coordinator) Control(ctx context.Context, by ControlInitiator, req ControlRequest) (ControlResult, error) {
	if err := req.Validate(); err != nil {
		return ControlResult{}, err
	}
	switch req.Verb {
	case ControlVerbSteer:
		out, err := c.ControlSteer(ctx, by, req.Harp, req.Body)
		if err != nil {
			return ControlResult{}, err
		}
		return ControlResult{Verb: req.Verb, Delivery: out.Delivery, MessageID: out.MessageID}, nil
	case ControlVerbQuestion:
		ans, err := c.ControlQuestion(ctx, by, req.Harp, req.Body)
		if err != nil {
			return ControlResult{}, err
		}
		return ControlResult{Verb: req.Verb, Answer: &ans}, nil
	case ControlVerbSummarize:
		ans, err := c.ControlSummarize(ctx, by, req.Harp, req.Body)
		if err != nil {
			return ControlResult{}, err
		}
		return ControlResult{Verb: req.Verb, Answer: &ans}, nil
	case ControlVerbPause:
		changed, err := c.ControlPause(ctx, by, req.Harp, req.Body)
		return ControlResult{Verb: req.Verb, Changed: changed}, err
	case ControlVerbResume:
		changed, err := c.ControlResume(ctx, by, req.Harp)
		return ControlResult{Verb: req.Verb, Changed: changed}, err
	}
	return ControlResult{}, fmt.Errorf("%w: control: unknown verb %q", ErrInvalidRequest, req.Verb)
}
