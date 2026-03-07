package operations

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/benjaminabbitt/scm/internal/config"
)

func TestFindSourceContextFile(t *testing.T) {
	tests := []struct {
		name         string
		files        map[string]string
		wantFile     string
		wantErr      bool
	}{
		{
			name:     "prefers llm.md",
			files:    map[string]string{"llm.md": "llm content", "scm.md": "scm content"},
			wantFile: "llm.md",
		},
		{
			name:     "falls back to scm.md",
			files:    map[string]string{"scm.md": "scm content"},
			wantFile: "scm.md",
		},
		{
			name:    "no source file",
			files:   map[string]string{},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			for name, content := range tc.files {
				_ = afero.WriteFile(fs, "/work/"+name, []byte(content), 0644)
			}

			file, _, err := findSourceContextFile(fs, "/work")
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if file != tc.wantFile {
				t.Errorf("got file %q, want %q", file, tc.wantFile)
			}
		})
	}
}

func TestTransformForBackend(t *testing.T) {
	content := "# My Context\n\nSome instructions."
	result := transformForBackend(content, "claude-code", "llm.md")

	// Should have header with SCM:MANAGED marker
	if !strings.Contains(result, "SCM:MANAGED") {
		t.Error("missing SCM:MANAGED marker")
	}
	if !strings.Contains(result, "This file is auto-generated from llm.md by SCM") {
		t.Error("missing auto-generated header")
	}
	if !strings.Contains(result, "Source: llm.md") {
		t.Error("missing source file reference")
	}
	if !strings.Contains(result, "Backend: claude-code") {
		t.Error("missing backend reference")
	}
	if !strings.Contains(result, "Edit llm.md instead") {
		t.Error("missing edit instructions")
	}
	// Should have original content
	if !strings.Contains(result, "# My Context") {
		t.Error("missing original content")
	}
}

func TestWriteContextFile_CreatesNew(t *testing.T) {
	fs := afero.NewMemMapFs()
	content := buildDerivedHeader("llm.md", "claude-code") + "content"

	status, err := writeContextFile(fs, "/work/CLAUDE.md", content, "llm.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "created" {
		t.Errorf("got status %q, want 'created'", status)
	}

	// Verify file was written
	data, _ := afero.ReadFile(fs, "/work/CLAUDE.md")
	if string(data) != content {
		t.Error("file content mismatch")
	}
}

func TestWriteContextFile_UpdatesExisting(t *testing.T) {
	fs := afero.NewMemMapFs()
	oldContent := buildDerivedHeader("llm.md", "claude-code") + "old content"
	newContent := buildDerivedHeader("llm.md", "claude-code") + "new content"

	_ = afero.WriteFile(fs, "/work/CLAUDE.md", []byte(oldContent), 0644)

	status, err := writeContextFile(fs, "/work/CLAUDE.md", newContent, "llm.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "updated" {
		t.Errorf("got status %q, want 'updated'", status)
	}
}

func TestWriteContextFile_SkipsUnchanged(t *testing.T) {
	fs := afero.NewMemMapFs()
	content := buildDerivedHeader("llm.md", "claude-code") + "content"

	_ = afero.WriteFile(fs, "/work/CLAUDE.md", []byte(content), 0644)

	status, err := writeContextFile(fs, "/work/CLAUDE.md", content, "llm.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "skipped" {
		t.Errorf("got status %q, want 'skipped'", status)
	}
}

func TestWriteContextFile_SkipsUserFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	userContent := "# My Custom CLAUDE.md\n\nUser's own instructions."
	newContent := buildDerivedHeader("llm.md", "claude-code") + "scm content"

	_ = afero.WriteFile(fs, "/work/CLAUDE.md", []byte(userContent), 0644)

	status, err := writeContextFile(fs, "/work/CLAUDE.md", newContent, "llm.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "user_managed" {
		t.Errorf("got status %q, want 'user_managed' (user file)", status)
	}

	// Verify user file wasn't overwritten
	data, _ := afero.ReadFile(fs, "/work/CLAUDE.md")
	if string(data) != userContent {
		t.Error("user file was overwritten")
	}
}

func TestTransformContext_NoSourceFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = fs.MkdirAll("/work/.scm", 0755)

	cfg := &config.Config{
		SCMRoot: "/work",
	}

	result, err := TransformContext(context.Background(), cfg, TransformContextRequest{FS: fs})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "no_source" {
		t.Errorf("got status %q, want 'no_source'", result.Status)
	}
}

func TestTransformContext_GeneratesFiles(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = fs.MkdirAll("/work/.scm", 0755)
	_ = afero.WriteFile(fs, "/work/llm.md", []byte("# Instructions"), 0644)

	cfg := &config.Config{
		SCMRoot: "/work",
	}

	result, err := TransformContext(context.Background(), cfg, TransformContextRequest{
		FS:       fs,
		Backends: []string{"claude-code", "gemini"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("got status %q, want 'completed'", result.Status)
	}
	if result.SourceFile != "llm.md" {
		t.Errorf("got source %q, want 'llm.md'", result.SourceFile)
	}
	if len(result.Generated) != 2 {
		t.Errorf("got %d generated files, want 2", len(result.Generated))
	}

	// Verify files were created
	claudeData, _ := afero.ReadFile(fs, "/work/CLAUDE.md")
	if !strings.Contains(string(claudeData), "# Instructions") {
		t.Error("CLAUDE.md missing content")
	}
	geminiData, _ := afero.ReadFile(fs, "/work/GEMINI.md")
	if !strings.Contains(string(geminiData), "# Instructions") {
		t.Error("GEMINI.md missing content")
	}
}

func TestTransformContext_UnknownBackend(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/work/llm.md", []byte("content"), 0644)

	cfg := &config.Config{
		SCMRoot: "/work",
	}

	result, err := TransformContext(context.Background(), cfg, TransformContextRequest{
		FS:       fs,
		Backends: []string{"unknown-backend"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "completed_with_errors" {
		t.Errorf("got status %q, want 'completed_with_errors'", result.Status)
	}
	if len(result.Errors) != 1 {
		t.Errorf("got %d errors, want 1", len(result.Errors))
	}
}

func TestTransformContext_WarnsOnUserManagedFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/work/llm.md", []byte("# Instructions"), 0644)
	// Create a user-managed CLAUDE.md (no SCM:MANAGED marker)
	_ = afero.WriteFile(fs, "/work/CLAUDE.md", []byte("# My custom instructions"), 0644)

	cfg := &config.Config{
		SCMRoot: "/work",
	}

	result, err := TransformContext(context.Background(), cfg, TransformContextRequest{
		FS:       fs,
		Backends: []string{"claude-code"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("got status %q, want 'completed'", result.Status)
	}
	if len(result.Warnings) != 1 {
		t.Errorf("got %d warnings, want 1", len(result.Warnings))
	} else {
		if !strings.Contains(result.Warnings[0], "CLAUDE.md exists but is not SCM-managed") {
			t.Errorf("unexpected warning: %s", result.Warnings[0])
		}
		if !strings.Contains(result.Warnings[0], "edit llm.md instead") {
			t.Errorf("warning should mention source file: %s", result.Warnings[0])
		}
	}

	// User file should not have been overwritten
	data, _ := afero.ReadFile(fs, "/work/CLAUDE.md")
	if strings.Contains(string(data), "SCM:MANAGED") {
		t.Error("user file was overwritten")
	}
}
