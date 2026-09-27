package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
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
// google-vertex-ai, microsoft-foundry, gateways}). A provider's credential
// FILES (~/.aws, gcloud's ADC) are not variables and are not carried.
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

// credentialVars are claude's own credential vars across the stored modes
// and the gateway; the container passthrough declares them too.
var credentialVars = []string{OAuthTokenEnv, APIKeyEnv, AuthTokenEnv}

// modeVar is the var each stored mode's credential is handed to claude in.
var modeVar = map[engine.AuthMode]string{
	engine.AuthToken:  OAuthTokenEnv,
	engine.AuthAPIKey: APIKeyEnv,
}

// setupTokenPattern is the token `claude setup-token` prints.
var setupTokenPattern = regexp.MustCompile(`sk-ant-oat[0-9]*-[A-Za-z0-9_-]+`)

// claudeAuth is claude's auth capability: its modes, the precedence between
// the vars that carry them, and minting through `claude setup-token`.
type claudeAuth struct {
	// binary is the claude executable Mint runs.
	binary string
	// engine is the registered name the remedies' ctxloom commands name.
	engine string
}

func (claudeAuth) Modes() []engine.AuthMode {
	return []engine.AuthMode{engine.AuthLogin, engine.AuthToken, engine.AuthAPIKey, engine.AuthCloud}
}

// LaunchEnv: the declared mode decides. Only that mode's credential reaches
// claude, a value the launching env exports for THAT mode wins over the
// stored one, and everything that would outrank or replace it is unset.
//
// login points SecureStorageEnv at the exact string the launching env's own
// claude resolves its storage from — its own SecureStorageEnv when set (a
// launch from inside a sharing run), else ConfigDirEnv, else "" (HOME/.claude)
// — never cleaned, since claude names its macOS keychain item from it. The
// credential and both refresh locks are then the human's own, shared. Every
// other mode unsets SecureStorageEnv: "" is not "unset" for it but
// HOME/.claude, the human's real credential.
func (c claudeAuth) LaunchEnv(mode engine.AuthMode, shell func(string) (string, bool), stored engine.CredentialReader) (engine.LaunchEnv, error) {
	switch mode {
	case engine.AuthLogin:
		return engine.LaunchEnv{
			Set:   map[string]string{SecureStorageEnv: sharedStorage(shell)},
			Unset: append(append(slices.Clone(credentialVars), providerSwitches...), ProfileEnv),
		}, nil
	case engine.AuthCloud:
		return c.cloudEnv(shell)
	}
	v, ok := modeVar[mode]
	if !ok {
		return engine.LaunchEnv{}, fmt.Errorf("claude: %w: %q", engine.ErrAuthModeUnsupported, mode)
	}
	secret, err := c.modeCredential(mode, v, shell, stored)
	if err != nil {
		return engine.LaunchEnv{}, err
	}
	unset := append(slices.DeleteFunc(slices.Clone(credentialVars), func(s string) bool { return s == v }), providerSwitches...)
	return engine.LaunchEnv{
		Set:   map[string]string{v: secret},
		Unset: append(unset, SecureStorageEnv),
	}, nil
}

// modeCredential is the shell's export of v when non-empty, else the stored
// credential for mode, trimmed.
func (c claudeAuth) modeCredential(mode engine.AuthMode, v string, shell func(string) (string, bool), stored engine.CredentialReader) (string, error) {
	if s, ok := shell(v); ok && s != "" {
		return s, nil
	}
	secret, err := stored.Read(mode)
	if errors.Is(err, engine.ErrNoCredential) {
		fix := fmt.Sprintf("store one with `ctxloom auth set --engine %s --mode %s` (read from stdin), or export %s", c.engine, mode, v)
		if mode.Minted() {
			fix = fmt.Sprintf("run `ctxloom auth mint --engine %s --mode %s` at a terminal, or %s", c.engine, mode, fix)
		}
		return "", report.Errorf(fix, "%s %s (neither %s exported nor one stored): %w", c.engine, mode, v, err)
	}
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", c.engine, mode, err)
	}
	return string(bytes.TrimSpace(secret)), nil
}

