package operations

import (
	"fmt"
	"path/filepath"
	"strings"
)

// requireLoadableName refuses an import whose SOURCE basename the corresponding
// loader could never find again.
//
// Every import in this package writes the source's basename into the target
// directory verbatim, and every loader that later reads that directory filters
// by extension. So importing "seed.txt" wrote a file nothing would ever open:
// exit 0, Status "imported", zero usable content, and with --force a real file
// of the same basename destroyed to make room for it (U081-F10). "Nothing
// happened because the name was unusable" is a failure, not a success.
//
// The accepted extensions differ by kind and are passed in rather than assumed,
// because they genuinely differ: bundles.Loader.Find only ever stats
// "<name>.yaml" or "<name>/bundle.yaml", while the profile scan accepts ".yaml"
// and ".yml" alike. Guessing one answer for both would either reject loadable
// profiles or admit unloadable bundles.
func requireLoadableName(sourcePath, kind string, accepted ...string) error {
	ext := strings.ToLower(filepath.Ext(sourcePath))
	for _, ok := range accepted {
		if ext == ok {
			return nil
		}
	}
	return fmt.Errorf("import %s: a %s file must be named %s — %q would be written to a path the loader never looks at, so nothing would ever read it",
		sourcePath, kind, humanExtList(accepted), filepath.Base(sourcePath))
}

// humanExtList renders [".yaml", ".yml"] as "*.yaml or *.yml".
func humanExtList(exts []string) string {
	globs := make([]string, len(exts))
	for i, e := range exts {
		globs[i] = "*" + e
	}
	switch len(globs) {
	case 0:
		return "(nothing)"
	case 1:
		return globs[0]
	default:
		return strings.Join(globs[:len(globs)-1], ", ") + " or " + globs[len(globs)-1]
	}
}
