package coord

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// auditEntry is c.audit's durable, no-projection payload (facts.go).
type auditEntry struct {
	Kind   string            `json:"kind"`
	Actor  string            `json:"actor,omitempty"`
	Detail map[string]string `json:"detail,omitempty"`
}

// readAuditKind reads interactions.jsonl straight off disk and returns every
// audit entry of the given kind, oldest first. The audit journal has no
// fold/query API by design (facts.go: "an audit log with no projection"), so
// reading the file IS the only way to assert what was recorded.
func readAuditKind(t *testing.T, c *Coordinator, kind string) []auditEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(c.stateDir, "interactions.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []auditEntry
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var f Fact
		require.NoError(t, json.Unmarshal([]byte(line), &f))
		if f.Kind != "interaction" {
			continue
		}
		var e auditEntry
		require.NoError(t, json.Unmarshal(f.Data, &e))
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}
