package claude

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// APIKeyEnv and AuthTokenEnv are claude's other credential vars: a
// pay-per-use key, and a gateway's bearer (ANTHROPIC_AUTH_TOKEN with
// ANTHROPIC_BASE_URL).
const (
	APIKeyEnv    = "ANTHROPIC_API_KEY"
	AuthTokenEnv = "ANTHROPIC_AUTH_TOKEN"
	// ProfileEnv names an Anthropic profile, which claude reads ahead of a
	// /login credential when set.
	ProfileEnv = "ANTHROPIC_PROFILE"
)

// claude's credential precedence, as documented
// (https://code.claude.com/docs/en/authentication, "Authentication
// precedence"): a cloud-provider switch, then ANTHROPIC_AUTH_TOKEN, then
// ANTHROPIC_API_KEY, then apiKeyHelper, then CLAUDE_CODE_OAUTH_TOKEN, then a
// named ANTHROPIC_PROFILE, then the /login credential. A mode is only what it
// says when everything that would outrank or replace it is gone, so each
// mode UNSETS the others' variables rather than trusting an empty value.

// providerSwitches select a cloud provider and outrank every other
// credential. From the Bedrock, Claude Platform on AWS, Google Vertex and
// Microsoft Foundry pages of https://code.claude.com/docs/en/.
var providerSwitches = []string{
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_MANTLE",
	"CLAUDE_CODE_USE_ANTHROPIC_AWS",
	"CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_FOUNDRY",
}

// cloudVars are what auth cloud passes through from the human's shell: the
// provider switches, each provider's documented credential, region and
// endpoint variables, and a gateway's bearer and base URL
// (https://code.claude.com/docs/en/{amazon-bedrock, claude-platform-on-aws,
// google-vertex-ai, microsoft-foundry, gateways}). The directories a
// provider's SDK finds under $HOME are shared stores (providerStores); a
// file a variable names is declared by credentialFileVars.
var cloudVars = append(slices.Clone(providerSwitches),
	// Amazon Bedrock.
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK",
	"AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE",
	"ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_BEDROCK_MANTLE_BASE_URL", "CLAUDE_CODE_SKIP_MANTLE_AUTH",
	// Claude Platform on AWS.
	"ANTHROPIC_AWS_API_KEY", "ANTHROPIC_AWS_BASE_URL", "ANTHROPIC_AWS_WORKSPACE_ID", "CLAUDE_CODE_SKIP_ANTHROPIC_AWS_AUTH",
	// Google Vertex.
	"ANTHROPIC_VERTEX_PROJECT_ID", "ANTHROPIC_VERTEX_BASE_URL", "CLOUD_ML_REGION",
	"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT",
	// Microsoft Foundry.
	"ANTHROPIC_FOUNDRY_API_KEY", "ANTHROPIC_FOUNDRY_AUTH_TOKEN", "ANTHROPIC_FOUNDRY_BASE_URL", "ANTHROPIC_FOUNDRY_RESOURCE",
	// A gateway.
	AuthTokenEnv, "ANTHROPIC_BASE_URL",
)

// credentialFileVars are the cloudVars whose value is a path to a credential
// file, not a credential: the AWS shared config and credentials files
// (https://docs.aws.amazon.com/sdkref/latest/guide/file-location.html) and a
// Google service-account or ADC key
// (https://cloud.google.com/docs/authentication/application-default-credentials).
var credentialFileVars = []string{"AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE", "GOOGLE_APPLICATION_CREDENTIALS"}

// providerStores are the cloud providers' own credential directories under
// $HOME, which the SDKs claude embeds read when no variable carries the
// credential: the AWS shared config and credentials files and SSO cache
// (https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-files.html,
// which claude's Bedrock page defers to) and gcloud's application-default
// credentials (https://cloud.google.com/docs/authentication/application-default-credentials,
// what claude's Vertex page has you create with `gcloud auth
// application-default login`). Shared read-only: a run uses the human's
// provider login, never changes it — except the SSO token cache, which the
// AWS SDK rewrites when it refreshes an SSO login
// (https://docs.aws.amazon.com/cli/latest/userguide/sso-using-profile.html),
// so it is shared read-write, nested in the read-only ~/.aws and listed after
// it: a store mounted before its parent would be shadowed by it.
var providerStores = []engine.SharedStore{
	{HomeRel: ".aws", ReadOnly: true},
	{HomeRel: ".aws/sso/cache"},
	{HomeRel: ".config/gcloud", ReadOnly: true},
}

// credentialVars are claude's own credential vars across the env modes and
// the gateway; the container passthrough declares them too.
var credentialVars = []string{OAuthTokenEnv, APIKeyEnv, AuthTokenEnv}

// modeVar is the var each env mode's credential is read from in the
// launching env and handed to claude in.
var modeVar = map[engine.AuthMode]string{
	engine.AuthToken:  OAuthTokenEnv,
	engine.AuthAPIKey: APIKeyEnv,
}

// claudeAuth is claude's auth capability: its modes and the precedence
// between the vars that carry them.
type claudeAuth struct {
	// engine is the registered name refusals name.
	engine string
}

func (claudeAuth) Modes() []engine.AuthMode {
	return []engine.AuthMode{engine.AuthLogin, engine.AuthToken, engine.AuthAPIKey, engine.AuthCloud}
}

