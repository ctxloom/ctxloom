package tasks

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// StampPlanFile ensures the file's YAML frontmatter contains `sessions:`
// with harpName included. Idempotent: re-running with the same harp is a
// no-op. Preserves all other frontmatter fields and the body verbatim.
//
// Behavior:
//   - File starts with `---\n...\n---\n` → parse frontmatter, add harp to
//     `sessions:` list if absent, rewrite atomically.
//   - File has no frontmatter → prepend a minimal one with just our session.
//   - Malformed YAML frontmatter → no-op (caller logs; we don't want a
//     stamping hook to corrupt user files).
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

	if !strings.HasPrefix(content, "---\n") {
		return prependFrontmatter(path, content, harpName)
	}
	return updateFrontmatter(path, content, harpName)
}

// prependFrontmatter writes a new frontmatter block ahead of content that has
// none, preserving a single newline gap before the body for readability.
func prependFrontmatter(path, content, harpName string) error {
	prefix := fmt.Sprintf("---\nsessions:\n  - %s\n---\n", harpName)
	if !strings.HasPrefix(content, "\n") && content != "" {
		prefix += "\n"
	}
	return atomicWriteString(path, prefix+content)
}

// updateFrontmatter parses content's leading frontmatter, adds harpName to its
// sessions list, and rewrites the file. It bails (no change) on a malformed or
// unterminated block, or when the harp is already present. yaml.Node is used so
// unknown keys, comments, key order, and scalar styles round-trip verbatim.
func updateFrontmatter(path, content, harpName string) error {
	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil // opening `---` but no closing — bail without modifying
	}
	block := rest[:end]
	// Normalize leading body whitespace so re-stamping yields exactly one
	// blank line between the closing `---` and the first body line.
	body := strings.TrimLeft(rest[end+len("\n---"):], "\n")

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(block), &root); err != nil {
		return nil // malformed; refuse to corrupt
	}
	if !addHarpToSessionsNode(&root, harpName) {
		return nil // already present, no change
	}

	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return fmt.Errorf("encode frontmatter: %w", err)
	}
	_ = enc.Close()

	newContent := "---\n" + strings.TrimRight(buf.String(), "\n") + "\n---\n"
	if body != "" {
		newContent += "\n" + body
	}
	return atomicWriteString(path, newContent)
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

func atomicWriteString(path, content string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
