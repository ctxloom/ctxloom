//go:build arch

package arch

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// reservedStartRunFields are the StartRun wire fields nothing reads on either
// side of the runner channel. Each is asserted reserved by number and absent
// by name.
var reservedStartRunFields = map[string]int{
	"task_id": 1,
	"budget":  5,
}

const coordinationProtoPath = "internal/adapters/coordgrpc/pb/coordination.proto"

// TestArch_StartRunReservesDeadFields asserts the
// coordination proto reserves each dead StartRun field by number and no
// longer declares it by name, so neither side of the runner channel can
// silently repopulate a field the other never reads.
func TestArch_StartRunReservesDeadFields(t *testing.T) {
	reserved, declared := parseStartRunFields(startRunMessageBody(t))
	if len(declared) == 0 {
		t.Fatal("StartRun parsed to zero fields — the message shape changed, not the wire")
	}
	for name, num := range reservedStartRunFields {
		if !reserved[num] {
			t.Errorf("StartRun does not reserve field %d (%s) — a reserved number is what stops a later field reusing it", num, name)
		}
		if got, ok := declared[name]; ok {
			t.Errorf("StartRun still declares %s = %d — nothing on either side reads it; reserve the number instead", name, got)
		}
	}
}

// parseStartRunFields reads the message body's reserved numbers and its
// declared fields by name.
func parseStartRunFields(body []string) (map[int]bool, map[string]int) {
	reserved := map[int]bool{}
	reservedRe := regexp.MustCompile(`^\s*reserved\s+([0-9,\s]+);`)
	fieldRe := regexp.MustCompile(`^\s*(?:optional\s+|repeated\s+)?[\w.]+\s+(\w+)\s*=\s*(\d+)\s*;`)
	declared := map[string]int{}
	for _, line := range body {
		if m := reservedRe.FindStringSubmatch(line); m != nil {
			for _, n := range strings.Split(m[1], ",") {
				if n = strings.TrimSpace(n); n != "" {
					reserved[digitsValue(n)] = true
				}
			}
			continue
		}
		if m := fieldRe.FindStringSubmatch(line); m != nil {
			declared[m[1]] = digitsValue(m[2])
		}
	}
	return reserved, declared
}

// digitsValue is the decimal value of a run of ASCII digits.
func digitsValue(s string) int {
	var v int
	for _, c := range s {
		v = v*10 + int(c-'0')
	}
	return v
}

// startRunMessageBody returns the lines between `message StartRun {` and its
// closing brace.
func startRunMessageBody(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(moduleRoot(t), coordinationProtoPath))
	if err != nil {
		t.Fatalf("open %s: %v", coordinationProtoPath, err)
	}
	defer f.Close()
	var body []string
	inMsg := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case !inMsg && strings.HasPrefix(strings.TrimSpace(line), "message StartRun {"):
			inMsg = true
		case inMsg && strings.TrimSpace(line) == "}":
			return body
		case inMsg:
			body = append(body, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("message StartRun not found in %s", coordinationProtoPath)
	return nil
}