// cloudEnv passes the human's cloud or gateway configuration through from
// the shell, refusing when nothing selects one. The stored modes' vars and
// the login's storage are unset: a provider switch outranks them anyway, but
// a gateway bearer does not outrank nothing.
func (c claudeAuth) cloudEnv(shell func(string) (string, bool)) (engine.LaunchEnv, error) {
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
		return engine.LaunchEnv{}, report.Errorf(
			fmt.Sprintf("export one of %s with that provider's own variables (https://code.claude.com/docs/en/third-party-integrations), or %s with %s for a gateway; or declare another of the modes %s supports: %s",
				strings.Join(providerSwitches, ", "), AuthTokenEnv, "ANTHROPIC_BASE_URL", c.engine, strings.Join(others, ", ")),
			"%s cloud: none of %s is set: %w", c.engine, strings.Join(append(slices.Clone(providerSwitches), AuthTokenEnv), ", "), engine.ErrNoCredential)
	}
	return engine.LaunchEnv{Set: set, Unset: []string{OAuthTokenEnv, APIKeyEnv, SecureStorageEnv, ProfileEnv}}, nil
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

// Mint runs `claude setup-token` on the human's terminal and returns the
// token it prints. Its output still reaches the terminal as it is produced,
// with the token itself replaced (tokenRedactor): the credential is stored,
// never echoed.
func (a claudeAuth) Mint(ctx context.Context, mode engine.AuthMode, term engine.Terminal) ([]byte, error) {
	if mode != engine.AuthToken {
		return nil, fmt.Errorf("claude %s: %w", mode, engine.ErrMintUnsupported)
	}
	var captured bytes.Buffer
	shown := &tokenRedactor{w: orDiscard(term.Out)}
	cmd := exec.CommandContext(ctx, a.binary, "setup-token")
	cmd.Stdin = term.In
	cmd.Stdout = io.MultiWriter(shown, &captured)
	cmd.Stderr = orDiscard(term.Err)
	err := cmd.Run()
	shown.flush()
	if err != nil {
		return nil, fmt.Errorf("claude setup-token: %w", err)
	}
	tok := setupTokenPattern.Find(captured.Bytes())
	if tok == nil {
		return nil, errNoTokenPrinted
	}
	return tok, nil
}

var errNoTokenPrinted = errors.New("claude setup-token finished without printing a token")

// redactedToken replaces a minted token in what the human sees.
const redactedToken = "[token stored by ctxloom]"

// tokenRedactor writes through to w immediately, with any setup-token
// replaced, holding back only a trailing fragment that could still grow
// into one (a prefix of it, or a token not yet terminated), so a token split
// across writes is still caught while a prompt with no newline is shown at
// once.
type tokenRedactor struct {
	w   io.Writer
	buf []byte
}

// tokenPrefix is what every setup-token starts with.
const tokenPrefix = "sk-ant-oat"

// openTokenTail matches a token still running at the end of the buffer.
var openTokenTail = regexp.MustCompile(`sk-ant-oat[0-9]*-?[A-Za-z0-9_-]*$`)

func (r *tokenRedactor) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	hold := heldFrom(r.buf)
	if hold > 0 {
		if _, err := r.w.Write(setupTokenPattern.ReplaceAll(r.buf[:hold], []byte(redactedToken))); err != nil {
			return len(p), err
		}
	}
	r.buf = append([]byte(nil), r.buf[hold:]...)
	return len(p), nil
}

// heldFrom is where the fragment that could still become a token begins:
// len(buf) when none does.
func heldFrom(buf []byte) int {
	if loc := openTokenTail.FindIndex(buf); loc != nil {
		return loc[0]
	}
	for k := min(len(tokenPrefix)-1, len(buf)); k > 0; k-- {
		if bytes.HasSuffix(buf, []byte(tokenPrefix[:k])) {
			return len(buf) - k
		}
	}
	return len(buf)
}

func (r *tokenRedactor) flush() {
	if len(r.buf) > 0 {
		_, _ = r.w.Write(setupTokenPattern.ReplaceAll(r.buf, []byte(redactedToken)))
		r.buf = nil
	}
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