// Credentials: the declared mode decides. Only that mode's credential
// reaches claude, read from the launching env, and everything that would
// outrank or replace it is unset.
//
// login shares the human's own credential storage (loginStore). Every other
// mode unsets SecureStorageEnv: "" is not "unset" for it but HOME/.claude,
// the human's real credential.
func (c claudeAuth) Credentials(mode engine.AuthMode, shell func(string) (string, bool)) (engine.Credentials, error) {
	switch mode {
	case engine.AuthLogin:
		return engine.Credentials{
			Unset:  append(append(slices.Clone(credentialVars), providerSwitches...), ProfileEnv),
			Stores: []engine.SharedStore{loginStore(shell)},
		}, nil
	case engine.AuthCloud:
		return c.cloudCredentials(shell)
	}
	v, ok := modeVar[mode]
	if !ok {
		return engine.Credentials{}, fmt.Errorf("claude: %w: %q", engine.ErrAuthModeUnsupported, mode)
	}
	secret, ok := shell(v)
	if !ok || secret == "" {
		return engine.Credentials{}, report.Errorf(modeRemedy[mode], "%s %s: %s is not exported: %w", c.engine, mode, v, engine.ErrNoCredential)
	}
	unset := append(slices.DeleteFunc(slices.Clone(credentialVars), func(s string) bool { return s == v }), providerSwitches...)
	return engine.Credentials{
		Env:   map[string]string{v: secret},
		Unset: append(unset, SecureStorageEnv),
	}, nil
}

// loginStore is the human's claude credential storage: SecureStorageEnv set
// to the exact string the launching env's own claude resolves its storage
// from — its own SecureStorageEnv when set (a launch from inside a sharing
// run), else ConfigDirEnv, else "" (HOME/.claude) — never cleaned, since
// claude names its macOS keychain item from it. The credential and both
// refresh locks are then the human's own, shared. Where the OS keeps it
// under $HOME is loginStoreHomeRel's per-OS answer.
func loginStore(shell func(string) (string, bool)) engine.SharedStore {
	return engine.SharedStore{Var: SecureStorageEnv, Value: sharedStorage(shell), HomeRel: loginStoreHomeRel}
}

// modeRemedy is how the human supplies each env mode's credential. The
// token is minted by the human with claude's own flow and kept in their
// environment or secret manager: Anthropic's terms forbid a third party to
// "collect, store, or intermediate Claude.ai credentials or session tokens"
// (https://code.claude.com/docs/en/legal-and-compliance), so no ctxloom
// command takes one.
var modeRemedy = map[engine.AuthMode]string{
	engine.AuthToken:  "run `claude setup-token` and export " + OAuthTokenEnv + " (or store it in your secret manager)",
	engine.AuthAPIKey: "export " + APIKeyEnv,
}

// cloudCredentials passes the human's cloud or gateway configuration
// through from the shell, refusing when nothing selects one, and shares each
// provider credential directory the human has (providerStores) read-only.
// The env modes' vars and the login's storage are unset: a provider
// switch outranks them anyway, but a gateway bearer does not outrank
// nothing.
func (c claudeAuth) cloudCredentials(shell func(string) (string, bool)) (engine.Credentials, error) {
	set := map[string]string{}
	for _, k := range cloudVars {
		if v, ok := shell(k); ok && v != "" {
			set[k] = v
		}
	}
	selected := set[AuthTokenEnv] != ""
	for _, k := range providerSwitches {
		selected = selected || set[k] != ""
	}
	if !selected {
		var others []string
		for _, m := range c.Modes() {
			if m != engine.AuthCloud {
				others = append(others, string(m))
			}
		}
		return engine.Credentials{}, report.Errorf(
			fmt.Sprintf("export one of %s with that provider's own variables (https://code.claude.com/docs/en/third-party-integrations), or %s with %s for a gateway; or declare another of the modes %s supports: %s",
				strings.Join(providerSwitches, ", "), AuthTokenEnv, "ANTHROPIC_BASE_URL", c.engine, strings.Join(others, ", ")),
			"%s cloud: none of %s is set: %w", c.engine, strings.Join(append(slices.Clone(providerSwitches), AuthTokenEnv), ", "), engine.ErrNoCredential)
	}
	return engine.Credentials{Env: set, Unset: []string{OAuthTokenEnv, APIKeyEnv, SecureStorageEnv, ProfileEnv}, Stores: existingProviderStores(shell), FileVars: fileVarsIn(set)}, nil
}

// fileVarsIn are the credentialFileVars env carries.
func fileVarsIn(env map[string]string) []string {
	var out []string
	for _, k := range credentialFileVars {
		if env[k] != "" {
			out = append(out, k)
		}
	}
	return out
}

// existingProviderStores are the providerStores present in the launching
// env's home: only a directory that exists is declared, since a declared
// store that is missing refuses the run and a provider login the human never
// made is not a missing one. The home is the launching env's HOME, where the
// provider SDKs look; none is declared without one.
func existingProviderStores(shell func(string) (string, bool)) []engine.SharedStore {
	home, _ := shell("HOME")
	if home == "" {
		return nil
	}
	var out []engine.SharedStore
	for _, st := range providerStores {
		if fi, err := os.Stat(st.HostDir(home)); err == nil && fi.IsDir() {
			out = append(out, st)
		}
	}
	return out
}

// sharedStorage is what SecureStorageEnv must carry to share the login the
// launching env resolves.
func sharedStorage(shell func(string) (string, bool)) string {
	if v, ok := shell(SecureStorageEnv); ok {
		return v
	}
	v, _ := shell(ConfigDirEnv)
	return v
}
