package config

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// migrationLossyText returns the concatenated migration-lossy warning text from
// a loaded config.
func migrationLossyText(cfg *Config) string {
	var b strings.Builder
	for _, w := range cfg.warnings {
		if w.Kind == WarnKindMigrationLossy {
			b.WriteString(w.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestConcurrentLoads_LossyMigrationWarningsDoNotCross pins U049-F14: the lossy
// migration diagnostics used to accumulate in a single package-global slice
// drained by whichever load finished first, so two concurrent loads — which now
// exist (concurrent child spawns re-load config; Manager.Update loads twice per
// transaction) — could attribute one config's dropped setting to another, or
// lose it. Each load now threads its OWN sink, so every load sees exactly and
// only its own warning. Runs under -race (just test-pkg), which also catches the
// shared-slice access the old global exposed.
func TestConcurrentLoads_LossyMigrationWarningsDoNotCross(t *testing.T) {
	// Config A drops a compaction model with no label; config B drops the
	// gemini-only trust_workspace knob. The two warnings are disjoint in text.
	const (
		markerA = "dropped compaction model"
		markerB = "trust_workspace"
	)
	configA := "llm:\n  compaction:\n    model: haiku-A\n"
	configB := "version: 3\nllm:\n  configs:\n    gem:\n      type: gemini\n      trust_workspace: true\n"

	newFS := func(body string) afero.Fs {
		fs := afero.NewMemMapFs()
		appDir := "/proj/" + paths.AppDirName
		require.NoError(t, fs.MkdirAll(appDir, 0o755))
		require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(body), 0o644))
		return fs
	}

	const iterations = 60
	var wg sync.WaitGroup
	errs := make(chan error, iterations*2)

	check := func(body, want, notWant string) {
		defer wg.Done()
		appDir := "/proj/" + paths.AppDirName
		cfg, err := LoadFresh(WithFS(newFS(body)), WithAppDir(appDir))
		if err != nil {
			errs <- err
			return
		}
		got := migrationLossyText(cfg)
		if !strings.Contains(got, want) {
			errs <- fmt.Errorf("load missing its own warning %q; got: %q", want, got)
			return
		}
		if strings.Contains(got, notWant) {
			errs <- fmt.Errorf("load saw the OTHER load's warning %q (crossed); got: %q", notWant, got)
		}
	}

	for i := 0; i < iterations; i++ {
		wg.Add(2)
		go check(configA, markerA, markerB)
		go check(configB, markerB, markerA)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}
