package spool

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Message is one spool file: a YAML frontmatter head and a markdown body.
//
// FRONTMATTER IS THE CONTROL PLANE. A coordinator or runner handles routing,
// correlation, approval decisions, withdrawals and acknowledgements from the
// head ALONE and never needs to read the body. That is why a control message
// with an EMPTY BODY is legal, and why there is no second file format for
// control: one shape serves both planes.
//
// The body is the payload for the eventual reader — a human, or an agent
// reading the file with its own file tools. Markdown (not JSON) is what makes
// that no-MCP read surface first class: the file is legible and greppable as
// it sits on disk.
type Message struct {
	// V is the format version. Written as 1; a reader must not refuse a
	// higher one on sight — see Parse.
	V int
	// ID is the filename stem, stamped by the writer. Dedupe key,
	// InReplyTo target, and audit handle are all this one string.
	ID string
	// Kind is the closed message vocabulary of the layer above (message,
	// result, steer, approval_request, ask:spawn, ...). This package does not
	// police the vocabulary — it would have to change with every consumer —
	// but it does require one to be present.
	Kind string
	// FromHarp is ADVISORY DISPLAY METADATA ONLY. Sender identity is which
	// spool the file appeared in, stamped by the side that owns the
	// directory; a value here is a claim by the file's interior and is never
	// what a router should trust.
	FromHarp string
	// To is the intended recipient ("parent", a harp, "user"), advisory in
	// the same way: delivery is by directory.
	To string
	// InReplyTo correlates a reply to the ID of the request it answers. The
	// asker matches it against its waiter table.
	InReplyTo string
	// OriginID is the identity this message carried in the system that
	// PRODUCED it, when that system is not the spool itself.
	//
	// It exists for the mailbox shadow tee, where the same logical message
	// lives in two places at once: the mailbox knows it by its own id, and
	// InReplyTo on a teed reply quotes THAT id, not a spool filename. Without
	// somewhere to record it, a spool file cannot be matched back to the
	// mailbox delivery it shadows and every correlation the tee copies is a
	// dangling reference — which would make the tee's whole purpose (comparing
	// the two representations before one of them is switched off)
	// unmeasurable.
	//
	// It is DELIBERATELY not ID: ID is the filename stem, stamped by the
	// Writer, and the spool's own identity must never be something a producer
	// can choose. Once the file IS the message, this is empty.
	OriginID string
	// Created is the write stamp, serialised RFC3339 with nanoseconds.
	Created time.Time
	// TTLSeconds is an optional budget: a reader may refuse a stale ask
	// rather than execute a request whose waiter is long gone. 0 = no TTL.
	TTLSeconds int
	// Structured is the machine companion to the body (approval projections,
	// spawn arguments, tool arguments).
	Structured map[string]any
	// Body is the markdown payload, verbatim. Empty is legal.
	Body string

	// head is the parsed frontmatter mapping, retained so that keys this
	// build does not know about survive a parse/rewrite round trip.
	// Forward compatibility is a ruling, not a nicety: an older ctxloom
	// consuming a newer peer's file must not silently strip the fields that
	// peer depends on.
	head *yaml.Node
}

// Frontmatter key names. One list, used by both the encoder and the
// known-key filter, so a new field cannot be added to one and forgotten in
// the other.
const (
	keyV          = "v"
	keyID         = "id"
	keyKind       = "kind"
	keyFromHarp   = "from_harp"
	keyTo         = "to"
	keyInReplyTo  = "in_reply_to"
	keyOriginID   = "origin_id"
	keyCreated    = "created"
	keyTTLSeconds = "ttl_s"
	keyStructured = "structured"
)

// knownKeys is the frontmatter this build understands, in canonical emit
// order for a message that was not parsed from disk.
var knownKeys = []string{keyV, keyID, keyKind, keyFromHarp, keyTo, keyInReplyTo, keyOriginID, keyCreated, keyTTLSeconds, keyStructured}

// CurrentVersion is the format version Encode stamps.
const CurrentVersion = 1

// frontMatterFence is the delimiter line, matching every other ctxloom
// markdown-with-frontmatter file (fragments, plans, memory).
const frontMatterFence = "---"

