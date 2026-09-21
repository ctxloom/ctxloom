package operations

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestResolveSetupPrompt_RejectedCompanionGuidanceWithheld: setup guidance IS
// an exposure surface (text that reaches an agent with tool access, at init,
// possibly unattended), so it rides the same gate every sibling surface does
// — and in that gate a human's rejection beats the companion exemption. A
// companion whose loadout the user has rejected contributes nothing to the
// init prompt, while the built-in is still delivered whole: withholding a
// contribution must never block setup.
func TestResolveSetupPrompt_RejectedCompanionGuidanceWithheld(t *testing.T) {
	testsupport.Isolate(t)
	fakeCompanions(t, map[string]string{
		"ltk": "init:\n  setup_guidance: REJECTED-COMPANION-GUIDANCE\n",
	})
	appDir, _ := regenTestApp(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	cfg.BindTrustForTesting(compositetest.Trust(compositetest.RejectWhen(func(_ trust.Ref, payload []byte) bool {
		return bytes.Contains(payload, []byte("REJECTED-COMPANION-GUIDANCE"))
	})))

	got := ResolveSetupPrompt(published(t, cfg), "BUILTIN-DEFAULT")

	assert.NotContains(t, got, "REJECTED-COMPANION-GUIDANCE",
		"a rejected companion's setup guidance reached the init prompt")
	assert.Contains(t, got, "BUILTIN-DEFAULT",
		"withholding a contribution must not block setup; the built-in still composes")
}
