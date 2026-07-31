package agentkey

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// TestDiscover_FailureTaxonomyIsClosed pins the contract a caller can actually
// key policy off: Discover fails in exactly two shapes.
//
//   - AMBIGUOUS (*AmbiguousKeyError, *AmbiguousKeyNameError) — candidates
//     exist and the user must choose. Discover never guesses.
//   - EVERYTHING ELSE (*NoKeyError) — nothing usable resolved. NoKeyError's
//     own doc already says a caller "MUST treat it as a hard failure to sign,
//     never degrade to an unsigned publish", which is only actionable if the
//     type actually covers every such failure.
//
// The defect this pins (U135-F08): it did not. Which arm produced a
// *NoKeyError and which produced a bare fmt.Errorf depended on WHICH STEP OF
// THE CHAIN was running, not on what had gone wrong — see the twin below.
func TestDiscover_FailureTaxonomyIsClosed(t *testing.T) {
	present, presentLine := newTestIdentity(t, "loaded@example.com")
	absent, absentLine := newTestIdentity(t, "not-loaded@example.com")

	writePub := func(t *testing.T, line string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "id.pub")
		require.NoError(t, os.WriteFile(path, []byte(line), 0o600))
		return path
	}

	agentWith := func(signers ...ssh.Signer) func() (agent.Agent, error) {
		comments := make([]string, len(signers))
		for i := range signers {
			comments[i] = "loaded@example.com"
		}
		ag := &fakeAgent{signers: signers, comments: comments}
		return func() (agent.Agent, error) { return ag, nil }
	}
	noGitKey := func(context.Context, string, string) (string, bool, error) { return "", false, nil }

	cases := []struct {
		name          string
		d             *Discoverer
		key           string
		wantAmbiguous bool
	}{
		{
			name: "agent unreachable, nothing else named",
			d:    &Discoverer{GitConfig: noGitKey, DialAgent: func() (agent.Agent, error) { return nil, errors.New("no agent") }},
		},
		{
			name: "agent holds nothing",
			d:    &Discoverer{GitConfig: noGitKey, DialAgent: agentWith()},
		},
		{
			name:          "agent holds several, nothing narrows",
			d:             &Discoverer{GitConfig: noGitKey, DialAgent: agentWith(present, absent)},
			wantAmbiguous: true,
		},
		{
			name: "--key names a fingerprint the agent does not hold",
			d:    &Discoverer{GitConfig: noGitKey, DialAgent: agentWith(present)},
			key:  ssh.FingerprintSHA256(absent.PublicKey()),
		},
		{
			name: "--key names a public key file the agent does not hold",
			d:    &Discoverer{GitConfig: noGitKey, DialAgent: agentWith(present)},
			key:  writePub(t, absentLine),
		},
		{
			name: "--key names nothing at all",
			d:    &Discoverer{GitConfig: noGitKey, DialAgent: agentWith(present)},
			key:  "nothing-by-this-name",
		},
		{
			name: "--key is a name matching several agent comments",
			d: &Discoverer{
				GitConfig: noGitKey,
				DialAgent: func() (agent.Agent, error) {
					return &fakeAgent{
						signers:  []ssh.Signer{present, absent},
						comments: []string{"shared@example.com", "shared@example.org"},
					}, nil
				},
			},
			key:           "shared@example.",
			wantAmbiguous: true,
		},
		{
			name: "git config names a key the agent does not hold",
			d: &Discoverer{
				GitConfig: func(context.Context, string, string) (string, bool, error) { return absentLine, true, nil },
				DialAgent: agentWith(present),
			},
		},
		{
			name: "git config names an unreadable path",
			d: &Discoverer{
				GitConfig: func(context.Context, string, string) (string, bool, error) {
					return "/nonexistent/definitely/not/here", true, nil
				},
				DialAgent: agentWith(present),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.d.Discover(context.Background(), tc.key)
			require.Error(t, err)

			var ambig *AmbiguousKeyError
			var ambigName *AmbiguousKeyNameError
			isAmbiguous := errors.As(err, &ambig) || errors.As(err, &ambigName)

			if tc.wantAmbiguous {
				assert.True(t, isAmbiguous, "expected an ambiguous shape, got %T: %v", err, err)
				return
			}

			var noKey *NoKeyError
			assert.False(t, isAmbiguous, "this is not an ambiguity: %v", err)
			assert.True(t, errors.As(err, &noKey),
				"every non-ambiguous failure must be a *NoKeyError; got %T: %v", err, err)
		})
	}

	// The twin. These two are the SAME situation — a key that was named,
	// exists, and simply is not loaded in the agent — reached by two different
	// steps of the chain. They disagreed: the git-config step returned a
	// *NoKeyError while the --key step returned a bare error, so a caller
	// switching on the type got different answers for the same fact, and
	// `ctxloom doctor` rendered them with different remedies.
	t.Run("the same fact reached two ways has the same type", func(t *testing.T) {
		viaKey := &Discoverer{GitConfig: noGitKey, DialAgent: agentWith(present)}
		_, errViaKey := viaKey.Discover(context.Background(), writePub(t, absentLine))

		viaGit := &Discoverer{
			GitConfig: func(context.Context, string, string) (string, bool, error) { return absentLine, true, nil },
			DialAgent: agentWith(present),
		}
		_, errViaGit := viaGit.Discover(context.Background(), "")

		require.Error(t, errViaKey)
		require.Error(t, errViaGit)

		var a, b *NoKeyError
		assert.True(t, errors.As(errViaGit, &a), "git-config route: got %T", errViaGit)
		assert.True(t, errors.As(errViaKey, &b),
			"--key route names the same not-loaded key and must classify identically; got %T: %v", errViaKey, errViaKey)
		_ = presentLine
	})
}
