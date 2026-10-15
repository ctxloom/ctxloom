package configload

import (
	"errors"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// CheckDocument validates doc — a config document, or the fragment of one a
// write is about to persist — against the config schema, and refuses it with
// the SAME per-key diagnostics a config file's load reports (unknown keys
// with their near-miss suggestion, one violation per key path, no noise from
// the backend alternatives a typed entry did not choose). A writer calls it
// before persisting, so a value the next load would refuse is never written.
//
// configPath only names the file in the messages.
func CheckDocument(configPath string, doc map[string]any) error {
	validator, err := newConfigValidator()
	if err != nil {
		return fmt.Errorf("config schema: %w", err)
	}
	data, err := yamlx.Marshal(doc)
	if err != nil {
		return fmt.Errorf("render %s for validation: %w", configPath, err)
	}
	verr := validator.ValidateBytes(data)
	if verr == nil {
		return nil
	}
	var errs []error
	for _, w := range classifyValidationError(configPath, validator, verr) {
		errs = append(errs, errors.New(w.Text))
	}
	return errors.Join(errs...)
}
