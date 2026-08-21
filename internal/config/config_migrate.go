package config

import (
	"fmt"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

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

// newConfigUpgrades builds the ordered, registry-free upgrade pipeline. It is
// deliberately EMPTY: a config older than CurrentConfigVersion is now REFUSED
// rather than repaired in place (see the refusal in loadConfigLayer).
//
// The frame around it is intact on purpose — the pipeline, the per-load sink,
// and the pending-upgrade consent path that asks before rewriting a user's
// file. Re-introducing an upgrade is appending one Upgrader here, oldest SOURCE
// version first, in its own package under internal/config/migrate named for the
// version it migrates OFF. Rebuilding the consent UX is the expensive half, and
// it is what this empty frame preserves.
//
// sink collects a lossy step's dropped-setting diagnostics; it is bound per
// load so one config's loss is never attributed to another. Nothing writes to
// it while the pipeline is empty.
func newConfigUpgrades(sink *migrationSink) upgrade.Pipeline {
	report := upgrade.Reporter(nil)
	if sink != nil {
		report = sink.record
	}
	_ = report
	return upgrade.Pipeline{}
}

// declaredConfigVersion reports the `version` a raw config document declares and
// whether the key was present at all. Absent, non-integer and unparseable all
// read as version 0 with declared=false: each means "this file does not say it
// is current", which is the only question the caller asks.
func declaredConfigVersion(data []byte) (version int, declared bool) {
	var doc struct {
		Version *int `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil || doc.Version == nil {
		return 0, false
	}
	return *doc.Version, true
}
