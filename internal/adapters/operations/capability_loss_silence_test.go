package operations

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/engines/mock"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/engines"
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
// genuinely declares a hook mock cannot carry. The loud arm must report
// it: testing only the silenced arm would let the DEFAULT rot into silence,
// which is the accident this setting exists to make deliberate.
func TestCapabilityLoss_SilenceOptOut(t *testing.T) {
	loud, _ := materializeHookFixture(t)

	lossy := CapabilityLoss(engines.Registry(), loud, string(mock.NameLossy), []string{"reviewer"})
	if len(lossy) == 0 {
		t.Fatal("precondition: the fixture must produce a real declared loss on mock")
	}

	if got := CapabilityLoss(engines.Registry(), silencedFrom(t, loud), string(mock.NameLossy), []string{"reviewer"}); got != nil {
		t.Fatalf("silenced config still reported loss: %v", got)
	}
}

// TestMaterialize_SilenceOptOut pins the same switch on the path that
// PRINTS the "NOT carried" lines, since that is where a user meets it.
func TestMaterialize_SilenceOptOut(t *testing.T) {
	loud, target := materializeHookFixture(t)

	res, err := materializeOne(t, loud, MaterializeRequest{
		Profiles: []string{"reviewer"}, Target: target, Engines: []string{string(mock.NameLossy)},
	})
	if err != nil {
		t.Fatalf("Materialize (loud): %v", err)
	}
	if len(res.NotCarried) == 0 {
		t.Fatal("precondition: the loud arm must report the declared loss")
	}

	quiet, qtarget := materializeHookFixture(t)
	res2, err := materializeOne(t, silencedFrom(t, quiet), MaterializeRequest{
		Profiles: []string{"reviewer"}, Target: qtarget, Engines: []string{string(mock.NameLossy)},
	})
	if err != nil {
		t.Fatalf("Materialize (silenced): %v", err)
	}
	if len(res2.NotCarried) != 0 {
		t.Fatalf("silenced materialize still reported loss: %v", res2.NotCarried)
	}
}
