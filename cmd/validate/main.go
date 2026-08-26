// validate is the build-time preflight gate: config YAML files conform to
// their JSON schemas, and the version stamp the build is about to bake in is
// well-formed. Run before build to catch both early.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/schema"
	"github.com/ctxloom/ctxloom/internal/version"
)

// stampEnv carries the version string the build is about to stamp in. Empty
// means the caller did not ask for a stamp check, which keeps every existing
// invocation working unchanged.
const stampEnv = "CTXLOOM_VERSION_STAMP"

// checkStamp refuses a malformed version stamp before it is baked into a
// binary.
//
// A build that cannot determine its commit used to interpolate an empty field
// and exit 0 -- "v0.7.0--20260826T043946" -- producing a binary that cannot
// name the commit it came from. That defeats the rule this project verifies by:
// run checks against a binary built from the tree under test, because a stale
// binary plus an exit-code check agrees with anything. version.ValidStamp is
// the single authority on the shape; this gate is only where it is enforced.
func checkStamp() error {
	stamp := os.Getenv(stampEnv)
	if stamp == "" {
		return nil
	}
	if !version.ValidStamp(stamp) {
		return fmt.Errorf("refusing to build: version stamp %q is malformed (want v<major>.<minor>.<patch>-<short-sha>-<YYYYMMDDTHHMMSS>[-dirty]).\n"+
			"A stamp with an empty field means the build could not determine its commit -- in a linked git worktree, check that versionator is new enough to resolve one (see .devcontainer/tool-versions.env)", stamp)
	}
	return nil
}

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
	// The stamp gate runs FIRST: a malformed stamp means the binary about to be
	// produced cannot identify itself, and there is no point validating
	// documents for a build that must not ship.
	if err := checkStamp(); err != nil {
		return err
	}
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