// Parse decodes one spool file.
//
// Frontmatter is REQUIRED — it is the control plane, and a file without it
// carries no kind, no correlation and no stamp, so it is malformed rather
// than "a message with defaults". Kind and Created are likewise required:
// both are writer-stamped invariants, and a reader that invented them would
// route on a guess.
//
// A version NEWER than CurrentVersion is accepted, not refused: unknown keys
// round-trip and the known ones still mean what they mean. Refusing on
// version would make every forward-compatible field pointless.
func Parse(data []byte) (*Message, error) {
	head, body, found := splitFrontMatter(data)
	if !found {
		return nil, fmt.Errorf("spool: message has no YAML frontmatter (the control plane): a spool file must open with a %q fence", frontMatterFence)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(head, &doc); err != nil {
		return nil, fmt.Errorf("spool: decoding message frontmatter: %w", err)
	}
	mapping := documentMapping(&doc)
	if mapping == nil {
		return nil, fmt.Errorf("spool: message frontmatter is not a YAML mapping")
	}
	msg := &Message{head: mapping, Body: body}
	if err := msg.decodeHead(); err != nil {
		return nil, err
	}
	if msg.Kind == "" {
		return nil, fmt.Errorf("spool: message frontmatter is missing %q, the routing discriminator", keyKind)
	}
	if msg.Created.IsZero() {
		return nil, fmt.Errorf("spool: message frontmatter is missing %q (RFC3339 write stamp)", keyCreated)
	}
	return msg, nil
}

// decodeHead reads the known keys off the retained mapping.
func (m *Message) decodeHead() error {
	scalar := func(key string) (string, bool, error) {
		node, ok := mappingGet(m.head, key)
		if !ok || node.Tag == "!!null" {
			return "", false, nil
		}
		var s string
		if err := node.Decode(&s); err != nil {
			return "", false, fmt.Errorf("spool: frontmatter %q must be a string: %w", key, err)
		}
		return s, true, nil
	}
	var err error
	if m.ID, _, err = scalar(keyID); err != nil {
		return err
	}
	if m.Kind, _, err = scalar(keyKind); err != nil {
		return err
	}
	if m.FromHarp, _, err = scalar(keyFromHarp); err != nil {
		return err
	}
	if m.To, _, err = scalar(keyTo); err != nil {
		return err
	}
	if m.InReplyTo, _, err = scalar(keyInReplyTo); err != nil {
		return err
	}
	if m.OriginID, _, err = scalar(keyOriginID); err != nil {
		return err
	}
	if node, ok := mappingGet(m.head, keyV); ok {
		if err := node.Decode(&m.V); err != nil {
			return fmt.Errorf("spool: frontmatter %q must be an integer: %w", keyV, err)
		}
	}
	if node, ok := mappingGet(m.head, keyTTLSeconds); ok && node.Tag != "!!null" {
		if err := node.Decode(&m.TTLSeconds); err != nil {
			return fmt.Errorf("spool: frontmatter %q must be an integer number of seconds: %w", keyTTLSeconds, err)
		}
	}
	if raw, ok, err := scalar(keyCreated); err != nil {
		return err
	} else if ok && raw != "" {
		stamp, perr := time.Parse(time.RFC3339Nano, raw)
		if perr != nil {
			return fmt.Errorf("spool: frontmatter %q must be an RFC3339 timestamp, got %q: %w", keyCreated, raw, perr)
		}
		m.Created = stamp
	}
	if node, ok := mappingGet(m.head, keyStructured); ok && node.Tag != "!!null" {
		if err := node.Decode(&m.Structured); err != nil {
			return fmt.Errorf("spool: frontmatter %q must be a mapping: %w", keyStructured, err)
		}
	}
	return nil
}

// Encode renders the message back to file bytes.
//
// Keys this build does not know about are preserved verbatim, in place, with
// their comments, because the encoder PATCHES the mapping the parse retained
// rather than marshalling a struct over the top of it. Marshalling a struct
// would be the shorter implementation and would silently delete every field a
// newer peer added — the forward-compatibility ruling exists precisely to
// forbid that.
func (m *Message) Encode() ([]byte, error) {
	head := m.head
	if head == nil {
		head = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	v := m.V
	if v == 0 {
		v = CurrentVersion
	}
	set := func(key string, value any, keep bool) error {
		if !keep {
			mappingDelete(head, key)
			return nil
		}
		node := &yaml.Node{}
		if err := node.Encode(value); err != nil {
			return fmt.Errorf("spool: encoding frontmatter %q: %w", key, err)
		}
		mappingSet(head, key, node)
		return nil
	}
	created := ""
	if !m.Created.IsZero() {
		created = m.Created.UTC().Format(time.RFC3339Nano)
	}
	fields := []struct {
		key   string
		value any
		keep  bool
	}{
		{keyV, v, true},
		{keyID, m.ID, m.ID != ""},
		{keyKind, m.Kind, true},
		{keyFromHarp, m.FromHarp, m.FromHarp != ""},
		{keyTo, m.To, m.To != ""},
		{keyInReplyTo, m.InReplyTo, m.InReplyTo != ""},
		{keyOriginID, m.OriginID, m.OriginID != ""},
		{keyCreated, created, created != ""},
		{keyTTLSeconds, m.TTLSeconds, m.TTLSeconds != 0},
		{keyStructured, m.Structured, len(m.Structured) > 0},
	}
	for _, f := range fields {
		if err := set(f.key, f.value, f.keep); err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(head); err != nil {
		return nil, fmt.Errorf("spool: encoding message frontmatter: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("spool: encoding message frontmatter: %w", err)
	}
	var out bytes.Buffer
	out.WriteString(frontMatterFence)
	out.WriteByte('\n')
	out.Write(buf.Bytes())
	if buf.Len() > 0 && !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		out.WriteByte('\n')
	}
	out.WriteString(frontMatterFence)
	out.WriteByte('\n')
	out.WriteString(m.Body)
	return out.Bytes(), nil
}

// documentMapping unwraps a decoded document to its root mapping node,
// tolerating an empty document.
func documentMapping(doc *yaml.Node) *yaml.Node {
	node := doc
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		}
		node = node.Content[0]
	}
	if node.Kind == 0 {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	return node
}

// mappingGet returns the value node for key.
func mappingGet(mapping *yaml.Node, key string) (*yaml.Node, bool) {
	if mapping == nil {
		return nil, false
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1], true
		}
	}
	return nil, false
}

