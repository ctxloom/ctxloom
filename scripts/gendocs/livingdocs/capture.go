package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/doccapture"
)

// DocCapture and DocCaptureStep are this package's names for the shared
// living-docs wire contract (internal/shared/doccapture) tests/acceptance/
// steps_doc_capture.go writes per scenario (one file per pickle) when
// CTXLOOM_DOC_CAPTURE_DIR is set (these used to be a second,
// independently-declared copy of that writer's structs, held in sync only by
// a comment demanding field-for-field agreement). Aliased, not copied, so
// every existing field access below is unaffected.
type DocCapture = doccapture.DocCapture
type DocCaptureStep = doccapture.DocCaptureStep

// LoadCaptures reads every *.json file in dir and groups them by scenario
// name (a Scenario Outline's Examples rows share a name across several
// files, each a separate DocCapture in the returned slice, in filename
// order). A missing dir is not an error — it means the generator ran without
// CTXLOOM_DOC_CAPTURE_DIR having produced anything, and every scenario simply
// renders as "not captured".
func LoadCaptures(dir string) (map[string][]DocCapture, error) {
	result := map[string][]DocCapture{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		var cap DocCapture
		if err := json.Unmarshal(data, &cap); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		result[cap.Scenario] = append(result[cap.Scenario], cap)
	}
	return result, nil
}
