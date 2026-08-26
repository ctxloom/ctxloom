package operations

import (
	"context"
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
)

// silencedFrom returns the same config with capability-loss reporting silenced,
// so a test's two arms differ in exactly one setting.
func silencedFrom(t *testing.T, cfg *config.Config) *config.Config {
	t.Helper()
	f := cfg.ToFixture()
	f.Settings.SilenceUnsupported = true
	return config.NewFixture(f)
}

// TestCapabilityLoss_SilenceOptOut pins BOTH arms against a profile that
// genuinely declares a hook opencode cannot carry. The loud arm must report
// it: testing only the silenced arm would let the DEFAULT rot into silence,
// which is the accident this setting exists to make deliberate.
func TestCapabilityLoss_SilenceOptOut(t *testing.T) {
	loud, _ := materializeHookFixture(t)

	lossy := CapabilityLoss(loud, "opencode", []string{"reviewer"})
	if len(lossy) == 0 {
		t.Fatal("precondition: the fixture must produce a real declared loss on opencode")
	}

	if got := CapabilityLoss(silencedFrom(t, loud), "opencode", []string{"reviewer"}); got != nil {
		t.Fatalf("silenced config still reported loss: %v", got)
	}
}

// TestMaterializeProfile_SilenceOptOut pins the same switch on the path that
// PRINTS the "NOT carried" lines, since that is where a user meets it.
func TestMaterializeProfile_SilenceOptOut(t *testing.T) {
	loud, target := materializeHookFixture(t)

	res, err := MaterializeProfile(context.Background(), loud, MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: target, Backend: "opencode",
	})
	if err != nil {
		t.Fatalf("MaterializeProfile (loud): %v", err)
	}
	if len(res.NotCarried) == 0 {
		t.Fatal("precondition: the loud arm must report the declared loss")
	}

	quiet, qtarget := materializeHookFixture(t)
	res2, err := MaterializeProfile(context.Background(), silencedFrom(t, quiet), MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: qtarget, Backend: "opencode",
	})
	if err != nil {
		t.Fatalf("MaterializeProfile (silenced): %v", err)
	}
	if len(res2.NotCarried) != 0 {
		t.Fatalf("silenced materialize still reported loss: %v", res2.NotCarried)
	}
}