// mappingSet replaces key's value IN PLACE when it exists (preserving its
// position and the key node's comments) and appends otherwise.
func mappingSet(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

// mappingDelete removes key and its value if present.
func mappingDelete(mapping *yaml.Node, key string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}

// UnknownKeys reports the frontmatter keys this build does not know about, in
// document order. It exists so a consumer can LOG what it is carrying blind
// rather than discovering the skew only when something misbehaves.
func (m *Message) UnknownKeys() []string {
	if m.head == nil {
		return nil
	}
	var out []string
	for i := 0; i+1 < len(m.head.Content); i += 2 {
		key := m.head.Content[i].Value
		known := false
		for _, k := range knownKeys {
			if k == key {
				known = true
				break
			}
		}
		if !known {
			out = append(out, key)
		}
	}
	return out
}

// splitFrontMatter separates a leading YAML frontmatter block from the body.
//
// The block is recognised only when the very first line is exactly "---" and
// ends at the FIRST later line that is exactly "---"; everything after that is
// the body, copied verbatim. Two properties this repo has been bitten by and
// which the tests pin: a body containing its own "---" line is not corrupted
// (only the first closing fence terminates the block), and nothing here
// templates or expands anything, so a body containing "{{ }}" survives.
//
// An UNTERMINATED block is not frontmatter — treating it as one would swallow
// the whole document into metadata.
//
// (Deliberate small duplication of internal/content's unexported splitter:
// this package's layering is paths+harp only, and exporting content's private
// helper to share ~40 lines would couple the message substrate to the bundle
// content model.)
func splitFrontMatter(raw []byte) (head []byte, body string, found bool) {
	text := string(raw)
	rest, ok := cutFenceLine(text)
	if !ok {
		return nil, text, false
	}
	offset := 0
	for offset <= len(rest) {
		lineEnd := strings.IndexByte(rest[offset:], '\n')
		var line string
		var next int
		if lineEnd < 0 {
			line, next = rest[offset:], len(rest)
		} else {
			line, next = rest[offset:offset+lineEnd], offset+lineEnd+1
		}
		if strings.TrimRight(line, " \t\r") == frontMatterFence {
			return []byte(rest[:offset]), rest[next:], true
		}
		if lineEnd < 0 {
			break
		}
		offset = next
	}
	return nil, text, false
}

// cutFenceLine consumes a leading fence line, reporting whether there was one.
func cutFenceLine(text string) (rest string, ok bool) {
	lineEnd := strings.IndexByte(text, '\n')
	if lineEnd < 0 {
		return "", false
	}
	if strings.TrimRight(text[:lineEnd], " \t\r") != frontMatterFence {
		return "", false
	}
	return text[lineEnd+1:], true
}
