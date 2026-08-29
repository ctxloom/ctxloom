package coord

import (
	"encoding/hex"
	"slices"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Structured report-back (plan B1.5, folded into B1.6 deliverable 5):
// agent_report rides plane 1 as AgentEvent{Summary}; plan manifests as
// AgentEvent{ArtifactProduced} (rev-7 D2). Both are journaled as durable
// facts in the runs journal and folded into the roster/seed projections —
// sessions are EXECUTION scopes, the log carries the knowledge.

// Report fact kinds (runs journal).
const (
	// factSummary is one filed report: scope + text (+ structured companion,
	// protojson-encoded). Deduped on (harp, run_id, seq) — seq is a PER-RUN
	// counter (Home.seq restarts at 0 in each runner process), so the run id
	// is load-bearing, not decoration. See reportKey.
	factSummary = "report.summary"
	// factArtifact is one artifact manifest (manifest-on-log: name, kind,
	// sha256, size, path label — bytes stay in the producing session's
	// dir). Revision is assigned HERE, monotonically per (harp,
	// artifact_id), when the producer sends 0; an unchanged sha256 is not a
	// new revision.
	factArtifact = "report.artifact"
)

// summaryFact is factSummary's payload.
type summaryFact struct {
	Harp string `json:"harp"`
	// RunID scopes Seq. Absent on facts journaled before this was added, which
	// degrades the key back to (harp, seq) for those — exactly the old
	// behaviour for an old log, and no cross-harp collision, because Harp is
	// still part of the key.
	RunID         string   `json:"run_id,omitempty"`
	Seq           uint64   `json:"seq,omitempty"`
	Scope         string   `json:"scope"`
	StepID        string   `json:"step_id,omitempty"`
	Text          string   `json:"text"`
	Structured    string   `json:"structured,omitempty"` // protojson Struct
	CoversThrough uint64   `json:"covers_through_seq,omitempty"`
	ArtifactIDs   []string `json:"artifact_ids,omitempty"`
}

// artifactFact is factArtifact's payload.
type artifactFact struct {
	Harp       string `json:"harp"`
	ArtifactID string `json:"artifact_id"`
	Revision   uint32 `json:"revision"`
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name,omitempty"`
	MediaType  string `json:"media_type,omitempty"`
	SizeBytes  uint64 `json:"size_bytes,omitempty"`
	SHA256     string `json:"sha256,omitempty"` // hex
	Path       string `json:"path,omitempty"`   // labels["path"]
	// UploadID (E1c) is set once the runner has actually uploaded the bytes
	// via ArtifactTransferService — by construction UploadID == hex(SHA256)
	// (the store is content-addressed), so this is redundant with SHA256 but
	// kept explicit: it is the field DownloadArtifact/FetchArtifactResult
	// echo back, and its presence marks "bytes are retrievable" versus a
	// manifest fact filed with no backing upload.
	UploadID string `json:"upload_id,omitempty"`
}

// ArtifactRecord is the reports fold's view of one artifact's latest
// revision.
type ArtifactRecord struct {
	ArtifactID string
	Revision   uint32
	Kind       string
	Name       string
	MediaType  string
	SizeBytes  uint64
	SHA256     string
	Path       string
	UploadID   string
}

// reportsFold projects report facts: the latest summary per harp (with the
// latest CHECKPOINT kept for seeding) and each artifact's latest revision
// per (harp, artifact_id). It rides the runs journal.
type reportsFold struct {
	latest     map[string]summaryFact               // harp → latest summary
	checkpoint map[string]summaryFact               // harp → latest SCOPE_CHECKPOINT
	artifacts  map[string]map[string]ArtifactRecord // harp → artifact_id → latest
	seq        map[string]uint64                    // reportKey(harp, run_id) → highest report seq seen
}

// reportKey scopes a report-seq watermark. seq is a PER-RUN counter — Home.seq
// starts at 0 in every runner process, and a resume revokes the credential,
// severs the runner and spawns a fresh one — so a watermark keyed by harp
// alone silently discarded every report from a resumed child until its seq
// climbed past the previous run's maximum. Under one-shot driving,
// which mints a new run per TURN, that was essentially every report after turn
// 1. itemsFold has always keyed its watermark by run_id; the harp stays in the
// key here so one harp's watermark can never suppress another's report.
func reportKey(harp, runID string) string { return harp + "\x00" + runID }

func newReportsFold() *reportsFold {
	return &reportsFold{
		latest:     make(map[string]summaryFact),
		checkpoint: make(map[string]summaryFact),
		artifacts:  make(map[string]map[string]ArtifactRecord),
		seq:        make(map[string]uint64),
	}
}

func (f *reportsFold) apply(fact Fact) {
	switch fact.Kind {
	case factSummary:
		var p summaryFact
		if err := fact.decode(&p); err != nil {
			// A report that cannot be decoded is a report that is GONE: the
			// roster shows the previous one and nothing marks the gap. Replay
			// must not fail on its own store's history, so the fold still
			// continues — but silently is how a corrupt log looks identical to
			// an agent that simply never reported.
			clidiag.Warn("ctxloom", "coordinator: skipping an undecodable %s fact in the reports journal (the report it carried is lost): %v", factSummary, err)
			return
		}
		k := reportKey(p.Harp, p.RunID)
		if p.Seq != 0 && p.Seq <= f.seq[k] {
			return // (harp, run_id, seq) dedupe: an at-least-once redelivery
		}
		if p.Seq != 0 {
			f.seq[k] = p.Seq
		}
		f.latest[p.Harp] = p
		if p.Scope == agentcoordpb.Summary_SCOPE_CHECKPOINT.String() {
			f.checkpoint[p.Harp] = p
		}
	case factArtifact:
		var p artifactFact
		if err := fact.decode(&p); err != nil {
			// Same as factSummary above: an undecodable manifest means bytes
			// that exist in a session dir are unreachable through the log, and
			// the only way anyone can find that out is if it is said out loud.
			clidiag.Warn("ctxloom", "coordinator: skipping an undecodable %s fact in the reports journal (the artifact manifest it carried is unresolvable): %v", factArtifact, err)
			return
		}
		byID := f.artifacts[p.Harp]
		if byID == nil {
			byID = make(map[string]ArtifactRecord)
			f.artifacts[p.Harp] = byID
		}
		// This projection is "each artifact's LATEST revision", so a manifest
		// carrying a revision BELOW the one already folded is never it: a
		// replayed or out-of-order fact (or a producer that supplied its own
		// non-zero revision) must not roll the record backwards under every
		// reader that resolves a manifest through it.
		if cur, ok := byID[p.ArtifactID]; ok && p.Revision < cur.Revision {
			return
		}
		byID[p.ArtifactID] = ArtifactRecord{
			ArtifactID: p.ArtifactID,
			Revision:   p.Revision,
			Kind:       p.Kind,
			Name:       p.Name,
			MediaType:  p.MediaType,
			SizeBytes:  p.SizeBytes,
			SHA256:     p.SHA256,
			Path:       p.Path,
			UploadID:   p.UploadID,
		}
	}
}

// latestSummary renders the harp's most recent report line for roster
// projections, truncated to a scannable width.
func (f *reportsFold) latestSummary(harp string) string {
	s, ok := f.latest[harp]
	if !ok {
		return ""
	}
	text := s.Text
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	// RUNES, not bytes: a report line in any non-ASCII script cut at a byte
	// offset splits its final rune and renders U+FFFD in the roster.
	const max = 200
	if utf8.RuneCountInString(text) > max {
		cut := 0
		for i := range text {
			if cut == max {
				text = text[:i]
				break
			}
			cut++
		}
		text += "…"
	}
	return strings.TrimPrefix(s.Scope, "SCOPE_") + ": " + text
}

// nextRevision returns the revision to assign a new manifest of artifactID:
// latest+1, or 0 when the content (sha256) is unchanged — not a new
// revision, skip the fact. Caller holds the journal's Exec window.
func (f *reportsFold) nextRevision(harp, artifactID, sha string) (uint32, bool) {
	if cur, ok := f.artifacts[harp][artifactID]; ok {
		if cur.SHA256 == sha {
			return 0, false
		}
		return cur.Revision + 1, true
	}
	return 1, true
}

// recordSummary journals one filed report (plane-1 Summary event → durable
// fact).
//
// A JOURNAL FAILURE LOSES THE REPORT. The runner's Ack advances on the event
// regardless (handleAgentEvent raises ch.ackSeq before dispatching here, and
// the flush that follows acks through it), so the runner will not re-emit it and
// nothing else re-sends it — there is no retry buffer on this path, unlike the
// item path's flushItems, which restores its facts and holds the watermark
// back. So the failure warns, and everything downstream that would ASSERT the
// report exists is skipped: no audit interaction (the interaction log would
// otherwise record a report the reports journal does not contain) and no
// checkpoint snapshot (whose contract is that the report it compacts to is
// already durable).
func (c *Coordinator) recordSummary(harp, runID string, seq uint64, s *agentcoordpb.Summary) {
	structured := ""
	if st := s.GetStructured(); st != nil {
		if raw, err := protojson.Marshal(st); err == nil {
			structured = string(raw)
		}
	}
	if err := c.runs.Exec(func() ([]Fact, error) {
		if seq != 0 && seq <= c.reportsF.seq[reportKey(harp, runID)] {
			return nil, nil // duplicate delivery
		}
		return []Fact{factAt(factSummary, c.now(), summaryFact{
			Harp:          harp,
			RunID:         runID,
			Seq:           seq,
			Scope:         s.GetScope().String(),
			StepID:        s.GetStepId(),
			Text:          s.GetText(),
			Structured:    structured,
			CoversThrough: s.GetCoversThroughSeq(),
			ArtifactIDs:   s.GetArtifactIds(),
		})}, nil
	}); err != nil {
		clidiag.Warn("ctxloom", "coordinator: journal report for %s: %v — the report is LOST "+
			"(the runner's ack has already advanced past it and nothing re-sends it)", harp, err)
		return
	}
	c.audit("agent_report", harp, map[string]string{"scope": s.GetScope().String()})
	c.notifyParentOfFinalReport(harp, s)
	// D4: a SCOPE_CHECKPOINT report is the natural compaction point — see
	// checkpoint.go.
	c.maybeCheckpointOnSummary(s)
}

// notifyParentOfFinalReport queues a child's FINAL report to its parent as
// mail, so a parent waiting in agent_recv learns of it.
//
// WHY THIS IS NEEDED AT ALL: a report and a message live in different stores.
// recordSummary journals a factSummary into the REPORTS fold, which is what
// roster reads — so a parent could see "FINAL: ..." in roster for a report
// agent_recv had nothing to return. A child filing the report its own
// instructions call "the deliverable" was, from a waiting parent's view,
// silent. That divergence made a reporting-vocabulary gap read as lost
// delivery.
//
// FINAL ONLY. Queueing on PROGRESS or STEP would wake a parent on every
// heartbeat, which is a flood rather than a doorbell; FINAL is the completion
// contract. Widening later is one line, and should be argued for rather than
// assumed.
//
// FULL TEXT, NOT A DIGEST, and the routing is not this function's business:
// queueMail's chokepoint decides whether the message rides the spool (file is
// truth, wire is doorbell — the standard for anything potentially large) or the
// mailbox. Truncating here would discard the bulk the file exists to carry.
//
// CALLED AFTER THE JOURNAL SUCCEEDS, deliberately: the report is durable before
// any notification is attempted, so a failed queue costs a WAKE and never a
// REPORT. A failure warns rather than propagating, for the same reason — the
// report already exists and nothing downstream should be skipped because the
// doorbell did not go out.
func (c *Coordinator) notifyParentOfFinalReport(harp string, s *agentcoordpb.Summary) {
	if s.GetScope() != agentcoordpb.Summary_SCOPE_FINAL {
		return
	}
	rec := c.runsF.currentRun(harp)
	// A top-level run has no parent to notify. Same guard launchgate.go uses
	// for the identical queueMail(rec.Harp, rec.ParentHarp, ...) call.
	if rec == nil || rec.ParentHarp == "" {
		return
	}
	if _, _, err := c.queueMail(harp, rec.ParentHarp, KindReport, s.GetText()); err != nil {
		clidiag.Warn("ctxloom", "coordinator: %s's FINAL report is journaled but could not be queued to %s: %v "+
			"(the report is intact in the reports fold; its parent will not be woken by it)", harp, rec.ParentHarp, err)
	}
}

// recordArtifact journals one artifact manifest, assigning the monotonic
// revision inside the journal's serialized window (the producer sends 0; an
// unchanged sha256 is not a new revision).
func (c *Coordinator) recordArtifact(harp string, a *agentcoordpb.ArtifactProduced) {
	sha := hex.EncodeToString(a.GetSha256())
	if err := c.runs.Exec(func() ([]Fact, error) {
		rev := a.GetRevision()
		if rev == 0 {
			next, changed := c.reportsF.nextRevision(harp, a.GetArtifactId(), sha)
			if !changed {
				return nil, nil
			}
			rev = next
		}
		return []Fact{factAt(factArtifact, c.now(), artifactFact{
			Harp:       harp,
			ArtifactID: a.GetArtifactId(),
			Revision:   rev,
			Kind:       a.GetKind().String(),
			Name:       a.GetName(),
			MediaType:  a.GetMediaType(),
			SizeBytes:  a.GetSizeBytes(),
			SHA256:     sha,
			Path:       a.GetLabels()["path"],
			UploadID:   a.GetUploadId(),
		})}, nil
	}); err != nil {
		clidiag.Warn("ctxloom", "coordinator: journal artifact manifest for %s: %v — the manifest is LOST, "+
			"so any bytes already uploaded for it are unreachable through the log", harp, err)
	}
}

// Artifacts lists the harp's artifact manifests (latest revisions) — the
// seed-projection accessor. Ordered by artifact id: the underlying projection
// is a map, and an unordered listing renders differently on every call, so two
// listings of an unchanged set cannot be compared.
func (c *Coordinator) Artifacts(harp string) []ArtifactRecord {
	var out []ArtifactRecord
	c.runs.View(func() {
		for _, rec := range c.reportsF.artifacts[harp] {
			out = append(out, rec)
		}
	})
	slices.SortFunc(out, func(a, b ArtifactRecord) int {
		return strings.Compare(a.ArtifactID, b.ArtifactID)
	})
	return out
}

// artifactRecord looks up one (harp, artifact_id)'s latest-revision
// manifest — DownloadArtifact's resolution accessor (E1d).
func (c *Coordinator) artifactRecord(harp, artifactID string) (ArtifactRecord, bool) {
	var (
		rec ArtifactRecord
		ok  bool
	)
	c.runs.View(func() {
		rec, ok = c.reportsF.artifacts[harp][artifactID]
	})
	return rec, ok
}

// LatestReport returns the harp's most recent report line ("" when none).
//
// test-only: no production caller — consumer.go's own use of
// reportsF.latestSummary reads it from inside an already-held View window,
// which is what this wrapper supplies. Kept rather than inlined at its 5
// test call sites (reports_resume_dedupe_test.go, runchannel_test.go): those
// tests run against a live coordinator with real background goroutines, and
// reading reportsF.latest directly without this View would be a race the
// wrapper exists to prevent — the finding's own suggested alternative
// ("route consumer.go:220 through it") doesn't change that.
func (c *Coordinator) LatestReport(harp string) string {
	var out string
	c.runs.View(func() { out = c.reportsF.latestSummary(harp) })
	return out
}
