package memory

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// StampPlanFile ensures the file's YAML frontmatter contains `sessions:`
// with harpName included. Idempotent: re-running with the same harp is a
// no-op. Preserves all other frontmatter fields and the body verbatim.
//
// Behavior:
//   - File starts with `---\n...\n---\n` → parse frontmatter, add harp to
//     `sessions:` list if absent, rewrite atomically.
//   - File has no frontmatter → prepend a minimal one with just our session.
//   - Malformed YAML, or an unterminated frontmatter block → the file is
//     left untouched (never corrupt it by guessing) AND a non-nil error is
//     returned (U078-F11: it used to be a silent nil-error no-op here, which
//     made this comment's own "caller logs" claim false — the caller,
//     hook_stamp_plan.go, only logs when err != nil).
//   - Harp already present in `sessions:` → a genuine no-op (nil, file
//     untouched) — the one case above that is NOT a failure.
//   - Empty file → prepend frontmatter, body remains empty.
//
// Designed to be invoked from a PostFileEdit hook with stdin-supplied JSON,
// but takes a raw path so it's also unit-testable and CLI-callable.
func StampPlanFile(path, harpName string) error {
	if harpName == "" {
		return fmt.Errorf("harpName required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)

	// The rewrite is atomic (temp file + rename), so the replacement carries
	// whatever mode it is given rather than the original's. Stamping is a
	// metadata edit to a document the user owns and must not change its
	// permissions: a plan kept at 0600 stays at 0600, an executable-bit-carrying
	// file keeps it. A stat failure is not fatal — the file was readable a line
	// ago — so fall back to the historical 0644.
	mode := os.FileMode(0o644)
	if st, sErr := os.Stat(path); sErr == nil {
		mode = st.Mode().Perm()
	}

	if !strings.HasPrefix(content, "---\n") {
		return prependFrontmatter(path, content, harpName, mode)
	}
	return updateFrontmatter(path, content, harpName, mode)
}

// prependFrontmatter writes a new frontmatter block ahead of content that has
// none, preserving a single newline gap before the body for readability.
func prependFrontmatter(path, content, harpName string, mode os.FileMode) error {
	prefix := fmt.Sprintf("---\nsessions:\n  - %s\n---\n", harpName)
	if !strings.HasPrefix(content, "\n") && content != "" {
		prefix += "\n"
	}
	return iox.WriteFileAtomic(path, []byte(prefix+content), mode)
}

// updateFrontmatter parses content's leading frontmatter, adds harpName to its
// sessions list, and rewrites the file. It bails (no change) on a malformed or
// unterminated block, or when the harp is already present. yaml.Node is used so
// unknown keys, comments, key order, and scalar styles round-trip verbatim.
func updateFrontmatter(path, content, harpName string, mode os.FileMode) error {
	rest := content[len("---\n"):]
	var block, body string
	switch {
	case rest == "---" || strings.HasPrefix(rest, "---\n"):
		// Empty, immediately-closed frontmatter (`---\n---\n...`): the opening
		// `---\n` consumed the newline that would precede the closing `---`, so
		// there is no "\n---" to find. The block is empty; synthesize the
		// sessions mapping rather than misclassifying this as unterminated.
		block = ""
		body = strings.TrimLeft(strings.TrimPrefix(rest, "---"), "\n")
	default:
		end := strings.Index(rest, "\n---")
		if end < 0 {
			// U078-F11: a genuine failure to stamp, not a silent no-op — the
			// file is still left untouched (never corrupt an unterminated
			// block by guessing where it ends), but the caller must be able
			// to tell "nothing changed because there was nothing to change"
			// (harp already present, below) apart from "nothing changed
			// because this file couldn't be parsed at all."
			return fmt.Errorf("stamp: %s: opening frontmatter `---` has no closing `---`; refusing to guess where it ends", path)
		}
		block = rest[:end]
		// Normalize leading body whitespace so re-stamping yields exactly one
		// blank line between the closing `---` and the first body line.
		body = strings.TrimLeft(rest[end+len("\n---"):], "\n")
	}

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(block), &root); err != nil {
		// U078-F11: same reasoning as the unterminated-block case above —
		// "refuse to corrupt" and "silently do nothing" are different
		// outcomes, and only the first was ever intended.
		return fmt.Errorf("stamp: %s: frontmatter is not valid YAML, refusing to modify it: %w", path, err)
	}
	if !addHarpToSessionsNode(&root, harpName) {
		return nil // already present, no change — a genuine no-op
	}

	rendered, err := encodeFrontmatter(&root)
	if err != nil {
		return fmt.Errorf("encode frontmatter: %w", err)
	}

	newContent := "---\n" + strings.TrimRight(rendered, "\n") + "\n---\n"
	if body != "" {
		newContent += "\n" + body
	}
	return iox.WriteFileAtomic(path, []byte(newContent), mode)
}

// encodeFrontmatter renders a parsed frontmatter document back to YAML. Both
// the document write and the stream close are checked: whatever the encoder
// leaves in the buffer after either fails is a partial document, and the only
// use of this string is to be written over the user's plan file. An error here
// must abort that write, never shorten it.
func encodeFrontmatter(root *yaml.Node) (string, error) {
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		_ = enc.Close()
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// addHarpToSessionsNode looks for a top-level `sessions:` key in the parsed
// frontmatter and either appends harpName (if missing) or no-ops (if
// present). If the key is absent or has a non-sequence type, it's
// replaced/inserted with a fresh single-element sequence.
//
// Returns true when the document was modified.
func addHarpToSessionsNode(root *yaml.Node, harpName string) bool {
	// root.Kind == DocumentNode wrapping a MappingNode (the frontmatter map).
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		// Empty frontmatter — synthesize the mapping.
		mapping := &yaml.Node{Kind: yaml.MappingNode}
		mapping.Content = []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "sessions"},
			{Kind: yaml.SequenceNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: harpName}}},
		}
		root.Kind = yaml.DocumentNode
		root.Content = []*yaml.Node{mapping}
		return true
	}
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return false
	}

	// Mapping nodes alternate key/value in Content. Find `sessions`.
	for i := 0; i < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		if key.Value != "sessions" {
			continue
		}
		value := mapping.Content[i+1]
		if value.Kind != yaml.SequenceNode {
			// Replace with a fresh sequence containing our harp.
			mapping.Content[i+1] = &yaml.Node{
				Kind:    yaml.SequenceNode,
				Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: harpName}},
			}
			return true
		}
		for _, item := range value.Content {
			if item.Kind == yaml.ScalarNode && item.Value == harpName {
				return false
			}
		}
		value.Content = append(value.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: harpName})
		return true
	}

	// No `sessions:` key — append one.
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "sessions"},
		&yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: harpName}}},
	)
	return true
}
