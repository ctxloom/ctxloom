package isolation

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Unique-layer size is the one prune figure that is honest about shared
// layers — tags × Size overstates a leak several-fold, because a hundred
// superseded images share one base. Neither CLI puts it on `images`; both
// print it only from `system df -v`, and in different shapes: docker renders
// JSON with humanized strings, podman refuses --format with -v and prints a
// table. Hence one reader per runtime, behind Runtime.imageUniqueSizes.

// imageUniqueSizes reads docker's `system df -v` JSON.
func (d Docker) imageUniqueSizes(ctx context.Context) (map[string]int64, error) {
	return dockerUniqueSizes(ctx, d.Binary())
}

// imageUniqueSizes reads podman's `system df -v` table.
func (p Podman) imageUniqueSizes(ctx context.Context) (map[string]int64, error) {
	out, err := probeExec(ctx, p.Binary(), []string{"system", "df", "-v"})
	if err != nil {
		return nil, fmt.Errorf("%s system df: %w", p.Binary(), err)
	}
	return parsePodmanUniqueSizes(out), nil
}

// dockerUniqueSizes runs and parses docker's disk-usage JSON: one object whose
// Images each carry ID and a humanized UniqueSize.
func dockerUniqueSizes(ctx context.Context, binary string) (map[string]int64, error) {
	out, err := probeExec(ctx, binary, []string{"system", "df", "-v", "--format", "{{json .}}"})
	if err != nil {
		return nil, fmt.Errorf("%s system df: %w", binary, err)
	}
	var df struct {
		Images []struct {
			ID         string `json:"ID"`
			UniqueSize string `json:"UniqueSize"`
		} `json:"Images"`
	}
	if err := json.Unmarshal([]byte(out), &df); err != nil {
		return nil, fmt.Errorf("%s system df: %w", binary, err)
	}
	sizes := map[string]int64{}
	for _, img := range df.Images {
		if n, ok := parseHumanSize(img.UniqueSize); ok {
			sizes[shortImageID(img.ID)] = n
		}
	}
	return sizes, nil
}

// podmanColumns splits a podman table row: columns are separated by two or
// more spaces, while a value ("7 days") holds single ones.
var podmanColumns = regexp.MustCompile(`\s{2,}`)

// parsePodmanUniqueSizes reads the "Images space usage" table of `podman
// system df -v`: IMAGE ID is the third column, UNIQUE SIZE the second to last.
func parsePodmanUniqueSizes(out string) map[string]int64 {
	sizes := map[string]int64{}
	inImages := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "REPOSITORY"):
			inImages = true
			continue
		case line == "":
			if inImages && len(sizes) > 0 {
				return sizes
			}
			continue
		case !inImages:
			continue
		}
		cols := podmanColumns.Split(line, -1)
		if len(cols) < 5 {
			continue
		}
		if n, ok := parseHumanSize(cols[len(cols)-2]); ok {
			sizes[shortImageID(cols[2])] = n
		}
	}
	return sizes
}

// humanUnits are the decimal suffixes both CLIs render sizes in.
var humanUnits = map[string]float64{
	"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15,
}

// humanSize matches a rendered size such as "42.1MB" or "0B".
var humanSize = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([kKMGTP]?B)$`)

// parseHumanSize reads a CLI-rendered decimal size back into bytes; the
// renderer kept three or four significant digits, so this is as exact as the
// runtime's own report and no more. ok is false for "N/A" or anything else
// unrecognized.
func parseHumanSize(s string) (int64, bool) {
	m := humanSize.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return int64(f * humanUnits[m[2]]), true
}
