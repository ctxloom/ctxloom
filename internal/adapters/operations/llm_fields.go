package operations

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// An llm entry's fields beyond type and model (context_window, an opencode
// provider block, model_options, permissions.approver, ...) are written by
// dotted key rather than one flag per field: the schema already says which
// fields each engine's entry takes and what each holds, so the write applies
// the keys as given and lets the schema refuse the result (checkLLMEntry),
// and a field an engine adds needs no CLI change.

// llmUnsettableKeys are the entry keys --set does not reach: type is checked
// against the registry by its own flag, and role is the shipped registry's
// marker, not a user setting.
var llmUnsettableKeys = map[string]string{
	"type": "set type with --type",
	"role": "role is not set from the CLI",
}

// parseLLMField splits one "<dotted.key>=<value>" assignment. The value is
// read as YAML, so 131072 is a number, true a boolean, and a flow mapping
// ({enable_thinking: false}) an object; an empty value (or null) removes
// the key.
func parseLLMField(assignment string) (path []string, value any, err error) {
	key, raw, ok := strings.Cut(assignment, "=")
	if !ok || key == "" {
		return nil, nil, fmt.Errorf("--set %q: want <dotted.key>=<value>", assignment)
	}
	path = strings.Split(key, ".")
	for _, seg := range path {
		if seg == "" {
			return nil, nil, fmt.Errorf("--set %q: empty segment in key %q", assignment, key)
		}
	}
	if why, refused := llmUnsettableKeys[path[0]]; refused {
		return nil, nil, fmt.Errorf("--set %q: %s", assignment, why)
	}
	if err := yaml.Unmarshal([]byte(raw), &value); err != nil {
		return nil, nil, fmt.Errorf("--set %q: value is not YAML: %w", assignment, err)
	}
	return path, value, nil
}

// applyLLMFields applies each "<dotted.key>=<value>" assignment to entry as
// written in config.yaml, in order, creating intermediate objects as needed;
// a nil value removes the key and any object it leaves empty.
func applyLLMFields(entry config.LLMConfig, assignments []string) (config.LLMConfig, error) {
	doc, err := llmEntryDoc(entry)
	if err != nil {
		return entry, err
	}
	for _, a := range assignments {
		path, value, err := parseLLMField(a)
		if err != nil {
			return entry, err
		}
		if err := setPath(doc, path, value); err != nil {
			return entry, fmt.Errorf("--set %q: %w", a, err)
		}
	}
	raw, err := yamlx.Marshal(doc)
	if err != nil {
		return entry, err
	}
	var out config.LLMConfig
	if err := yaml.Unmarshal(raw, &out); err != nil {
		return entry, err
	}
	if len(out.Body) == 0 {
		out.Body = nil
	}
	return out, nil
}

// llmEntryDoc renders entry as the mapping config.yaml holds for it.
func llmEntryDoc(entry config.LLMConfig) (map[string]any, error) {
	raw, err := yamlx.Marshal(entry)
	if err != nil {
		return nil, err
	}
	doc := map[string]any{}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

// setPath writes value at path inside m (nil deletes), reporting a path that
// runs through a non-object value.
func setPath(m map[string]any, path []string, value any) error {
	key := path[0]
	if len(path) == 1 {
		if value == nil {
			delete(m, key)
		} else {
			m[key] = value
		}
		return nil
	}
	child, present := m[key]
	if !present {
		if value == nil {
			return nil
		}
		child = map[string]any{}
	}
	next, ok := child.(map[string]any)
	if !ok {
		return fmt.Errorf("%s holds %T, not an object", key, child)
	}
	if err := setPath(next, path[1:], value); err != nil {
		return err
	}
	if len(next) == 0 {
		delete(m, key)
	} else {
		m[key] = next
	}
	return nil
}

// checkLLMEntry refuses an entry the next load would refuse: the config
// schema with its own diagnostics, then the label's engine for the engine
// keys of its permissions block, which the schema leaves to the engine.
func checkLLMEntry(reg engine.Registry, typ, configPath, label string, entry config.LLMConfig) error {
	doc, err := llmEntryDoc(entry)
	if err != nil {
		return fmt.Errorf("llm %q: %w", label, err)
	}
	wrapped := map[string]any{"llm": map[string]any{"configs": map[string]any{label: doc}}}
	if err := configload.CheckDocument(configPath, wrapped); err != nil {
		return fmt.Errorf("llm %q not written:\n%w", label, err)
	}
	if len(entry.Permissions.Engine) == 0 {
		return nil
	}
	return validateLabelEngineKeys(reg, typ, label, entry.Permissions.Engine)
}
