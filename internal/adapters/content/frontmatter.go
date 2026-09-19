package content

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontMatterFence is the delimiter line.
const frontMatterFence = "---"

// splitFrontMatter separates a leading YAML front-matter block from the body.
//
// The block is recognised only when the very first line is exactly "---"; it
// ends at the FIRST subsequent line that is exactly "---". Everything after
// that is the body, returned verbatim.
//
// Two properties this repo has been bitten by, and which the tests pin:
//
//   - A body containing its own "---" line is not corrupted, because only the
//     first closing fence terminates the block and the remainder is copied
//     without further scanning.
//   - A body containing "{{ }}" is not corrupted, because nothing here
//     templates or expands anything.
//
// found=false means the document has no front-matter at all; body is then the
// whole input.
func splitFrontMatter(raw []byte) (fm []byte, body string, found bool) {
	text := string(raw)
	rest, ok := cutFenceLine(text)
	if !ok {
		return nil, text, false
	}
	// Scan line by line for the closing fence.
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
	// An unterminated block is not front-matter. Treating it as one would
	// swallow the whole document into metadata.
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

// joinFrontMatter renders a metadata block and a body into one .md component.
//
// When meta marshals to nothing, the front-matter block is normally omitted —
// EXCEPT when the body itself starts with a fence line, in which case an empty
// block is emitted anyway. Without that, a metadata-less document whose body
// opens with "---" would be read back with its first paragraph parsed as
// metadata: a silent corruption of exactly the kind splitFrontMatter is written
// to avoid.
func joinFrontMatter(meta any, body string) ([]byte, error) {
	var fm []byte
	if meta != nil {
		encoded, err := marshalYAML(meta)
		if err != nil {
			return nil, err
		}
		fm = encoded
	}
	bodyOpensWithFence := false
	if rest, ok := cutFenceLine(body); ok {
		bodyOpensWithFence = true
		_ = rest
	}
	if len(fm) == 0 && !bodyOpensWithFence {
		return []byte(body), nil
	}
	var buf bytes.Buffer
	buf.WriteString(frontMatterFence)
	buf.WriteByte('\n')
	buf.Write(fm)
	if len(fm) > 0 && !bytes.HasSuffix(fm, []byte("\n")) {
		buf.WriteByte('\n')
	}
	buf.WriteString(frontMatterFence)
	buf.WriteByte('\n')
	buf.WriteString(body)
	return buf.Bytes(), nil
}

// marshalYAML encodes a metadata struct, returning nil for a struct that
// produces an empty mapping (yaml.v3 renders that as "{}\n").
//
// Indentation is pinned to 2 explicitly rather than left to the package default:
// the encoded bytes land inside a signed component, so a library default change
// would silently reshape every digest.
func marshalYAML(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("content: encoding metadata: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("content: encoding metadata: %w", err)
	}
	out := buf.Bytes()
	if bytes.Equal(bytes.TrimSpace(out), []byte("{}")) {
		return nil, nil
	}
	return out, nil
}

// unmarshalYAML decodes YAML into v, tolerating empty input.
func unmarshalYAML(data []byte, v any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("content: decoding metadata: %w", err)
	}
	return nil
}
