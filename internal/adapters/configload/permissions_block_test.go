package configload

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
)

// parseRefusal loads doc and returns the parse warning that refused it,
// failing unless the refusal left NOTHING of the document loaded: a
// config that does not parse is not applied in part (configload's
// convention for a document that fails to decode).
func parseRefusal(t *testing.T, doc string) string {
	t.Helper()
	cfg := loadYAML(t, doc)
	_, loaded := cfg.Agent("a")
	assert.Falsef(t, loaded, "the refused document must not load: %s", doc)
	for _, w := range cfg.GetWarnings() {
		if w.Kind == config.WarnKindParse {
			return w.Text
		}
	}
	t.Fatalf("no parse refusal for %s", doc)
	return ""
}

// A permissions block is a privilege grant, so a key the block does not
// know refuses the whole document rather than being dropped the way an
// unknown key elsewhere is: a `dney:` that vanished would leave the denial
// undeclared while the launch reports success. A binding's non-neutral key
// must be an engine's block; the project knows no engine at all.
func TestLoad_PermissionsBlockRefusesAnUnknownKey(t *testing.T) {
	assert.Contains(t, parseRefusal(t, "version: 6\nagents:\n  a:\n    llm: e\n    permissions:\n      dney: [Bash]\n"),
		`"dney" is not a neutral key`)
	assert.Contains(t, parseRefusal(t, "version: 6\nagents:\n  a:\n    llm: e\npermissions:\n  aprover: none\n"),
		"the project's permissions take only approver, approval_timeout, sandbox, network")
	assert.Contains(t, parseRefusal(t, "version: 6\nagents:\n  a:\n    llm: e\npermissions:\n  mode: bypass\n"),
		"declare it at agents.<name>.permissions.<engine>.mode")
}

func TestLoad_PermissionsScalarIsRefusedWithTheBlockSpelling(t *testing.T) {
	assert.Contains(t, parseRefusal(t, "version: 6\nagents:\n  a:\n    llm: e\n    permissions: bypass\n"), "permissions: {<engine>: {mode: bypass}}")
}

func TestLoad_PermissionsBlockLoads(t *testing.T) {
	cfg := loadYAML(t, "version: 6\nagents:\n  a:\n    llm: e\n    permissions:\n      approver: none\n      approval_timeout: 20m\n      sandbox: full\n      network: false\n      e:\n        mode: plan\n        after_plan: acceptEdits\n        deny: [\"Bash(rm *)\"]\n")
	a, ok := cfg.Agent("a")
	require.True(t, ok)
	assert.Equal(t, map[string]map[string]any{"e": {"mode": "plan", "after_plan": "acceptEdits", "deny": []any{"Bash(rm *)"}}}, a.Permissions.Engines)
	assert.Equal(t, "none", a.Permissions.Approver)
	assert.Empty(t, unknownKeyWarnings(cfg), "every key of a full block is known to the schema")
	for _, w := range cfg.GetWarnings() {
		assert.NotContains(t, w.Text, "permissions", "a valid block raises no schema warning: %s", w.Text)
	}
}
