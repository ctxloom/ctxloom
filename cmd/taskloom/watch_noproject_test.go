package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// A watch has nothing to follow in a directory with no project yet, and it
// must not mint one just to have a file to watch: it refuses, naming the
// write that establishes the project, and leaves nothing behind.
func TestWatch_RefusesWhenThereIsNoProjectYet(t *testing.T) {
	dir := taskstest.ProjectDir(t)
	init := exec.Command("git", "init", "-q")
	init.Dir = dir
	require.NoError(t, init.Run())
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &cobra.Command{}
	c.Flags().String("format", "text", "")
	c.SetContext(ctx)
	c.SetOut(&syncBuffer{})

	done := make(chan error, 1)
	go func() { done <- watchCmd.RunE(c, nil) }()
	var got error
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("watch started streaming in a directory with no project yet")
	}

	require.ErrorIs(t, got, errNoProjectYet)
	marker, err := projectid.ReadMarker(dir)
	require.NoError(t, err)
	require.Empty(t, marker, "watch must not mint a project marker")
	require.NoDirExists(t, filepath.Join(home, ".ctxloom"), "watch must not create the registry or the task store")
}
