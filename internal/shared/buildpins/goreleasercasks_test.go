package buildpins

import (
	"testing"

	"gopkg.in/yaml.v3"
)

const goreleaserPath = "../../../.goreleaser.yml"

// TestGoreleaserCasks_SkipPrereleaseUpload (owner ruling #7b, 2026-10-12):
// every Homebrew cask carries `skip_upload: auto`, so a prerelease tag
// (v0.7.0-rc.1) reaches no brew user. The ctxloom cask was once the
// exception.
func TestGoreleaserCasks_SkipPrereleaseUpload(t *testing.T) {
	var cfg struct {
		Casks []struct {
			Name       string `yaml:"name"`
			SkipUpload string `yaml:"skip_upload"`
		} `yaml:"homebrew_casks"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, goreleaserPath)), &cfg); err != nil {
		t.Fatalf("parse %s: %v", goreleaserPath, err)
	}
	if len(cfg.Casks) == 0 {
		t.Fatalf("%s: found no homebrew_casks", goreleaserPath)
	}
	for _, c := range cfg.Casks {
		if c.SkipUpload != "auto" {
			t.Errorf("%s: cask %q has skip_upload %q, want \"auto\" so a prerelease reaches no brew user", goreleaserPath, c.Name, c.SkipUpload)
		}
	}
}
