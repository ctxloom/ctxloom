package config

import (
	"fmt"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config/migrate/fromv1"
	"github.com/ctxloom/ctxloom/internal/config/migrate/fromv2"
	"github.com/ctxloom/ctxloom/internal/config/migrate/fromv3"
	"github.com/ctxloom/ctxloom/internal/config/migrate/fromv4"
	"github.com/ctxloom/ctxloom/internal/config/migrate/fromv5"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// versionKey is the top-level integer schema-version field on config.yaml.
const versionKey = "version"

// migrationSink collects the lossy-migration diagnostics raised while ONE
// config load's upgrade pipeline runs (a user-set value a step had to drop).
// The Upgrader interface has no warning channel and no access to the Config
// being built, so the lossy steps record into a sink THREADED through the
// upgraders that can drop a value; loadConfigLayer builds a fresh sink per load
// and drains it into cfg.warnings (kind migration-lossy) so the strict startup
// gate can abort on a silently dropped setting.
//
// It is per-load precisely so concurrent loads — which now exist (the
// delegation concurrency ceiling admits concurrent child spawns, each
// re-loading config; Manager.Update loads twice per transaction) — never
// attribute one config's dropped setting to another, or lose it entirely, the
// way a single package-global buffer drained by whichever load finished first
// did (U049-F14).
type migrationSink struct {
	mu   sync.Mutex
	msgs []string
}

func (s *migrationSink) record(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, fmt.Sprintf(format, args...))
}

func (s *migrationSink) drain() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.msgs
	s.msgs = nil
	return out
}

// CurrentConfigVersion is the config *schema* version ctxloom writes and
// upgrades toward. It is an integer, deliberately distinct from the application
// version (cmd.Version, a string like "v0.6.4"). Bump it whenever a new Upgrader
// is appended to configUpgrades. A config with no `version` is treated as the
// pre-versioning generation (version 0/1) and is upgraded on load.
const CurrentConfigVersion = 6

// agentProfileCanonicalizeUpgrade rewrites a per-remote SHORT profile ref stored
// under agents.<name>.profiles ("<remote>/<bundle>#profiles/<name>") to its
// canonical URL form ("<url>@bundles/<bundle>#profiles/<name>"). SetAgent once
// stored these verbatim, so a machine-local alias could persist into config.yaml;
// decision B makes canonical the stored form. This is the one-time
// canonicalize-on-load migration for those already-stored short entries.
//
// It is registry-DEPENDENT (the alias→URL map lives in .ctxloom/remotes.yaml), so
// unlike the version-stamped node upgrades above it is threaded an aliasToURL
// resolver at the load call site (see loadConfigFile) rather than living in the
// static configUpgrades pipeline. It is deliberately NOT version-gated: like the
// directory-profile bundleRefCanonicalizeUpgrade it is naturally idempotent —
// canonical, ctxloom:local, and bare/local refs pass through unchanged, so only
// a resolvable short ref changes — and safe to run every load. When the resolver
// is nil (no registry) it self-gates to a no-op, leaving short refs verbatim for
// the read-path loader to resolve (fault tolerant).
type agentProfileCanonicalizeUpgrade struct {
	aliasToURL func(string) string
}

// Name identifies the upgrade in logs and the rewrite prompt.
func (agentProfileCanonicalizeUpgrade) Name() string { return "canonicalize agent profile refs" }

// Apply canonicalizes each agents.<name>.profiles sequence, reporting whether it
// changed anything. A missing agents map, a non-mapping agent entry, or a missing
// profiles sequence is skipped.
func (u agentProfileCanonicalizeUpgrade) Apply(root *yaml.Node) (changed bool) {
	if u.aliasToURL == nil {
		return false
	}
	agentsNode := upgrade.MapValue(root, "agents")
	if agentsNode == nil || agentsNode.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(agentsNode.Content); i += 2 {
		agent := agentsNode.Content[i+1]
		if agent.Kind != yaml.MappingNode {
			continue
		}
		seq := upgrade.MapValue(agent, "profiles")
		if seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		for _, item := range seq.Content {
			if item.Kind != yaml.ScalarNode {
				continue
			}
			if canonical := remote.CanonicalizeProfileShortRef(item.Value, u.aliasToURL); canonical != item.Value {
				item.Value = canonical
				changed = true
			}
		}
	}
	return changed
}

// newConfigUpgrades builds the ordered, registry-free upgrade pipeline, oldest
// SOURCE version first. Each step lives in its own package under
// internal/config/migrate, named for the config version it migrates OFF, and
// carries its own tests — so retiring support for a source version is deleting
// that directory and the one line here that names it.
//
// sink collects the lossy steps' dropped-setting diagnostics; it is bound per
// load so one config's loss is never attributed to another.
func newConfigUpgrades(sink *migrationSink) upgrade.Pipeline {
	report := upgrade.Reporter(nil)
	if sink != nil {
		report = sink.record
	}
	return upgrade.Pipeline{
		fromv1.Upgrade{},
		fromv2.Upgrade{Report: report},
		fromv3.Upgrade{Report: report},
		fromv4.Upgrade{},
		fromv5.Upgrade{Report: report},
	}
}
