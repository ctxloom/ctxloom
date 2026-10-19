package isolation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrImageNotBuilt is MissingCommands' answer when the agent image is not
// present in the runtime: there is nothing to look inside, which is not the
// same fact as "nothing is missing". A caller reports it as information (the
// image is built on first launch), never as a gap.
var ErrImageNotBuilt = errors.New("the agent image is not built")

// commandProbeTimeout bounds one inspect or one probe container. The probe
// container runs a shell over a handful of names, so a longer wait is a wedged
// daemon, not slow work.
const commandProbeTimeout = 60 * time.Second

// commandResults memoizes one probe outcome per (runtime binary, image ID,
// sorted command list). An image ID names immutable content, so the answer
// cannot change for as long as the image exists; keying on the ID (not the
// tag) is what makes a rebuilt image under the same tag miss the cache. It is
// in-process: a doctor run is one process, and a probe container per agent
// sharing an image is the only cost worth avoiding.
var commandResults sync.Map // string -> []string

// MissingCommands reports which of names do not resolve (`command -v`) inside
// this container's agent image, in the order given. It runs the probe in a
// throwaway container with no network against the image ALREADY in the
// runtime — it never builds or pulls — and returns ErrImageNotBuilt when the
// image is absent.
func (c Container) MissingCommands(ctx context.Context, names []string) ([]string, error) {
	names = commandNames(names)
	if len(names) == 0 {
		return nil, nil
	}
	if c.runtime == nil || c.runtime.Binary() == "" {
		return nil, fmt.Errorf("probe commands in %s: no container runtime", c.image)
	}
	id, err := c.imageID(ctx)
	if err != nil {
		return nil, err
	}
	key := c.runtime.Binary() + "\x00" + id + "\x00" + sortedKey(names)
	if cached, ok := commandResults.Load(key); ok {
		return missingInOrder(names, cached.([]string)), nil
	}
	cctx, cancel := context.WithTimeout(ctx, commandProbeTimeout)
	defer cancel()
	out, err := probeExec(cctx, c.runtime.Binary(), []string{
		"run", "--rm", "--network", "none", "--entrypoint", "sh", c.image, "-c", missingScript(names),
	})
	if err != nil {
		return nil, fmt.Errorf("probe commands in %s: %w", c.image, err)
	}
	missing := strings.Fields(out)
	commandResults.Store(key, missing)
	return missingInOrder(names, missing), nil
}

// imageID is the image's ID in the runtime, or ErrImageNotBuilt when the
// runtime has no such image.
func (c Container) imageID(ctx context.Context) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, commandProbeTimeout)
	defer cancel()
	out, err := probeExec(cctx, c.runtime.Binary(), c.runtime.imageInspectArgs("{{.Id}}", c.image))
	id := strings.TrimSpace(out)
	if err != nil || id == "" {
		return "", ErrImageNotBuilt
	}
	return id, nil
}

// missingScript is the shell the probe container runs: it prints each name
// that does not resolve, one per line. Every name reaches the shell as a
// single-quoted literal (shellQuote), so a config value is never interpreted.
func missingScript(names []string) string {
	var b strings.Builder
	for _, n := range names {
		q := shellQuote(n)
		fmt.Fprintf(&b, "command -v -- %s >/dev/null 2>&1 || echo %s\n", q, q)
	}
	return b.String()
}

// shellQuote renders s as one POSIX single-quoted word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// commandNames drops blanks and duplicates, keeping the caller's order. A name
// holding whitespace cannot be a single executable and would split in the
// probe's output, so it is dropped too: the callers hand over the first word
// of a command.
func commandNames(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if n == "" || seen[n] || strings.ContainsAny(n, " \t\r\n") {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

func sortedKey(names []string) string {
	s := append([]string(nil), names...)
	sort.Strings(s)
	return strings.Join(s, "\x00")
}

// missingInOrder filters names to those in missing, keeping names' order, so a
// cached answer for the same set in a different order reads identically.
func missingInOrder(names, missing []string) []string {
	in := map[string]bool{}
	for _, m := range missing {
		in[m] = true
	}
	var out []string
	for _, n := range names {
		if in[n] {
			out = append(out, n)
		}
	}
	return out
}
