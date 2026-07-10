package coord

import (
	"encoding/hex"
	"strings"

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
	// protojson-encoded). Deduped on (harp, seq).
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
	Harp          string   `json:"harp"`
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
	Seq        uint64 `json:"seq,omitempty"`
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
	seq        map[string]uint64                    // harp → highest report seq seen
}

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
		if fact.decode(&p) != nil {
			return
		}
		if p.Seq != 0 && p.Seq <= f.seq[p.Harp] {
			return // (harp, seq) dedupe: an at-least-once redelivery
		}
		if p.Seq != 0 {
			f.seq[p.Harp] = p.Seq
		}
		f.latest[p.Harp] = p
		if p.Scope == agentcoordpb.Summary_SCOPE_CHECKPOINT.String() {
			f.checkpoint[p.Harp] = p
		}
	case factArtifact:
		var p artifactFact
		if fact.decode(&p) != nil {
			return
		}
		byID := f.artifacts[p.Harp]
		if byID == nil {
			byID = make(map[string]ArtifactRecord)
			f.artifacts[p.Harp] = byID
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
	const max = 200
	if len(text) > max {
		text = text[:max] + "…"
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
// fact). Best-effort from the stream handler: a journal failure warns — the
// runner's Ack still advances, and the report re-rides the next checkpoint.
func (c *Coordinator) recordSummary(harp string, seq uint64, s *agentcoordpb.Summary) {
	structured := ""
	if st := s.GetStructured(); st != nil {
		if raw, err := protojson.Marshal(st); err == nil {
			structured = string(raw)
		}
	}
	if err := c.runs.Exec(func() ([]Fact, error) {
		if seq != 0 && seq <= c.reportsF.seq[harp] {
			return nil, nil // duplicate delivery
		}
		return []Fact{factAt(factSummary, c.now(), summaryFact{
			Harp:          harp,
			Seq:           seq,
			Scope:         s.GetScope().String(),
			StepID:        s.GetStepId(),
			Text:          s.GetText(),
			Structured:    structured,
			CoversThrough: s.GetCoversThroughSeq(),
			ArtifactIDs:   s.GetArtifactIds(),
		})}, nil
	}); err != nil {
		clidiag.Warn("ctxloom", "coordinator: journal report for %s: %v", harp, err)
	}
	c.audit("agent_report", harp, map[string]string{"scope": s.GetScope().String()})
	// D4: a SCOPE_CHECKPOINT report is the natural compaction point — see
	// checkpoint.go.
	c.maybeCheckpointOnSummary(s)
}

// recordArtifact journals one artifact manifest, assigning the monotonic
// revision inside the journal's serialized window (the producer sends 0; an
// unchanged sha256 is not a new revision).
func (c *Coordinator) recordArtifact(harp string, seq uint64, a *agentcoordpb.ArtifactProduced) {
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
			Seq:        seq,
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
		clidiag.Warn("ctxloom", "coordinator: journal artifact manifest for %s: %v", harp, err)
	}
}

// Artifacts lists the harp's artifact manifests (latest revisions) — the
// seed-projection accessor.
func (c *Coordinator) Artifacts(harp string) []ArtifactRecord {
	var out []ArtifactRecord
	c.runs.View(func() {
		for _, rec := range c.reportsF.artifacts[harp] {
			out = append(out, rec)
		}
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
func (c *Coordinator) LatestReport(harp string) string {
	var out string
	c.runs.View(func() { out = c.reportsF.latestSummary(harp) })
	return out
}
