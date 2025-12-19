// git-context is a context generator that outputs git repository information.
// It produces a markdown fragment with current branch, status, and recent commits.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"mlcm/internal/markdown"
)

func main() {
	if !isGitRepo() {
		fmt.Fprintln(os.Stderr, "not a git repository")
		os.Exit(1)
	}

	doc := buildFragment()
	fmt.Print(doc)
}

func buildFragment() string {
	branch := gitBranch()
	status := gitStatus()
	recentCommits := gitRecentCommits(5)
	remoteURL := gitRemoteURL()

	frag := markdown.NewFragment()

	// Build context content
	ctx := frag.Context()
	ctx.P("Current git repository state:")
	ctx.BulletBold("Branch", markdown.Code(branch))
	if remoteURL != "" {
		ctx.BulletBold("Remote", markdown.Code(remoteURL))
	}

	if status != "" {
		ctx.P(markdown.Bold("Working tree status:"))
		ctx.CodeBlock("", status)
	} else {
		ctx.P("Working tree is clean.")
	}

	if recentCommits != "" {
		ctx.P(markdown.Bold("Recent commits:"))
		ctx.CodeBlock("", recentCommits)
	}

	// Set variables
	frag.SetVar("git_branch", branch)
	frag.SetVar("git_remote", remoteURL)

	return frag.String()
}

func isGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Stderr = nil
	return cmd.Run() == nil
}

func gitBranch() string {
	return runGit("rev-parse", "--abbrev-ref", "HEAD")
}

func gitStatus() string {
	return runGit("status", "--short")
}

func gitRecentCommits(n int) string {
	return runGit("log", "--oneline", fmt.Sprintf("-%d", n))
}

func gitRemoteURL() string {
	return runGit("remote", "get-url", "origin")
}

func runGit(args ...string) string {
	cmd := exec.Command("git", args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(stdout.String())
}
