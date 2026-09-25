package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh/agent"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// reviewRun drives reviewPending over res as a terminal user asking for text, with stdin
// scripted and the listing/project flags set, and returns its output.
func reviewRun(t *testing.T, cfg *config.Config, res *operations.PendingReviewResult, interactive, list, project bool, stdin string) (string, error) {
	t.Helper()
	savedTTY, savedIn, savedList, savedProject := isInteractiveTerminal, stdinReader, reviewListFlag, reviewProjectFlag
	t.Cleanup(func() {
		isInteractiveTerminal, stdinReader, reviewListFlag, reviewProjectFlag = savedTTY, savedIn, savedList, savedProject
	})
	isInteractiveTerminal = func() bool { return interactive }
	stdinReader = bufio.NewReader(strings.NewReader(stdin))
	reviewListFlag, reviewProjectFlag = list, project

	resetApp()
	c := &cobra.Command{Use: "review"}
	c.Flags().String("format", "text", "")
	require.NoError(t, c.Flags().Set("format", "text"))
	c.SetContext(context.Background())
	var buf bytes.Buffer
	c.SetOut(&buf)
	err := reviewPending(c, cfg, res)
	return buf.String(), err
}

// reviewFixture isolates HOME, the agent socket and the project root, and
// returns a config plus a pending set holding one fragment.
func reviewFixture(t *testing.T) (*config.Config, *operations.PendingReviewResult) {
	t.Helper()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}})
	return cfg, &operations.PendingReviewResult{Total: 1, Bundles: []operations.ReviewBundle{
		{Ref: "demo", Items: []operations.ReviewItem{{Ref: "demo#fragments/keep", Kind: "fragments", Name: "keep", CurrentContent: "acceptable body"}}},
	}}
}

// serveOneKeyAgent points SSH_AUTH_SOCK at an in-process agent holding a
// single software ed25519 key.
func serveOneKeyAgent(t *testing.T) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	kr := agent.NewKeyring()
	require.NoError(t, kr.Add(agent.AddedKey{PrivateKey: priv, Comment: "one@example.com"}))
	dir, err := os.MkdirTemp("", "ag")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = agent.ServeAgent(kr, conn); _ = conn.Close() }()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
}

// TestReviewPending_BranchesBeforeTheWalk pins every way a review ends before
// the pending set reaches the walk.
func TestReviewPending_BranchesBeforeTheWalk(t *testing.T) {
	t.Run("off a terminal it lists", func(t *testing.T) {
		cfg, res := reviewFixture(t)
		out, err := reviewRun(t, cfg, res, false, false, false, "")
		require.NoError(t, err)
		var want bytes.Buffer
		renderReviewList(&want, res)
		require.Equal(t, want.String(), out)
	})
	t.Run("--list on a terminal lists", func(t *testing.T) {
		cfg, res := reviewFixture(t)
		out, err := reviewRun(t, cfg, res, true, true, false, "")
		require.NoError(t, err)
		var want bytes.Buffer
		renderReviewList(&want, res)
		require.Equal(t, want.String(), out)
	})
	t.Run("nothing pending", func(t *testing.T) {
		cfg, _ := reviewFixture(t)
		out, err := reviewRun(t, cfg, &operations.PendingReviewResult{}, true, false, false, "")
		require.NoError(t, err)
		require.Equal(t, "Nothing is pending review.\n", out)
	})
	t.Run("--project with no key is refused", func(t *testing.T) {
		cfg, res := reviewFixture(t)
		out, err := reviewRun(t, cfg, res, true, false, true, "")
		require.ErrorContains(t, err, "— 'ctxloom review --project' requires one; run 'ssh-add ~/.ssh/id_ed25519' and try again, or review without --project")
		require.Empty(t, out)
	})
	for _, answer := range []string{"n\n", ""} {
		t.Run("unsigned declined "+strings.TrimSpace(answer), func(t *testing.T) {
			cfg, res := reviewFixture(t)
			out, err := reviewRun(t, cfg, res, true, false, false, answer)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(out, "No signing key found"), out)
			require.True(t, strings.HasSuffix(out, "Run 'ctxloom review' again once a signing key is available (see 'ssh-add').\n"), out)
		})
	}
	t.Run("software key, quit at the warning", func(t *testing.T) {
		cfg, res := reviewFixture(t)
		serveOneKeyAgent(t)
		out, err := reviewRun(t, cfg, res, true, false, false, "q\n")
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(out, "Your approval key is a software key held in ssh-agent."), out)
		require.True(t, strings.HasSuffix(out, "Review cancelled.\n"), out)
	})
}

// TestReviewPending_UnsignedWalkEndsWithTheSummary pins the tail: an accepted
// unsigned review hands the set to the walk and closes with its summary.
func TestReviewPending_UnsignedWalkEndsWithTheSummary(t *testing.T) {
	cfg, res := reviewFixture(t)
	out, err := reviewRun(t, cfg, res, true, false, false, "y\ns\n")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(out, "No signing key found"), out)
	require.True(t, strings.HasSuffix(out, "\nReview complete: 0 trusted, 0 rejected, 1 skipped — 1 still pending.\n"), out)
}
