package memory

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/textutil"
)

// MaxNextStepBytes bounds a stored next step. It is measured in BYTES because
// textutil.Ellipsize is, and a rune count would not bound the file.
//
// The bound exists because the writer is a TurnEnd hook copying an assistant
// message verbatim: a turn that ends by pasting a whole file into its reply
// would otherwise leave that file on disk under the harp, and hand it to the
// distiller as a "task hint" that swamps the prompt it is supposed to steer.
// The hint's job is to name an INTENTION, and an intention that cannot be
// stated in 4KB is not one the distiller can act on either.
const MaxNextStepBytes = 4096

// ErrEmptyNextStep refuses a write with nothing in it.
//
// Refusing is the load-bearing behaviour, not a courtesy check. The file is
// OVERWRITTEN every turn, so a write of empty text would erase a good next
// step captured a turn earlier and replace it with a file that reads as "this
// session intends nothing" — indistinguishable, downstream, from a session
// that never captured one. A turn the hook cannot read a next step out of
// leaves the previous turn's answer standing instead.
var ErrEmptyNextStep = errors.New("next step is empty: refusing to overwrite the harp's captured next step with nothing")

// WriteNextStep stores text as harpName's next step, replacing any previous
// one. Empty (or whitespace-only) text is refused with ErrEmptyNextStep; see
// there for why that is not a silent no-op.
func WriteNextStep(harpName, text string) error {
	bounded := boundNextStep(text)
	if bounded == "" {
		return ErrEmptyNextStep
	}
	path, err := paths.HarpNextStepPath(harpName)
	if err != nil {
		return fmt.Errorf("resolve next-step path for %s: %w", harpName, err)
	}
	dir, err := paths.HarpDir(harpName)
	if err != nil {
		return fmt.Errorf("resolve harp dir for %s: %w", harpName, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create harp dir %s: %w", dir, err)
	}
	if err := iox.WriteFileAtomic(path, []byte(bounded), 0o644); err != nil {
		return fmt.Errorf("write next step %s: %w", path, err)
	}
	return nil
}

// ReadNextStep returns harpName's captured next step and whether there is one.
//
// A missing file is NOT an error and reports ("", false): a session that has
// not finished a turn yet has no next step, and that is the ordinary case on
// the first distill of a fresh harp. Every other failure — an unresolvable
// harp, an unreadable file, a file holding only whitespace — reports the same
// ("", false), because the single question this answers is whether a usable
// hint is available, and there is no caller that could act on the difference.
func ReadNextStep(harpName string) (string, bool) {
	path, err := paths.HarpNextStepPath(harpName)
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	// Bounded on the way out as well as in. The cap is applied by one function
	// at both boundaries so there is a single policy rather than two: the file
	// is ordinary state on disk, and a reader that trusted its size would be
	// bounded only for as long as this process was the only writer.
	text := boundNextStep(string(data))
	if text == "" {
		return "", false
	}
	return text, true
}

// boundNextStep is the ONE place the next-step bound is applied — both when
// storing and when loading. It trims surrounding whitespace first so that a
// message of nothing but blank lines is recognized as empty rather than
// stored as a hint with no content in it.
func boundNextStep(s string) string {
	return textutil.Ellipsize(strings.TrimSpace(s), MaxNextStepBytes)
}
