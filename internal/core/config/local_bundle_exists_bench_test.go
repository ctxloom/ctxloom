package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// localBundleProject sizes a project for measuring the local-file-wins oracle
// (Config.LocalBundleExists): local bundles of fragments, commands and
// profiles on the OS filesystem, and project-bundle profiles whose refs are
// spelled "<alias>/<bundle>" — every such ref is one oracle call per loader.
type localBundleProject struct {
	bundles, fragments, profiles, refsPerProfile int
}

func (p localBundleProject) String() string {
	return fmt.Sprintf("bundles=%d/fragments=%d/profiles=%d/refs=%d", p.bundles, p.fragments, p.profiles, p.refsPerProfile)
}

// build writes the project under a temp dir and returns its config; resolve
// binds the alias table, which is what wires the oracle into the loader.
func (p localBundleProject) build(b *testing.B, resolve bool) *Config {
	b.Helper()
	fs := afero.NewOsFs()
	appDir := filepath.Join(b.TempDir(), ".ctxloom")
	root := paths.BundlesLayoutRoot(paths.LocalBundlesPath(appDir), paths.LayoutV2)
	body := strings.Repeat("A paragraph of guidance an agent reads. ", 50)
	for i := range p.bundles {
		var doc strings.Builder
		fmt.Fprintf(&doc, "version: \"1.0\"\ndescription: team bundle %d\nfragments:\n", i)
		for j := range p.fragments {
			fmt.Fprintf(&doc, "  frag%d:\n    content: %q\n", j, body)
		}
		doc.WriteString("commands:\n  go:\n    content: run it\nprofiles:\n  dev:\n    description: a dev profile\n")
		bundletree.Write(b, fs, root, fmt.Sprintf("team/b%d", i), doc.String())
	}
	dir := bundletree.ProjectProfilesDirFS(b, fs, appDir)
	for i := range p.profiles {
		var doc strings.Builder
		fmt.Fprintf(&doc, "description: profile %d\nbundles:\n", i)
		for j := range p.refsPerProfile {
			fmt.Fprintf(&doc, "  - team/b%d\n", (i+j)%p.bundles)
		}
		require.NoError(b, safefs.WriteFile(fs, filepath.Join(dir, fmt.Sprintf("p%d.yaml", i)), []byte(doc.String()), 0o644))
	}
	builder := NewBuilder(fs, true, appDir, SourceProject)
	if resolve {
		builder.BindProfileResolvers(func(alias string) string {
			if alias == "team" {
				return "https://github.com/acme/team"
			}
			return ""
		})
	}
	return builder.Build()
}

var localBundleProjects = []localBundleProject{
	{bundles: 6, fragments: 10, profiles: 8, refsPerProfile: 4},
	{bundles: 20, fragments: 30, profiles: 20, refsPerProfile: 6},
}

// BenchmarkLocalBundleExists is one oracle call: what each "<alias>/<bundle>"
// ref costs a loader, and each ref a profile create or update stores.
func BenchmarkLocalBundleExists(b *testing.B) {
	for _, p := range localBundleProjects {
		b.Run(p.String(), func(b *testing.B) {
			cfg := p.build(b, true)
			for b.Loop() {
				if !cfg.LocalBundleExists("team/b0") {
					b.Fatal("the local bundle was not found")
				}
			}
		})
	}
}

// BenchmarkGetProfileLoader is a loader with the oracle wired (alias table
// bound) against one without it: the difference is what the oracle costs.
func BenchmarkGetProfileLoader(b *testing.B) {
	for _, p := range localBundleProjects {
		for _, resolve := range []bool{true, false} {
			b.Run(fmt.Sprintf("%s/oracle=%t", p, resolve), func(b *testing.B) {
				cfg := p.build(b, resolve)
				for b.Loop() {
					if _, err := cfg.GetProfileLoader().Load("p0"); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
