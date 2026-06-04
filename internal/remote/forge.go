package remote

import (
	"fmt"
	"net/url"
	"strings"
)

// ForgeConfig is the discriminator+union envelope for one labeled forge
// instance, mirroring config.LLMConfig exactly. Type names the adapter
// (github / git); Body carries that adapter's endpoint and auth fields
// (base_url, api_url, token_env), decoded by the resolution path. The map
// label keying this entry is an arbitrary user string with no semantics;
// the adapter is determined ONLY by Type.
type ForgeConfig struct {
	Type string         `yaml:"type,omitempty"`
	Body map[string]any `yaml:",inline"`
}

// stringField returns the named Body field as a trimmed string, or "" when
// absent or not a string.
func (f ForgeConfig) stringField(key string) string {
	if v, ok := f.Body[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// BaseURL is the forge endpoint host this instance serves (used for
// remote→forge host matching and as the github API base for enterprise).
func (f ForgeConfig) BaseURL() string { return f.stringField("base_url") }

// APIURL is an explicit github REST API endpoint (enterprise). Empty means
// derive it from BaseURL. The git adapter ignores it.
func (f ForgeConfig) APIURL() string { return f.stringField("api_url") }

// TokenEnv is the name of the environment variable holding this forge's
// token. Empty falls back to the type default (GITHUB_TOKEN for github).
func (f ForgeConfig) TokenEnv() string { return f.stringField("token_env") }

// DefaultGitHubTokenEnv is the env var a github forge reads its token from
// when token_env is unset.
const DefaultGitHubTokenEnv = "GITHUB_TOKEN"

// builtinForges are the always-available forge instances, so the common case
// needs no forges: block: github.com via the rich GitHub adapter, and a
// catch-all generic git adapter for every other host.
func builtinForges() map[string]ForgeConfig {
	return map[string]ForgeConfig{
		string(ForgeGitHub):     {Type: string(ForgeGitHub)},
		string(ForgeGitGeneric): {Type: string(ForgeGitGeneric)},
	}
}

// ResolvedForge is the concrete forge a remote binds to: the adapter type,
// its endpoint, and the env var its token comes from.
type ResolvedForge struct {
	Type     ForgeType
	BaseURL  string
	APIURL   string
	TokenEnv string
}

// resolveForge picks the forge for a remote against a set of configured
// forge instances. Resolution order, highest priority first:
//
//  1. explicit remote.Forge label → forges[label]
//  2. URL host matches a configured forge's base_url
//  3. built-in github for github.com (and owner/repo shorthand)
//  4. generic git for any other host
//
// forges may be nil; built-in defaults always apply.
func resolveForge(remoteURL, forgeLabel string, forges map[string]ForgeConfig) ResolvedForge {
	if forgeLabel != "" {
		if fc, ok := forges[forgeLabel]; ok {
			return resolvedFromConfig(remoteURL, fc)
		}
	}

	host := forgeHost(remoteURL)
	if host != "" {
		for _, fc := range forges {
			if bh := forgeHost(fc.BaseURL()); bh != "" && bh == host {
				return resolvedFromConfig(remoteURL, fc)
			}
		}
	}

	forgeType, base, err := DetectForge(remoteURL)
	if err != nil {
		forgeType, base = ForgeGitGeneric, ""
	}
	return resolvedFromConfig(remoteURL, ForgeConfig{
		Type: string(forgeType),
		Body: map[string]any{"base_url": base},
	})
}

// resolvedFromConfig fills a ResolvedForge from a ForgeConfig, applying the
// per-type token-env default (GITHUB_TOKEN for github) and falling back to
// the remote's own host as the base URL when the config omits one.
func resolvedFromConfig(remoteURL string, fc ForgeConfig) ResolvedForge {
	forgeType := ForgeType(fc.Type)
	if forgeType == "" {
		forgeType = ForgeGitGeneric
	}

	base := fc.BaseURL()
	if base == "" {
		if _, detected, err := DetectForge(remoteURL); err == nil {
			base = detected
		}
	}

	tokenEnv := fc.TokenEnv()
	if tokenEnv == "" && forgeType == ForgeGitHub {
		tokenEnv = DefaultGitHubTokenEnv
	}

	return ResolvedForge{
		Type:     forgeType,
		BaseURL:  base,
		APIURL:   fc.APIURL(),
		TokenEnv: tokenEnv,
	}
}

// forgeHost returns the lowercased host of a URL, stripping a www. prefix so
// github.com and www.github.com match. Returns "" for shorthand owner/repo
// or unparseable input.
func forgeHost(raw string) string {
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		if !strings.Contains(raw, ".") {
			return ""
		}
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// ResolveForgeForURLWith resolves a forge for a bare URL against an explicit
// forge set (already merged with built-ins by the caller). It is the
// package-level entry point the registry methods wrap.
func ResolveForgeForURLWith(remoteURL, forgeLabel string, forges map[string]ForgeConfig) ResolvedForge {
	return resolveForge(remoteURL, forgeLabel, forges)
}

// MergeForges overlays user-configured forges onto the built-in defaults so
// a user can override github/git or add named instances while the common
// case still resolves with no configuration.
func MergeForges(user map[string]ForgeConfig) map[string]ForgeConfig {
	merged := builtinForges()
	for name, fc := range user {
		merged[name] = fc
	}
	return merged
}

// validateForgeConfig reports a clear error for an unknown adapter type so a
// typo'd forge surfaces at load rather than silently degrading to git.
func validateForgeConfig(name string, fc ForgeConfig) error {
	switch ForgeType(fc.Type) {
	case ForgeGitHub, ForgeGitGeneric, "":
		return nil
	default:
		return fmt.Errorf("forge %q: unknown type %q (want %q or %q)",
			name, fc.Type, ForgeGitHub, ForgeGitGeneric)
	}
}
