package coord

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxInlineBodyBytes bounds the part of one message delivered inline — its
// body AND its structured companion together, so neither half is a way past
// the other's bound. At any fan-out width an uncapped message floods the
// recipient's context, and the coordinator's is the one hardest to recover. A
// longer message is NOT refused: boundBody delivers its head and attaches the
// whole of it as an artifact.
const MaxInlineBodyBytes = 8 << 10

// ErrBodyTooLarge refuses a message too large for its overflow artifact: the
// ceiling is ArtifactUploadSizeCap, the artifact store's own bound, so the one
// number that limits an artifact also limits the message it carries.
var ErrBodyTooLarge = fmt.Errorf("%w: message (body and structured companion) exceeds ArtifactUploadSizeCap (%d bytes), the largest an overflow artifact can carry",
	ErrInvalidRequest, ArtifactUploadSizeCap)

// errOverflowIDTaken refuses an overflow whose id already names DIFFERENT
// content under the same holder. Ids carry the whole digest, so reaching this
// means the journal holds a manifest this path did not write; replacing it
// would silently repoint every earlier marker at the wrong message.
var errOverflowIDTaken = errors.New("overflow: artifact id already names different content")

// OverflowMarkerPhrase is the reader's cue. The agent_send and agent_report
// tool descriptions quote it (asserted in mcpschema), so a reader told to look
// for it finds it.
const OverflowMarkerPhrase = "keep reading for more detail"

// overflowEnvelope is an overflow artifact's content when the message carried
// a structured companion: both halves, recoverable as sent.
type overflowEnvelope struct {
	Body       string          `json:"body"`
	Structured json.RawMessage `json:"structured"`
}

// inlineHead returns the part of body delivered inline and whether anything
// was cut. The cut falls after the last whole line when one ends in the second
// half of the window (a line boundary costs at most half the window), and
// otherwise at a rune boundary: a head is never invalid UTF-8.
func inlineHead(body string, limit int) (string, bool) {
	if len(body) <= limit {
		return body, false
	}
	if i := strings.LastIndexByte(body[:limit], '\n'); i >= limit/2 {
		return body[:i+1], true
	}
	n := limit
	for n > 0 && !utf8.RuneStart(body[n]) {
		n--
	}
	return body[:n], true
}

// overflowArtifactID names an overflow artifact by its WHOLE content digest:
// the same message sent twice is one artifact, and two different messages can
// never share an id (a digest prefix could).
func overflowArtifactID(shaHex string) string {
	return "mail-overflow-" + shaHex
}

// overflowMarker is the line that follows a cut head: the cue phrase and the
// (agent_id, artifact_id) address agent_fetch_artifact takes.
func overflowMarker(holder, artifactID string, size int) string {
	return fmt.Sprintf("\n[... %s: this message is %d bytes and only the part above is inline. "+
		"The full message is attached: agent_fetch_artifact with agent_id %q, artifact_id %q.]\n",
		OverflowMarkerPhrase, size, holder, artifactID)
}

// overflowContent is what an overflow artifact holds: the body alone, or the
// body and structured companion as an overflowEnvelope.
func overflowContent(body string, structured json.RawMessage) (content []byte, mediaType string, err error) {
	if len(structured) == 0 {
		return []byte(body), "text/plain; charset=utf-8", nil
	}
	content, err = json.Marshal(overflowEnvelope{Body: body, Structured: structured})
	if err != nil {
		return nil, "", fmt.Errorf("overflow: encode the structured companion: %w", err)
	}
	return content, "application/json", nil
}

// boundBody is THE message bound, for every message an agent or the
// coordinator hands a recipient: a body and structured companion that together
// fit MaxInlineBodyBytes are returned unchanged; a larger message is stored
// whole as an artifact and replaced by its body's head and the marker naming
// that artifact, with NO structured companion inline.
//
// holder is the harp the artifact's manifest is filed under, and it must be
// one the RECIPIENT may read (authorizeArtifactDownload): its own harp, or a
// child's when the recipient is that child's parent. A raw path was
// rejected: a container-isolated recipient cannot read one, and crossing that
// boundary is what artifacts are for.
//
// The artifact is CREATE-ONCE: the same content under the same holder reuses
// the stored blob and manifest untouched, and an id already naming different
// content is refused (errOverflowIDTaken), never re-revised.
func (c *Coordinator) boundBody(holder, body string, structured json.RawMessage) (string, json.RawMessage, error) {
	if len(body)+len(structured) <= MaxInlineBodyBytes {
		return body, structured, nil
	}
	content, mediaType, err := overflowContent(body, structured)
	if err != nil {
		return "", nil, err
	}
	if len(content) > ArtifactUploadSizeCap {
		return "", nil, fmt.Errorf("%w (got %d)", ErrBodyTooLarge, len(content))
	}
	sum := sha256.Sum256(content)
	shaHex := hex.EncodeToString(sum[:])
	id := overflowArtifactID(shaHex)
	if rec, ok := c.artifactRecord(holder, id); !ok || rec.SHA256 != shaHex {
		if _, _, err := c.artifacts.writeAtomic(strings.NewReader(string(content)), sum[:], uint64(len(content))); err != nil {
			return "", nil, fmt.Errorf("overflow: store the %d-byte message: %w", len(content), err)
		}
		if err := c.recordArtifactOnce(holder, ArtifactProduced{
			ArtifactID: id,
			Kind:       ArtifactKindDocument,
			Name:       id,
			MediaType:  mediaType,
			SizeBytes:  uint64(len(content)),
			SHA256:     sum[:],
			UploadID:   shaHex,
		}); err != nil {
			return "", nil, fmt.Errorf("overflow: journal the manifest for %q under %q: %w", id, holder, err)
		}
	}
	head, _ := inlineHead(body, MaxInlineBodyBytes)
	return head + overflowMarker(holder, id, len(content)), nil, nil
}
