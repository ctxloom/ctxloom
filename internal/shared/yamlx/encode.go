package yamlx

import (
	"bytes"

	"gopkg.in/yaml.v3"
)

// Marshal is the one way this module serializes YAML: two-space indentation,
// whether v is a value or a parsed *yaml.Node (whose comments and styles it
// keeps). Every writer and the upgrade write-back (upgrade.Encode) go through
// it, so a file upgraded by --write-upgrades and the same file saved normally
// are the same bytes; two encoders would rewrite each other's output in full
// with no content change. The lint rule forbidding yaml.Marshal and
// yaml.NewEncoder elsewhere, tests included (.golangci.yml, forbidigo), is
// what holds this; its one exclusion is pkg/clifmt, which renders command
// output rather than a file ctxloom saves.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		_ = enc.Close()
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
