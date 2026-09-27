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
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
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

func (fakeMintAuth) LaunchEnv(mode engine.AuthMode, _ func(string) (string, bool), stored engine.CredentialReader) (map[string]string, error) {
	v, err := stored.Read(mode)
	if err != nil {
		return nil, fmt.Errorf("fake: %w", err)
	}
	return map[string]string{fakeTokenVar: string(v)}, nil
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

	env, err := resolveRunAuth(context.Background(), reg, runAuth{Backend: "fake-auth", Mode: engine.AuthToken, OnHost: true})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{fakeTokenVar: "minted-secret"}, env)
	require.Len(t, *seen, 1, "minted once, on the terminal it was handed")
	assert.Same(t, &errOut, (*seen)[0].Err.(*bytes.Buffer))
	assert.Contains(t, errOut.String(), "owner-only")
	assert.NotContains(t, errOut.String(), "minted-secret", "the credential is never echoed")

	got, err := isolation.StoredCredentials("fake-auth").Read(engine.AuthToken)
	require.NoError(t, err)
	assert.Equal(t, "minted-secret", string(got), "stored for every later run")

	_, err = resolveRunAuth(context.Background(), reg, runAuth{Backend: "fake-auth", Mode: engine.AuthToken, OnHost: false})
	require.NoError(t, err)
	assert.Len(t, *seen, 1, "a stored credential is read, never minted again")
}

// UNATTENDED: no terminal, so nothing prompts. The run is refused with the
// typed absence and the commands that would provide the credential, and the
// engine's Mint is never called.
func TestResolveRunAuth_UnattendedRefusesNamingTheRemedy(t *testing.T) {
	reg, seen := installFakeMint(t)
	withTerminal(t, engine.Terminal{}, false)

	_, err := resolveRunAuth(context.Background(), reg, runAuth{Backend: "fake-auth", Mode: engine.AuthToken, OnHost: true})
	require.ErrorIs(t, err, engine.ErrNoCredential)
	assert.Contains(t, err.Error(), "ctxloom auth mint --engine fake-auth --mode token")
	assert.Contains(t, err.Error(), "no terminal")
	assert.Empty(t, *seen, "an unattended run never starts the mint flow")
}

// A mode the engine cannot mint is refused with the store command, even at
// a terminal.
func TestResolveRunAuth_UnmintableModeNamesTheStoreCommand(t *testing.T) {
	reg, _ := installFakeMint(t)
	withTerminal(t, engine.Terminal{Err: &bytes.Buffer{}}, true)
	_, err := resolveRunAuth(context.Background(), reg, runAuth{Backend: "fake-auth", Mode: engine.AuthAPIKey, OnHost: true})
	require.ErrorIs(t, err, engine.ErrMintUnsupported)
	assert.Contains(t, err.Error(), "ctxloom auth set --engine fake-auth --mode api-key")
}

func TestResolveRunAuth_RefusesAModeTheEngineLacks(t *testing.T) {
	reg, _ := installFakeMint(t)
	_, err := resolveRunAuth(context.Background(), reg, runAuth{Backend: "fake-auth", Mode: engine.AuthLogin, OnHost: true})
	require.ErrorIs(t, err, engine.ErrAuthModeUnsupported)
}

// Nothing to resolve: an engine that declares no auth, and a host run on the
// human's real home, which authenticates as their own engine does, in place.
func TestResolveRunAuth_NothingToResolve(t *testing.T) {
	reg := enginefixture.Install(t, enginefixture.Kind("no-auth"))
	env, err := resolveRunAuth(context.Background(), reg, runAuth{Backend: "no-auth", Mode: engine.AuthToken, OnHost: true})
	require.NoError(t, err)
	assert.Nil(t, env)

	fakeHostHome(t, "")
	env, err = resolveRunAuth(context.Background(), engines.Registry(), runAuth{Backend: claude.EngineName, Mode: engine.AuthToken, OnHost: true})
	require.NoError(t, err)
	assert.Nil(t, env, "the real home in place is the human's own login; nothing is resolved or minted")
}

// A host claude agent declaring login shares the human's own credential
// storage, as the launching env resolves it, verbatim, with every other
// credential blanked — including a token the human exported.
func TestResolveRunAuth_HostLoginSharesTheHumansStorage(t *testing.T) {
	fakeHostHome(t, tokenFixture)
	t.Setenv(claude.ConfigDirEnv, "/home/me/./.claude-work/")
	env, err := resolveRunAuth(context.Background(), engines.Registry(), runAuth{Backend: claude.EngineName, Mode: engine.AuthLogin, OnHost: true})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		claude.SecureStorageEnv: "/home/me/./.claude-work/",
		claude.OAuthTokenEnv:    "",
		claude.APIKeyEnv:        "",
		claude.AuthTokenEnv:     "",
	}, env)
}

// The human's login cannot reach a container yet: declaring it there is
// refused, never launched logged out.
func TestResolveRunAuth_ContainerLoginIsRefused(t *testing.T) {
	fakeHostHome(t, "")
	_, err := resolveRunAuth(context.Background(), engines.Registry(), runAuth{Backend: claude.EngineName, Mode: engine.AuthLogin, OnHost: false})
	require.ErrorIs(t, err, errLoginNotInContainer)
}

// A claude token agent gets the stored token, with an exported API key
// blanked: the declared mode decides.
func TestResolveRunAuth_ClaudeTokenFromTheStore(t *testing.T) {
	fakeHostHome(t, "")
	t.Setenv(claude.APIKeyEnv, "sk-ant-api-shell")
	_, err := isolation.StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(tokenFixture))
	require.NoError(t, err)
	env, err := resolveRunAuth(context.Background(), engines.Registry(), runAuth{Backend: claude.EngineName, Mode: engine.AuthToken, OnHost: false})
	require.NoError(t, err)
	assert.Equal(t, tokenFixture, env[claude.OAuthTokenEnv])
	assert.Equal(t, "", env[claude.APIKeyEnv])
	_, set := os.LookupEnv(claude.OAuthTokenEnv)
	assert.True(t, set, "fixture: fakeHostHome sets the var empty")
	assert.Empty(t, os.Getenv(claude.OAuthTokenEnv), "the stored token never enters this process's env")
}
