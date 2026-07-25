package tui

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// exportEntry is the NDJSON export DTO — snake_case mirroring the session
// index / watch contract so frontend tooling reads one vocabulary.
type exportEntry struct {
	Type          string          `json:"type"`
	TimestampUnix int64           `json:"timestamp_unix,omitempty"`
	Content       string          `json:"content,omitempty"`
	ToolName      string          `json:"tool_name,omitempty"`
	ToolInput     json.RawMessage `json:"tool_input,omitempty"`
	ToolOutput    string          `json:"tool_output,omitempty"`
	IsError       bool            `json:"is_error,omitempty"`
	Sidechain     bool            `json:"sidechain,omitempty"`
}

// transcriptNDJSON renders the feed as one JSON object per line.
func transcriptNDJSON(items []feedItem) ([]byte, error) {
	var b strings.Builder
	for _, it := range items {
		if it.role == "notice" {
			continue // viewer chrome, not transcript content
		}
		e := exportEntry{
			Type:       it.role,
			Content:    it.text,
			ToolName:   it.toolName,
			ToolOutput: it.toolOutput,
			IsError:    it.isError,
			Sidechain:  it.sidechain,
		}
		if !it.ts.IsZero() {
			e.TimestampUnix = it.ts.Unix()
		}
		if it.toolInput != "" {
			e.ToolInput = json.RawMessage(it.toolInput)
		}
		line, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// transcriptText renders the feed as the same role-tagged text the viewer
// shows, fully expanded.
func transcriptText(items []feedItem, width int) []byte {
	var b strings.Builder
	for _, it := range items {
		if it.role == "notice" {
			continue
		}
		for _, l := range renderItem(it, width, true) {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	return []byte(b.String())
}

// errEmptyTranscript is returned when the feed renders to zero bytes — an
// empty feed, or one holding nothing but viewer chrome (notices, which both
// renderers skip). Writing that file and reporting "saved <path>" would be a
// success message over zero delivered bytes, so the export is refused
// instead.
var errEmptyTranscript = errors.New("nothing to export: the feed holds no transcript content")

// exportTranscript writes the feed under the harp's session dir and returns
// the path. kind is "txt" (rendered, s) or "ndjson" (raw, S), and is the ONLY
// permitted set: kind picks the renderer AND supplies the filename extension,
// so the switch below must stay exhaustive. It used to have a `default:` arm
// falling through to the text renderer, which meant any other kind wrote
// role-tagged plain text into a file whose extension claimed otherwise
// (kind "json" -> transcript-<harp>-<ts>.json full of text) and still reported
// "saved <path>". A file that lies about its own contents is worse than a
// refused export, so an unrecognised kind is now an error.
//
// It returns errEmptyTranscript rather than writing a 0-byte file.
func exportTranscript(dir, harp, kind string, items []feedItem, now time.Time) (string, error) {
	var data []byte
	var err error
	switch kind {
	case "ndjson":
		data, err = transcriptNDJSON(items)
	case "txt":
		data = transcriptText(items, 100)
	default:
		return "", fmt.Errorf("unknown transcript export kind %q (supported: txt, ndjson)", kind)
	}
	if err != nil {
		return "", err
	}
	// Both renderers skip notices, so a feed of nothing but viewer chrome
	// renders to zero bytes just as an empty feed does. Refuse rather than
	// write the file and report "saved <path>" over it.
	if len(data) == 0 {
		return "", errEmptyTranscript
	}
	path := filepath.Join(dir, fmt.Sprintf("transcript-%s-%s.%s", harp, now.UTC().Format("20060102T150405"), kind))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// osc52Copy encodes text as an OSC 52 clipboard write — it works over SSH
// because the TERMINAL applies it. Terminals that refuse OSC 52 ignore the
// sequence silently, which is why the caller pairs it with a file fallback.
func osc52Copy(text string) []byte {
	enc := base64.StdEncoding.EncodeToString([]byte(text))
	return []byte("\x1b]52;c;" + enc + "\x07")
}

// copyText is the selected material for y: the cursor entry when the feed
// pane is focused, the entire visible feed otherwise.
func copyText(items []feedItem, cursor int, feedFocused bool) string {
	if feedFocused && cursor >= 0 && cursor < len(items) {
		return strings.Join(renderItem(items[cursor], 100, true), "\n")
	}
	var parts []string
	for _, it := range items {
		if it.role == "notice" {
			continue
		}
		parts = append(parts, strings.Join(renderItem(it, 100, true), "\n"))
	}
	return strings.Join(parts, "\n")
}
