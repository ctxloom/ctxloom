// git-context is a context generator that outputs git repository information.
// It produces a markdown fragment with current branch, status, and recent commits.
package main

import (
	"fmt"
	"os"

	"mlcm/internal/gitutil"
	"mlcm/internal/markdown"
)

// recentCommitCount is the number of recent commits to include in the context.
// This provides enough history to understand recent changes without overwhelming
// the context with too much information.
const recentCommitCount = 5

func main() {
	repo, err := gitutil.Open(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, "not a git repository")
		os.Exit(1)
	}

	doc := buildFragment(repo)
	fmt.Print(doc)
}

func buildFragment(repo *gitutil.Repo) string {
	branch, _ := repo.Branch()
	status, _ := repo.StatusShort()
	remoteURL := repo.RemoteURL("origin")

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

	recentCommits := formatCommits(repo, recentCommitCount)
	if recentCommits != "" {
		ctx.P(markdown.Bold("Recent commits:"))
		ctx.CodeBlock("", recentCommits)
	}

	// Set variables
	frag.SetVar("git_branch", branch)
	frag.SetVar("git_remote", remoteURL)

	return frag.String()
}

func formatCommits(repo *gitutil.Repo, n int) string {
	commits, err := repo.RecentCommits(n)
	if err != nil || len(commits) == 0 {
		return ""
	}

	result := ""
	for i, c := range commits {
		if i > 0 {
			result += "\n"
		}
		result += c.Hash + " " + c.Message
	}
	return result
}
