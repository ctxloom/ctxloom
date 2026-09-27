package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// APIKeyEnv and AuthTokenEnv are claude's other credential vars: a
// pay-per-use key, and a gateway's bearer (ANTHROPIC_AUTH_TOKEN with
// ANTHROPIC_BASE_URL). claude reads either ahead of a stored login.
const (
	APIKeyEnv    = "ANTHROPIC_API_KEY"
	AuthTokenEnv = "ANTHROPIC_AUTH_TOKEN"
)

// credentialVars are every var claude authenticates from, so a run in one
// mode blanks the others: claude would otherwise read an inherited one
// ahead of the credential the mode chose.
var credentialVars = []string{OAuthTokenEnv, APIKeyEnv, AuthTokenEnv}

// modeVar is the var each stored mode's credential is handed to claude in.
var modeVar = map[engine.AuthMode]string{
	engine.AuthToken:  OAuthTokenEnv,
	engine.AuthAPIKey: APIKeyEnv,
}

// setupTokenPattern is the token `claude setup-token` prints.
var setupTokenPattern = regexp.MustCompile(`sk-ant-oat[0-9]*-[A-Za-z0-9_-]+`)

// claudeAuth is claude's auth capability: its three modes, the precedence
// between the vars that carry them, and minting through `claude
// setup-token`.
type claudeAuth struct {
	// binary is the claude executable Mint runs.
	binary string
}

func (claudeAuth) Modes() []engine.AuthMode {
	return []engine.AuthMode{engine.AuthLogin, engine.AuthToken, engine.AuthAPIKey}
}

// LaunchEnv: the declared mode decides. Only that mode's credential reaches
// claude; a value the launching env exports for THAT mode wins over the
// stored one; every other credential var is blanked.
//
// login points SecureStorageEnv at the exact string the launching env's own
// claude resolves its storage from — its own SecureStorageEnv when set (a
// launch from inside a sharing run), else ConfigDirEnv, else "" (HOME/.claude)
// — never cleaned, since claude names its macOS keychain item from it. The
// credential and both refresh locks are then the human's own, shared.
//
// The storage var is deliberately NOT blanked for token and api-key: "" is
// not "unset" for it but HOME/.claude, the human's real credential, and an
// env map cannot unset a var. The mode's credential outranks any stored
// login anyway.
func (claudeAuth) LaunchEnv(mode engine.AuthMode, shell func(string) (string, bool), stored engine.CredentialReader) (map[string]string, error) {
	env := make(map[string]string, len(credentialVars)+1)
	for _, v := range credentialVars {
		env[v] = ""
	}
	if mode == engine.AuthLogin {
		env[SecureStorageEnv] = sharedStorage(shell)
		return env, nil
	}
	v, ok := modeVar[mode]
	if !ok {
		return nil, fmt.Errorf("claude: %w: %q", engine.ErrAuthModeUnsupported, mode)
	}
	if s, ok := shell(v); ok && s != "" {
		env[v] = s
		return env, nil
	}
	secret, err := stored.Read(mode)
	if err != nil {
		return nil, fmt.Errorf("claude %s (neither %s exported nor one stored): %w", mode, v, err)
	}
	env[v] = string(bytes.TrimSpace(secret))
	return env, nil
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
// token it prints. Its output still reaches the terminal as it is produced;
// the token is picked out of it and never stored here.
func (a claudeAuth) Mint(ctx context.Context, mode engine.AuthMode, term engine.Terminal) ([]byte, error) {
	if mode != engine.AuthToken {
		return nil, fmt.Errorf("claude %s: %w", mode, engine.ErrMintUnsupported)
	}
	var captured bytes.Buffer
	cmd := exec.CommandContext(ctx, a.binary, "setup-token")
	cmd.Stdin = term.In
	cmd.Stdout = io.MultiWriter(orDiscard(term.Out), &captured)
	cmd.Stderr = orDiscard(term.Err)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("claude setup-token: %w", err)
	}
	tok := setupTokenPattern.Find(captured.Bytes())
	if tok == nil {
		return nil, errNoTokenPrinted
	}
	return tok, nil
}

var errNoTokenPrinted = errors.New("claude setup-token finished without printing a token")

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
