package operations

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// fakeMintAuth is an engine auth whose token lands in FAKE_TOKEN and whose
// mint returns minted, recording each terminal it was handed.
type fakeMintAuth struct {
	minted string
	mu     *sync.Mutex
	seen   *[]engine.Terminal
}

const fakeTokenVar = "FAKE_TOKEN"

func (fakeMintAuth) Modes() []engine.AuthMode {
	return []engine.AuthMode{engine.AuthToken, engine.AuthAPIKey}
}

func (fakeMintAuth) LaunchEnv(mode engine.AuthMode, _ func(string) (string, bool), stored engine.CredentialReader) (engine.LaunchEnv, error) {
	v, err := stored.Read(mode)
	if err != nil {
		return engine.LaunchEnv{}, fmt.Errorf("fake: %w", err)
	}
	return engine.LaunchEnv{Set: map[string]string{fakeTokenVar: string(v)}}, nil
}

func (f fakeMintAuth) Mint(_ context.Context, mode engine.AuthMode, term engine.Terminal) ([]byte, error) {
	if mode != engine.AuthToken {
		return nil, engine.ErrMintUnsupported
	}
	f.mu.Lock()
	*f.seen = append(*f.seen, term)
	f.mu.Unlock()
	return []byte(f.minted), nil
}

// installFakeMint stands a fake-auth engine in front of the registry and the
// store, under a scratch HOME, and returns the registry and the recorded
// terminals.
func installFakeMint(t *testing.T) (engine.Registry, *[]engine.Terminal) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	seen := &[]engine.Terminal{}
	auth := fakeMintAuth{minted: "minted-secret", mu: &sync.Mutex{}, seen: seen}
	reg := enginefixture.Install(t, enginefixture.Kind("fake-auth", mock.WithHome(engine.HomeSpec{
		Vars: []engine.HomeVar{{Name: "FAKE_HOME", Subdir: ".fake"}},
		Auth: engine.Provide[engine.Auth](auth),
	})))
	return reg, seen
}

// withTerminal sets what attendedTerminal answers for the test.
func withTerminal(t *testing.T, term engine.Terminal, ok bool) {
	t.Helper()
	prev := attendedTerminal
	attendedTerminal = func() (engine.Terminal, bool) { return term, ok }
	t.Cleanup(func() { attendedTerminal = prev })
}

// A run whose agent needs a credential that is not stored mints it at the
// human's terminal through the ENGINE's own Mint, stores it owner-only, and
// launches with it; the next run reads the stored one and mints nothing.
func TestResolveRunAuth_MintsWhenNeededAtATerminalAndStores(t *testing.T) {
	reg, seen := installFakeMint(t)
	var errOut bytes.Buffer
	term := engine.Terminal{In: bytes.NewBufferString(""), Out: &errOut, Err: &errOut}
	withTerminal(t, term, true)

	env, err := resolveRunAuth(context.Background(), reg, runAuth{Backend: "fake-auth", Declared: string(engine.AuthToken), OnHost: true})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{fakeTokenVar: "minted-secret"}, env.Set)
	require.Len(t, *seen, 1, "minted once, on the terminal it was handed")
	assert.Same(t, &errOut, (*seen)[0].Err.(*bytes.Buffer))
	assert.Contains(t, errOut.String(), "owner-only")
	assert.NotContains(t, errOut.String(), "minted-secret", "the credential is never echoed")

	got, err := isolation.StoredCredentials("fake-auth").Read(engine.AuthToken)
	require.NoError(t, err)
	assert.Equal(t, "minted-secret", string(got), "stored for every later run")

	_, err = resolveRunAuth(context.Background(), reg, runAuth{Backend: "fake-auth", Declared: string(engine.AuthToken), OnHost: false})
	require.NoError(t, err)
	assert.Len(t, *seen, 1, "a stored credential is read, never minted again")
}

// UNATTENDED: no terminal, so nothing prompts. The run is refused with the
// typed absence and the commands that would provide the credential, and the
// engine's Mint is never called.
func TestResolveRunAuth_UnattendedRefusesNamingTheRemedy(t *testing.T) {
	reg, seen := installFakeMint(t)
	withTerminal(t, engine.Terminal{}, false)

	_, err := resolveRunAuth(context.Background(), reg, runAuth{Backend: "fake-auth", Declared: string(engine.AuthToken), OnHost: true})
	require.ErrorIs(t, err, engine.ErrNoCredential)
	assert.Contains(t, remedyOf(t, err), "ctxloom auth mint --engine fake-auth --mode token")
	assert.Contains(t, err.Error(), "no terminal")
	assert.Empty(t, *seen, "an unattended run never starts the mint flow")
}

// A mode the engine does not mint is never minted, even at a terminal: its
// missing credential is the engine's own refusal.
func TestResolveRunAuth_AnUnmintedModeIsNeverMinted(t *testing.T) {
	reg, seen := installFakeMint(t)
	withTerminal(t, engine.Terminal{Err: &bytes.Buffer{}}, true)
	_, err := resolveRunAuth(context.Background(), reg, runAuth{Backend: "fake-auth", Declared: string(engine.AuthAPIKey), OnHost: true})
	require.ErrorIs(t, err, engine.ErrNoCredential)
	assert.Empty(t, *seen, "Mint is never called for an api-key")
}

func TestResolveRunAuth_RefusesAModeTheEngineLacks(t *testing.T) {
	reg, _ := installFakeMint(t)
	_, err := resolveRunAuth(context.Background(), reg, runAuth{Backend: "fake-auth", Declared: string(engine.AuthLogin), OnHost: true})
	require.ErrorIs(t, err, engine.ErrAuthModeUnsupported)
}

