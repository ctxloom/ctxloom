// validate checks that config YAML files conform to their JSON schemas.
// Run before build to catch schema violations early.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/schema"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "validation failed: %v\n", err)
		os.Exit(1)
	}
}

// target is one document the gate checks. optional means "may legitimately be
// absent"; every other target must exist.
type target struct {
	path     string
	optional bool
}

// defaultTargets is what a build-time run checks, relative to the repo root.
func defaultTargets() []target {
	return []target{
		{path: paths.ConfigPath(paths.AppDirName), optional: true},
		{path: filepath.Join("resources", "default-config.yaml")},
		{path: filepath.Join("resources", "example-config.yaml")},
		{path: filepath.Join("resources", "init-config.yaml")},
	}
}

func run() error {
	n, err := validateAll(defaultTargets())
	if err != nil {
		return err
	}
	fmt.Printf("Validated %d document(s) against schema\n", n)
	return nil
}

// validateAll checks every target against the embedded config schema and
// reports how many documents it actually validated.
//
// It fails rather than reporting a hollow success in three cases:
//
//   - a REQUIRED target that is absent — the gate's own inputs are missing, so
//     it cannot have gated anything;
//   - a target that exists but cannot be READ — a directory at the path, wrong
//     permissions, an I/O error. The old `if err == nil` treated every one of
//     these as "the file isn't there";
//   - validating zero documents. "Nothing was there" and "the gate did its job"
//     used to be the same exit code and the same neutral stdout line; the whole
//     point of a pre-build gate is that the second one is earned.
//
// Only fs.ErrNotExist on an OPTIONAL target is a legitimate skip — the project
// config is gitignored and simply does not exist in a fresh clone.
func validateAll(targets []target) (int, error) {
	configValidator, err := schema.NewConfigValidator()
	if err != nil {
		return 0, err
	}
	var validated int
	for _, tg := range targets {
		data, err := os.ReadFile(tg.path)
		switch {
		case errors.Is(err, fs.ErrNotExist) && tg.optional:
			continue
		case errors.Is(err, fs.ErrNotExist):
			return validated, fmt.Errorf("required document %s is missing", tg.path)
		case err != nil:
			return validated, fmt.Errorf("read %s: %w", tg.path, err)
		}
		if err := configValidator.ValidateBytes(data); err != nil {
			return validated, fmt.Errorf("schema validation error in %s: %w", tg.path, err)
		}
		fmt.Printf("Validated %s\n", tg.path)
		validated++
	}
	if validated == 0 {
		return 0, fmt.Errorf("validated no documents: the schema gate checked nothing, so it guarantees nothing")
	}
	return validated, nil
}
