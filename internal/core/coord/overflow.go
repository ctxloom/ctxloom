package coord

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxInlineBodyBytes bounds the part of one message body delivered inline. At
// any fan-out width an uncapped body floods the recipient's context, and the
// coordinator's is the one hardest to recover. A longer body is NOT refused:
// boundBody delivers its head and attaches the whole of it as an artifact.
const MaxInlineBodyBytes = 8 << 10

// ErrBodyTooLarge refuses a body too large for its overflow artifact: the
// ceiling is ArtifactUploadSizeCap, the artifact store's own bound, so the one
// number that limits an artifact also limits the message it carries.
var ErrBodyTooLarge = fmt.Errorf("%w: body exceeds ArtifactUploadSizeCap (%d bytes), the largest body an overflow artifact can carry",
	ErrInvalidRequest, ArtifactUploadSizeCap)

// OverflowMarkerPhrase is the reader's cue. The agent_send and agent_report
// tool descriptions quote it (asserted in mcpschema), so a reader told to look
// for it finds it.
const OverflowMarkerPhrase = "keep reading for more detail"

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

// overflowArtifactID names an overflow artifact by its content, so the same
// body sent twice is one artifact and one manifest, not two.
func overflowArtifactID(shaHex string) string {
	return "mail-overflow-" + shaHex[:16]
}

// overflowMarker is the line that follows a cut head: the cue phrase and the
// (agent_id, artifact_id) address agent_fetch_artifact takes.
func overflowMarker(holder, artifactID string, size int) string {
	return fmt.Sprintf("\n[... %s: this message is %d bytes and only the part above is inline. "+
		"The full text is attached: agent_fetch_artifact with agent_id %q, artifact_id %q.]\n",
		OverflowMarkerPhrase, size, holder, artifactID)
}

// boundBody is THE body bound, for every body an agent or the coordinator
// hands a recipient: a body within MaxInlineBodyBytes is returned unchanged;
// a longer one is stored whole as an artifact and replaced by its head and
// the marker naming that artifact.
//
// holder is the harp the artifact's manifest is filed under, and it must be
// one the RECIPIENT may read (authorizeArtifactDownload): its own harp, or a
// child's when the recipient is that child's parent. A raw path was
// rejected: a container-isolated recipient cannot read one, and crossing that
// boundary is what artifacts are for.
func (c *Coordinator) boundBody(holder, body string, structured json.RawMessage) (string, json.RawMessage, error) {
	b, err := c.boundBodyOld(holder, body)
	return b, structured, err
}

var errOverflowIDTaken = errors.New("overflow: stub")

func (c *Coordinator) boundBodyOld(holder, body string) (string, error) {
	head, cut := inlineHead(body, MaxInlineBodyBytes)
	if !cut {
		return body, nil
	}
	if len(body) > ArtifactUploadSizeCap {
		return "", fmt.Errorf("%w (got %d)", ErrBodyTooLarge, len(body))
	}
	sum := sha256.Sum256([]byte(body))
	shaHex, size, err := c.artifacts.writeAtomic(strings.NewReader(body), sum[:], uint64(len(body)))
	if err != nil {
		return "", fmt.Errorf("overflow: store the %d-byte body: %w", len(body), err)
	}
	id := overflowArtifactID(shaHex)
	if err := c.recordArtifact(holder, ArtifactProduced{
		ArtifactID: id,
		Kind:       ArtifactKindDocument,
		Name:       id + ".txt",
		MediaType:  "text/plain; charset=utf-8",
		SizeBytes:  uint64(size),
		SHA256:     sum[:],
		UploadID:   shaHex,
	}); err != nil {
		return "", fmt.Errorf("overflow: journal the manifest for %q under %q: %w", id, holder, err)
	}
	return head + overflowMarker(holder, id, len(body)), nil
}
