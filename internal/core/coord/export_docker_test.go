//go:build docker_integration

package coord

import (
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/liveness"
)

// The docker-gated integration tests compose this coordinator with the real
// runner, isolation and operations — packages that import this one — so they
// live in the external test package and reach the in-package harness through
// these names.

type (
	FakeSpawner     = fakeSpawner
	FakeAgent       = fakeAgent
	ProgressVerdict = progressVerdict
)

var (
	NewFakeSpawner        = newFakeSpawner
	NewTestCoordinator    = newTestCoordinator
	OwnerIdentity         = ownerIdentity
	OwnerLaunch           = ownerLaunch
	OwnedRunOf            = ownerRun
	ResetStrictness       = resetStrictness
	TeeHome               = teeHome
	AwaitRunnerHome       = awaitRunnerHome
	ContainerStoryBackend = containerStoryBackend
)

// BypassAgent is a fake agent binding resolved under the bypass permission.
func BypassAgent() FakeAgent { return fakeAgent{perm: "bypass"} }

// AssessTranscriptProgress is assessTranscriptProgress.
func AssessTranscriptProgress(mon *liveness.Monitor, harp, path string, startedAt time.Time) ProgressVerdict {
	return assessTranscriptProgress(mon, harp, path, startedAt)
}

func (v ProgressVerdict) Stalled() bool         { return v.stalled() }
func (v ProgressVerdict) Present() bool         { return v.present() }
func (v ProgressVerdict) Records() int          { return v.records() }
func (v ProgressVerdict) MaxSeq() int           { return v.maxSeq() }
func (v ProgressVerdict) EntryTypes() []string  { return v.entryTypes() }
func (v ProgressVerdict) AssistantEntries() int { return v.assistantEntries() }
func (v ProgressVerdict) Reason() string        { return v.reason() }
func (v ProgressVerdict) Progressing() bool     { return v.progressing() }
func (v ProgressVerdict) Parked() bool          { return v.parked() }
func ProgressMonitor(thr liveness.Thresholds, now func() time.Time) *liveness.Monitor {
	return progressMonitor(thr, now)
}

// PeerSend is peerSend, for a test that needs the id of the message it sent:
// AgentSend returns only the delivery disposition.
func (c *Coordinator) PeerSend(caller Identity, to, kind, body string) (msgID, disposition string, err error) {
	return c.peerSend(newMessageID(), caller, to, kind, body, nil, "")
}

// ReapIdleRuns runs one idle-reaper sweep now, against the coordinator's clock.
func (c *Coordinator) ReapIdleRuns() { c.reapIdleRuns() }

// CurrentRunID is the run id harp's current incarnation runs under.
func (c *Coordinator) CurrentRunID(harp string) string { return currentRunID(c, harp) }

// RunCause is the terminal cause the journal folded for runID.
func (c *Coordinator) RunCause(runID string) string { return runCause(c, runID) }

// OwnerMail drains the owner's spool, as the owner's turn-start hook does.
var OwnerMail = ownerMail
