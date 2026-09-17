package coord

// The spool WRITERS: one cached spool.Writer per (harp, direction), and the
// projection of a mailbox Message onto the spool.Message the file carries.
// Sender identity is the DIRECTORY — the coordinator writes in/ and only in/;
// a runner writes its own harp's out/ and only that — which is what makes
// ordering and the consume-rename trivial: single writer per direction.

import (
	"errors"
	"sync"

	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
)

// spoolWriterIDCoordinator is the writer token stamped into every filename the
// COORDINATOR publishes. A fixed token, not a harp: in/ has exactly one writer
// whichever session's spool it is, and naming it after the recipient would make
// every filename claim the wrong author.
const spoolWriterIDCoordinator = "coord"

// spoolWriterCache lends one spool.Writer per harp for ONE direction.
//
// Writers are cached rather than made per message because spool.NewWriter
// re-seeds its sequence by reading the whole direction plus its consumed and
// withdrawn siblings: correct per call, but O(mailbox) per message, and two
// writers for one directory would also each hold their own sequence counter
// and could mint the same filename inside one nanosecond. One writer per
// (harp, direction) is what makes spool.Writer's own mutex sufficient.
type spoolWriterCache struct {
	mapper spool.PathMapper
	dir    spool.Dir
	id     string

	mu      sync.Mutex
	writers map[string]*spool.Writer
	// closed is set by close: the coordinator has torn down, and a write
	// after that point — a terminal notice from a child dying because Close
	// killed it — must be refused the way the closed journals refuse it.
	// The spool is the mailbox's replacement, and "closed means nothing is
	// written" is a property the tests (and a TempDir teardown) rely on.
	closed bool
}

// errSpoolClosed answers writerFor on a closed cache — the spool's twin of
// errStoreClosed.
var errSpoolClosed = errors.New("coord: spool closed")

func newSpoolWriterCache(m spool.PathMapper, dir spool.Dir, writerID string) *spoolWriterCache {
	return &spoolWriterCache{mapper: m, dir: dir, id: writerID, writers: map[string]*spool.Writer{}}
}

// writerFor returns harp's writer, creating (and thereby creating the spool
// directories) on first use. The lock is held across construction — which does
// filesystem work — deliberately: it happens once per harp, and letting two
// callers race to build writers for one directory is how the duplicate
// sequence counter above gets created.
func (c *spoolWriterCache) writerFor(harp string) (*spool.Writer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errSpoolClosed
	}
	if w, ok := c.writers[harp]; ok {
		return w, nil
	}
	w, err := spool.NewWriter(c.mapper, harp, c.dir, c.id)
	if err != nil {
		return nil, err
	}
	c.writers[harp] = w
	return w, nil
}

// close refuses every later writerFor. Cached writers are plain handles with
// nothing to flush; the point is the refusal, not a release.
func (c *spoolWriterCache) close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

// spoolMessageForMail projects one mailbox Message onto its spool.Message.
//
// This function IS the mailbox->file mapping, and every field it drops would
// be a field the post-cutover system never had. What maps:
//
//	mailbox.Kind       -> frontmatter kind      (SpoolKindForMail; "" is a kind)
//	mailbox.ID         -> frontmatter origin_id (the mailbox's own id; spool.Message.ID
//	                      is the filename stem and is not a producer's to choose)
//	mailbox.From       -> frontmatter from_harp (ADVISORY — the directory is identity)
//	mailbox.To         -> frontmatter to        (advisory in the same way)
//	mailbox.InReplyTo  -> frontmatter in_reply_to
//	mailbox.Structured -> frontmatter structured (spoolStructured; a payload
//	                      that is not a JSON object rides the wrapper key —
//	                      see spoolRawJSONKey)
//	mailbox.Body       -> the markdown body, verbatim
//
// Nothing else exists on a mailbox Message, and the mapping is total: a kind
// outside the closed vocabulary and a structured payload that is not a JSON
// object are both refused here, at the projection, rather than written as a
// file missing the part that would not fit.
func spoolMessageForMail(msg Message, to string) (*spool.Message, error) {
	kind, err := SpoolKindForMail(msg.Kind)
	if err != nil {
		return nil, err
	}
	structured, err := spoolStructured(msg.Structured)
	if err != nil {
		return nil, err
	}
	return &spool.Message{
		Kind:       kind,
		FromHarp:   msg.From,
		To:         to,
		InReplyTo:  msg.InReplyTo,
		OriginID:   msg.ID,
		Structured: structured,
		Body:       msg.Body,
	}, nil
}