// Nothing to resolve: an engine that declares no auth.
func TestResolveRunAuth_NothingToResolve(t *testing.T) {
	reg := enginefixture.Install(t, enginefixture.Kind("no-auth"))
	env, err := resolveRunAuth(context.Background(), reg, runAuth{Backend: "no-auth", OnHost: true})
	require.NoError(t, err)
	assert.Zero(t, env)
}

// A host claude agent declaring login shares the human's own credential
// storage, as the launching env resolves it, verbatim, and every other
// credential — including a token the human exported — is unset.
func TestResolveRunAuth_HostLoginSharesTheHumansStorage(t *testing.T) {
	fakeHostHome(t, tokenFixture)
	t.Setenv(claude.ConfigDirEnv, "/home/me/./.claude-work/")
	env, err := resolveRunAuth(context.Background(), engines.Registry(), runAuth{Backend: claude.EngineName, Declared: string(engine.AuthLogin), OnHost: true})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{claude.SecureStorageEnv: "/home/me/./.claude-work/"}, env.Set)
	assert.Subset(t, env.Unset, []string{claude.OAuthTokenEnv, claude.APIKeyEnv, claude.AuthTokenEnv})
}

// The human's login cannot reach a container: declaring it there is
// refused, typed, with a remedy naming the modes the engine supports there.
func TestResolveRunAuth_ContainerLoginIsRefused(t *testing.T) {
	fakeHostHome(t, "")
	_, err := resolveRunAuth(context.Background(), engines.Registry(), runAuth{Backend: claude.EngineName, Declared: string(engine.AuthLogin), OnHost: false})
	require.ErrorIs(t, err, errLoginNotInContainer)
	requireRemedyNamingModes(t, err, engine.AuthLogin)
}

// A claude token agent gets the stored token, with an exported API key
// unset: the declared mode decides.
func TestResolveRunAuth_ClaudeTokenFromTheStore(t *testing.T) {
	fakeHostHome(t, "")
	t.Setenv(claude.APIKeyEnv, "sk-ant-api-shell")
	_, err := isolation.StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(tokenFixture))
	require.NoError(t, err)
	env, err := resolveRunAuth(context.Background(), engines.Registry(), runAuth{Backend: claude.EngineName, Declared: string(engine.AuthToken), OnHost: false})
	require.NoError(t, err)
	assert.Equal(t, tokenFixture, env.Set[claude.OAuthTokenEnv])
	assert.Contains(t, env.Unset, claude.APIKeyEnv)
	assert.Contains(t, env.Unset, claude.SecureStorageEnv)
	assert.Empty(t, os.Getenv(claude.OAuthTokenEnv), "the stored token never enters this process's env")
}

// A mode ctxloom stores nothing for is never minted: cloud with nothing
// selected is the engine's own refusal, at a terminal or not.
func TestResolveRunAuth_CloudIsNeverMinted(t *testing.T) {
	fakeHostHome(t, "")
	withTerminal(t, engine.Terminal{Err: &bytes.Buffer{}}, true)
	t.Setenv("PATH", t.TempDir())
	_, err := resolveRunAuth(context.Background(), engines.Registry(), runAuth{Backend: claude.EngineName, Declared: string(engine.AuthCloud), OnHost: true})
	require.ErrorIs(t, err, engine.ErrNoCredential)
	requireRemedyNamingModes(t, err, engine.AuthCloud)
}

// requireRemedyNamingModes asserts err carries a remedy that names every
// mode claude supports except the one refused, read from its Modes().
func requireRemedyNamingModes(t *testing.T, err error, refused engine.AuthMode) {
	t.Helper()
	var r report.Remediable
	require.ErrorAs(t, err, &r)
	a, ok := isolation.AuthFor(claude.EngineName)
	require.True(t, ok)
	for _, m := range a.Modes() {
		if m != refused {
			assert.Contains(t, r.Remedy(), string(m))
		}
	}
}

// ONE check, two doors: every invalid selection is refused by the same
// typed error at write time (agent create/edit) and at run time (a launch).
func TestAuthSelection_WriteAndRunRefuseAlike(t *testing.T) {
	fakeHostHome(t, "")
	withTerminal(t, engine.Terminal{}, false)
	for _, tc := range []struct {
		name     string
		llm      string
		mode     string
		sentinel error
	}{
		{"an unknown mode", "claude-code", "apikey", engine.ErrUnknownAuthMode},
		{"any mode on an engine with no auth", "mock", "token", engine.ErrEngineHasNoAuth},
		{"cloud with nothing selected", "claude-code", "cloud", engine.ErrNoCredential},
		{"api-key with no key", "claude-code", "api-key", engine.ErrNoCredential},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, appDir := loadConfigDir(t, fmt.Sprintf("version: %d\n", config.CurrentConfigVersion))
			_, werr := SetAgent(context.Background(), managerFor(t, appDir), cfg, SetAgentRequest{
				Name: "a", LLM: ptr(tc.llm), Profiles: ptr([]string{"x"}), Auth: ptr(tc.mode),
			})
			require.ErrorIs(t, werr, tc.sentinel, "write")
			_, rerr := resolveRunAuth(context.Background(), engines.Registry(), runAuth{Backend: tc.llm, Declared: tc.mode, OnHost: true})
			require.ErrorIs(t, rerr, tc.sentinel, "run")
			for _, err := range []error{werr, rerr} {
				var r report.Remediable
				require.ErrorAs(t, err, &r)
				assert.NotEmpty(t, r.Remedy())
			}
		})
	}
}

// remedyOf is the remedy err carries, failing when it carries none.
func remedyOf(t *testing.T, err error) string {
	t.Helper()
	var r report.Remediable
	require.ErrorAs(t, err, &r)
	return r.Remedy()
}
