package fragments

import (
	"strings"
	"testing"

	"github.com/benjaminabbitt/mlcm/internal/fsys"
)

func TestNewLoader(t *testing.T) {
	dirs := []string{"/test/dir1", "/test/dir2"}
	loader := NewLoader(dirs)

	if len(loader.searchDirs) != 2 {
		t.Errorf("expected 2 search dirs, got %d", len(loader.searchDirs))
	}
	if loader.fs == nil {
		t.Error("expected fs to be set")
	}
	if loader.warnFunc == nil {
		t.Error("expected warnFunc to be set")
	}
}

func TestNewLoaderWithOptions(t *testing.T) {
	fs := fsys.NewMapFS()
	var warnings []string
	warnFunc := func(msg string) {
		warnings = append(warnings, msg)
	}

	loader := NewLoader([]string{"/test"},
		WithFS(fs),
		WithWarnFunc(warnFunc),
		WithSuppressWarnings(true),
	)

	if loader.fs != fs {
		t.Error("expected custom fs to be set")
	}
	if loader.suppressWarnings != true {
		t.Error("expected suppressWarnings to be true")
	}

	// Warn should not append when suppressed
	loader.warn("test warning")
	if len(warnings) != 0 {
		t.Errorf("expected no warnings when suppressed, got %d", len(warnings))
	}
}

func TestLoaderFind(t *testing.T) {
	fs := fsys.NewMapFS()
	fs.AddDir("/fragments")
	fs.AddDir("/fragments/testing")
	fs.AddFile("/fragments/simple.yaml", []byte("content: Simple"))
	fs.AddFile("/fragments/testing/tdd.yaml", []byte("content: TDD"))

	loader := NewLoader([]string{"/fragments"}, WithFS(fs))

	tests := []struct {
		name     string
		expected string
		wantErr  bool
	}{
		{"simple", "/fragments/simple.yaml", false},
		{"simple.yaml", "/fragments/simple.yaml", false},
		{"testing/tdd", "/fragments/testing/tdd.yaml", false},
		{"testing/tdd.yaml", "/fragments/testing/tdd.yaml", false},
		{"tdd", "/fragments/testing/tdd.yaml", false}, // Basename lookup
		{"nonexistent", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, err := loader.Find(tt.name)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for %q", tt.name)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error for %q: %v", tt.name, err)
				}
				if path != tt.expected {
					t.Errorf("expected %q, got %q", tt.expected, path)
				}
			}
		})
	}
}

func TestLoaderFindPriority(t *testing.T) {
	fs := fsys.NewMapFS()
	fs.AddDir("/dir1")
	fs.AddDir("/dir2")
	fs.AddFile("/dir1/first.yaml", []byte("content: First"))
	fs.AddFile("/dir2/first.yaml", []byte("content: Second"))
	fs.AddFile("/dir2/second.yaml", []byte("content: Second Only"))

	loader := NewLoader([]string{"/dir1", "/dir2"}, WithFS(fs))

	// First directory takes priority
	path, err := loader.Find("first")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/dir1/first.yaml" {
		t.Errorf("expected /dir1/first.yaml, got %s", path)
	}

	// Falls back to second directory
	path, err = loader.Find("second")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/dir2/second.yaml" {
		t.Errorf("expected /dir2/second.yaml, got %s", path)
	}
}

func TestLoaderLoad(t *testing.T) {
	fs := fsys.NewMapFS()
	fs.AddDir("/fragments")
	fs.AddFile("/fragments/test.yaml", []byte(`tags:
  - testing
variables:
  - project
  - language
content: |
  This is the context content.
`))

	loader := NewLoader([]string{"/fragments"}, WithFS(fs))

	frag, err := loader.Load("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if frag.Name != "test" {
		t.Errorf("expected name 'test', got %q", frag.Name)
	}
	if !strings.Contains(frag.Content, "This is the context content.") {
		t.Errorf("expected content to contain content, got %q", frag.Content)
	}
	if len(frag.Tags) != 1 || frag.Tags[0] != "testing" {
		t.Errorf("unexpected tags: %v", frag.Tags)
	}
	if len(frag.Variables) != 2 {
		t.Errorf("expected 2 variables, got %d", len(frag.Variables))
	}
}

func TestLoaderLoadSimpleContent(t *testing.T) {
	fs := fsys.NewMapFS()
	fs.AddDir("/fragments")
	fs.AddFile("/fragments/simple.yaml", []byte(`content: Just some plain content without sections.`))

	loader := NewLoader([]string{"/fragments"}, WithFS(fs))

	frag, err := loader.Load("simple")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if frag.Content != "Just some plain content without sections." {
		t.Errorf("unexpected content: %q", frag.Content)
	}
}

func TestLoaderLoadMultiple(t *testing.T) {
	fs := fsys.NewMapFS()
	fs.AddDir("/fragments")
	fs.AddFile("/fragments/frag1.yaml", []byte(`content: |
  Fragment 1 content.
`))
	fs.AddFile("/fragments/frag2.yaml", []byte(`content: |
  Fragment 2 content.
`))

	loader := NewLoader([]string{"/fragments"}, WithFS(fs), WithSuppressWarnings(true))

	content, err := loader.LoadMultiple([]string{"frag1", "frag2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(content, "Fragment 1 content.") {
		t.Error("expected content to contain Fragment 1")
	}
	if !strings.Contains(content, "Fragment 2 content.") {
		t.Error("expected content to contain Fragment 2")
	}
	if !strings.Contains(content, "---") {
		t.Error("expected content to contain separator")
	}
}

func TestLoaderLoadMultipleWithVars(t *testing.T) {
	fs := fsys.NewMapFS()
	fs.AddDir("/fragments")
	fs.AddFile("/fragments/template.yaml", []byte(`variables:
  - project_name
content: |
  Project: {{project_name}}
`))

	loader := NewLoader([]string{"/fragments"}, WithFS(fs), WithSuppressWarnings(true))

	vars := map[string]string{"project_name": "MyProject"}
	content, err := loader.LoadMultipleWithVars([]string{"template"}, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(content, "Project: MyProject") {
		t.Errorf("expected template to be rendered, got %q", content)
	}
}

func TestLoaderLoadMultipleMissingFragment(t *testing.T) {
	fs := fsys.NewMapFS()
	fs.AddDir("/fragments")
	fs.AddFile("/fragments/exists.yaml", []byte(`content: |
  Existing fragment.
`))

	var warnings []string
	loader := NewLoader([]string{"/fragments"},
		WithFS(fs),
		WithWarnFunc(func(msg string) { warnings = append(warnings, msg) }),
	)

	content, err := loader.LoadMultiple([]string{"exists", "missing"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(content, "Existing fragment.") {
		t.Error("expected content from existing fragment")
	}
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning, got %d", len(warnings))
	}
	if !strings.Contains(warnings[0], "missing") {
		t.Errorf("expected warning about missing fragment, got %q", warnings[0])
	}
}

func TestLoaderList(t *testing.T) {
	fs := fsys.NewMapFS()
	fs.AddDir("/fragments")
	fs.AddDir("/fragments/testing")
	fs.AddFile("/fragments/simple.yaml", []byte("content: Simple"))
	fs.AddFile("/fragments/testing/tdd.yaml", []byte("content: TDD"))
	fs.AddFile("/fragments/testing/unit.yaml", []byte("content: Unit"))
	fs.AddFile("/fragments/README.txt", []byte("Not a fragment"))

	loader := NewLoader([]string{"/fragments"}, WithFS(fs))

	frags, err := loader.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(frags) != 3 {
		t.Errorf("expected 3 fragments, got %d", len(frags))
	}

	names := make(map[string]bool)
	for _, f := range frags {
		names[f.Name] = true
	}

	expected := []string{"simple", "testing/tdd", "testing/unit"}
	for _, e := range expected {
		if !names[e] {
			t.Errorf("expected fragment %q to be listed", e)
		}
	}
}

func TestParseYAML(t *testing.T) {
	content := `variables:
  - git_branch
content: |
  Generator output content.
exports:
  git_branch: main
`

	frag, err := ParseYAML(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(frag.Content, "Generator output content.") {
		t.Errorf("unexpected content: %q", frag.Content)
	}
	if frag.Exports["git_branch"] != "main" {
		t.Errorf("expected git_branch=main, got %q", frag.Exports["git_branch"])
	}
}

func TestUndefinedVariableWarning(t *testing.T) {
	fs := fsys.NewMapFS()
	fs.AddDir("/fragments")
	fs.AddFile("/fragments/template.yaml", []byte(`content: |
  Hello {{undefined_var}}!
`))

	var warnings []string
	loader := NewLoader([]string{"/fragments"},
		WithFS(fs),
		WithWarnFunc(func(msg string) { warnings = append(warnings, msg) }),
	)

	_, err := loader.LoadMultiple([]string{"template"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, w := range warnings {
		if strings.Contains(w, "undefined_var") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about undefined_var, got %v", warnings)
	}
}

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "fragment", false},
		{"valid with path", "testing/tdd", false},
		{"valid nested", "a/b/c", false},
		{"empty name", "", true},
		{"null byte", "test\x00name", true},
		{"absolute path unix", "/etc/passwd", true},
		{"path traversal simple", "../secret", true},
		{"path traversal nested", "a/../../../etc/passwd", true},
		{"path traversal in middle", "a/b/../../../secret", true},
		{"double dot only", "..", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateName(tt.input)
			if tt.wantErr && err == nil {
				t.Errorf("expected error for %q", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for %q: %v", tt.input, err)
			}
		})
	}
}

func TestLoaderFindPathTraversal(t *testing.T) {
	fs := fsys.NewMapFS()
	fs.AddDir("/fragments")
	fs.AddFile("/fragments/safe.yaml", []byte("content: Safe"))

	loader := NewLoader([]string{"/fragments"}, WithFS(fs))

	// These should all fail with path traversal errors
	badNames := []string{
		"../../../etc/passwd",
		"..\\..\\windows\\system32",
		"/etc/passwd",
		"test\x00file",
		"",
	}

	for _, name := range badNames {
		t.Run(name, func(t *testing.T) {
			_, err := loader.Find(name)
			if err == nil {
				t.Errorf("expected error for path traversal attempt: %q", name)
			}
		})
	}

	// Valid name should still work
	path, err := loader.Find("safe")
	if err != nil {
		t.Errorf("unexpected error for valid name: %v", err)
	}
	if path != "/fragments/safe.yaml" {
		t.Errorf("unexpected path: %s", path)
	}
}

func TestParseYAMLFragment(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		expectedTags []string
		expectedVars []string
		expectedCtx  string
	}{
		{
			name: "full format",
			input: `tags:
  - review
  - security
variables:
  - project_name
  - language
content: |
  # Review Guidelines

  Follow these guidelines for {{project_name}}.`,
			expectedTags: []string{"review", "security"},
			expectedVars: []string{"project_name", "language"},
			expectedCtx:  "# Review Guidelines",
		},
		{
			name: "content only",
			input: `content: |
  Simple content without tags or variables.`,
			expectedTags: nil,
			expectedVars: nil,
			expectedCtx:  "Simple content without tags or variables.",
		},
		{
			name: "tags only",
			input: `tags:
  - style
content: |
  Style guide content.`,
			expectedTags: []string{"style"},
			expectedVars: nil,
			expectedCtx:  "Style guide content.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frag, err := parseYAMLFragment([]byte(tt.input))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !strings.Contains(frag.Content, tt.expectedCtx) {
				t.Errorf("expected content to contain %q, got %q", tt.expectedCtx, frag.Content)
			}

			if len(frag.Tags) != len(tt.expectedTags) {
				t.Errorf("expected %d tags, got %d", len(tt.expectedTags), len(frag.Tags))
			}
			for i, tag := range tt.expectedTags {
				if i < len(frag.Tags) && frag.Tags[i] != tag {
					t.Errorf("expected tag %q at index %d, got %q", tag, i, frag.Tags[i])
				}
			}

			if len(frag.Variables) != len(tt.expectedVars) {
				t.Errorf("expected %d variables, got %d", len(tt.expectedVars), len(frag.Variables))
			}
			for i, v := range tt.expectedVars {
				if i < len(frag.Variables) && frag.Variables[i] != v {
					t.Errorf("expected variable %q at index %d, got %q", v, i, frag.Variables[i])
				}
			}
		})
	}
}

func TestLoaderFindYAMLExtensions(t *testing.T) {
	fs := fsys.NewMapFS()
	fs.AddDir("/fragments")
	fs.AddFile("/fragments/yaml-ext.yaml", []byte("content: YAML version"))
	fs.AddFile("/fragments/yml-ext.yml", []byte("content: YML version"))

	loader := NewLoader([]string{"/fragments"}, WithFS(fs))

	// .yaml files should be found
	path, err := loader.Find("yaml-ext")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/fragments/yaml-ext.yaml" {
		t.Errorf("expected .yaml file, got %s", path)
	}

	// .yml files should be found
	path, err = loader.Find("yml-ext")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/fragments/yml-ext.yml" {
		t.Errorf("expected .yml file, got %s", path)
	}
}

func TestLoaderLoadYAMLFragment(t *testing.T) {
	fs := fsys.NewMapFS()
	fs.AddDir("/fragments")
	fs.AddFile("/fragments/test.yaml", []byte(`tags:
  - testing
  - unit
variables:
  - project_name
content: |
  # Test Fragment

  Testing content for {{project_name}}.
`))

	loader := NewLoader([]string{"/fragments"}, WithFS(fs))

	frag, err := loader.Load("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if frag.Name != "test" {
		t.Errorf("expected name 'test', got %q", frag.Name)
	}
	if !strings.Contains(frag.Content, "# Test Fragment") {
		t.Errorf("expected content to contain '# Test Fragment', got %q", frag.Content)
	}
	if len(frag.Tags) != 2 || frag.Tags[0] != "testing" || frag.Tags[1] != "unit" {
		t.Errorf("unexpected tags: %v", frag.Tags)
	}
	if len(frag.Variables) != 1 || frag.Variables[0] != "project_name" {
		t.Errorf("unexpected variables: %v", frag.Variables)
	}
}

func TestLoaderListByTags(t *testing.T) {
	fs := fsys.NewMapFS()
	fs.AddDir("/fragments")
	fs.AddFile("/fragments/review.yaml", []byte(`tags:
  - review
  - security
content: Review content.
`))
	fs.AddFile("/fragments/style.yaml", []byte(`tags:
  - style
content: Style content.
`))
	fs.AddFile("/fragments/both.yaml", []byte(`tags:
  - review
  - style
content: Both content.
`))

	loader := NewLoader([]string{"/fragments"}, WithFS(fs))

	// Find by single tag
	frags, err := loader.ListByTags([]string{"review"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frags) != 2 {
		t.Errorf("expected 2 fragments with 'review' tag, got %d", len(frags))
	}

	// Find by multiple tags (OR logic)
	frags, err = loader.ListByTags([]string{"style", "security"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frags) != 3 {
		t.Errorf("expected 3 fragments with 'style' or 'security' tag, got %d", len(frags))
	}

	// Empty tags returns all
	frags, err = loader.ListByTags(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frags) != 3 {
		t.Errorf("expected 3 fragments when no tags specified, got %d", len(frags))
	}
}

func TestFragmentHasTag(t *testing.T) {
	frag := &Fragment{
		Tags: []string{"Review", "Security"},
	}

	if !frag.HasTag("review") {
		t.Error("expected HasTag to be case-insensitive")
	}
	if !frag.HasTag("SECURITY") {
		t.Error("expected HasTag to match uppercase")
	}
	if frag.HasTag("style") {
		t.Error("expected HasTag to return false for missing tag")
	}
}

func TestFragmentHasAnyTag(t *testing.T) {
	frag := &Fragment{
		Tags: []string{"review", "security"},
	}

	if !frag.HasAnyTag([]string{"style", "review"}) {
		t.Error("expected HasAnyTag to return true when one tag matches")
	}
	if frag.HasAnyTag([]string{"style", "testing"}) {
		t.Error("expected HasAnyTag to return false when no tags match")
	}
	if frag.HasAnyTag(nil) {
		t.Error("expected HasAnyTag to return false for empty tag list")
	}
}

func TestCombineFragments(t *testing.T) {
	frags := []*Fragment{
		{Content: "First fragment."},
		{Content: "Second fragment."},
		{Content: ""}, // Empty should be skipped
		{Content: "Third fragment."},
	}

	combined := CombineFragments(frags)

	if !strings.Contains(combined, "First fragment.") {
		t.Error("expected combined to contain first fragment")
	}
	if !strings.Contains(combined, "Second fragment.") {
		t.Error("expected combined to contain second fragment")
	}
	if !strings.Contains(combined, "Third fragment.") {
		t.Error("expected combined to contain third fragment")
	}
	if strings.Count(combined, "---") != 2 {
		t.Errorf("expected 2 separators, got %d", strings.Count(combined, "---"))
	}
}

func TestLoadByTags(t *testing.T) {
	fs := fsys.NewMapFS()
	fs.AddDir("/fragments")
	fs.AddFile("/fragments/lang-go.yaml", []byte(`tags:
  - golang
  - language
content: |
  Go development guidelines.
`))
	fs.AddFile("/fragments/lang-python.yaml", []byte(`tags:
  - python
  - language
content: |
  Python development guidelines.
`))
	fs.AddFile("/fragments/tdd.yaml", []byte(`tags:
  - testing
  - workflow
content: |
  TDD workflow.
`))

	loader := NewLoader([]string{"/fragments"}, WithFS(fs))

	// Load only golang fragments
	frags, err := loader.LoadByTags([]string{"golang"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frags) != 1 {
		t.Errorf("expected 1 golang fragment, got %d", len(frags))
	}
	if len(frags) > 0 && !strings.Contains(frags[0].Content, "Go development") {
		t.Errorf("expected Go content, got %q", frags[0].Content)
	}

	// Load all language fragments
	frags, err = loader.LoadByTags([]string{"language"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frags) != 2 {
		t.Errorf("expected 2 language fragments, got %d", len(frags))
	}
}

func TestPersonaTagSelection(t *testing.T) {
	// Simulates how a persona with tags selects fragments
	fs := fsys.NewMapFS()
	fs.AddDir("/fragments")
	fs.AddFile("/fragments/lang-go.yaml", []byte(`tags:
  - golang
  - language
content: |
  # Go Development
  Use Go modules. Run golangci-lint.
`))
	fs.AddFile("/fragments/tdd.yaml", []byte(`tags:
  - testing
  - workflow
content: |
  # TDD
  Red-green-refactor cycle.
`))
	fs.AddFile("/fragments/git.yaml", []byte(`tags:
  - git
  - workflow
content: |
  # Git Practices
  Use conventional commits.
`))
	fs.AddFile("/fragments/review-security.yaml", []byte(`tags:
  - review
  - security
content: |
  # Security Review
  Check for injection vulnerabilities.
`))

	loader := NewLoader([]string{"/fragments"}, WithFS(fs), WithSuppressWarnings(true))

	// Simulate go-developer persona: tags=[golang], fragments=[tdd, git]
	personaTags := []string{"golang"}
	explicitFragments := []string{"tdd", "git"}

	// Get fragments matching tags
	taggedInfos, err := loader.ListByTags(personaTags)
	if err != nil {
		t.Fatalf("unexpected error listing by tags: %v", err)
	}

	// Collect all fragment names
	var allFragmentNames []string
	for _, info := range taggedInfos {
		allFragmentNames = append(allFragmentNames, info.Name)
	}
	allFragmentNames = append(allFragmentNames, explicitFragments...)

	// Load and combine
	content, err := loader.LoadMultiple(allFragmentNames)
	if err != nil {
		t.Fatalf("unexpected error loading: %v", err)
	}

	// Verify all expected content is present
	if !strings.Contains(content, "Go Development") {
		t.Error("expected Go content from tag match")
	}
	if !strings.Contains(content, "TDD") {
		t.Error("expected TDD content from explicit fragment")
	}
	if !strings.Contains(content, "Git Practices") {
		t.Error("expected Git content from explicit fragment")
	}
	if strings.Contains(content, "Security Review") {
		t.Error("security content should NOT be included (not in tags or explicit)")
	}
}

func TestTagSelectionCaseInsensitive(t *testing.T) {
	fs := fsys.NewMapFS()
	fs.AddDir("/fragments")
	fs.AddFile("/fragments/go.yaml", []byte(`tags:
  - GoLang
  - Language
content: Go content.
`))

	loader := NewLoader([]string{"/fragments"}, WithFS(fs))

	// Search with lowercase
	frags, err := loader.ListByTags([]string{"golang"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frags) != 1 {
		t.Errorf("expected case-insensitive match, got %d fragments", len(frags))
	}

	// Search with uppercase
	frags, err = loader.ListByTags([]string{"LANGUAGE"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frags) != 1 {
		t.Errorf("expected case-insensitive match, got %d fragments", len(frags))
	}
}

func TestParseNoDistill(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		expectFlag bool
	}{
		{
			name: "no_distill true",
			input: `no_distill: true
content: Do not compress this.`,
			expectFlag: true,
		},
		{
			name: "no_distill false",
			input: `no_distill: false
content: Can compress this.`,
			expectFlag: false,
		},
		{
			name: "no_distill absent",
			input: `content: Default behavior.`,
			expectFlag: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frag, err := parseYAMLFragment([]byte(tt.input))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if frag.NoDistill != tt.expectFlag {
				t.Errorf("expected NoDistill=%v, got %v", tt.expectFlag, frag.NoDistill)
			}
		})
	}
}

func TestSaveNoDistill(t *testing.T) {
	// Create temp directory for test
	tmpDir := t.TempDir()
	path := tmpDir + "/test.yaml"

	frag := &Fragment{
		Path:      path,
		Version:   "1.0",
		Tags:      []string{"test"},
		NoDistill: true,
		Content:   "Test content.",
	}

	// Save the fragment
	err := frag.Save()
	if err != nil {
		t.Fatalf("unexpected error saving: %v", err)
	}

	// Load it back via standard loader
	loader := NewLoader([]string{tmpDir})
	loaded, err := loader.Load("test")
	if err != nil {
		t.Fatalf("unexpected error loading: %v", err)
	}

	// Verify NoDistill was preserved
	if !loaded.NoDistill {
		t.Error("expected NoDistill to be true after round-trip")
	}
	if loaded.Content != "Test content." {
		t.Errorf("expected content preserved, got %q", loaded.Content)
	}
}

func TestEffectiveContentFallback(t *testing.T) {
	tests := []struct {
		name            string
		content         string
		distilled       string
		preferDistilled bool
		expected        string
	}{
		{
			name:            "prefer distilled when available",
			content:         "full content",
			distilled:       "compressed",
			preferDistilled: true,
			expected:        "compressed",
		},
		{
			name:            "fallback to content when no distilled",
			content:         "full content",
			distilled:       "",
			preferDistilled: true,
			expected:        "full content",
		},
		{
			name:            "use content when not preferring distilled",
			content:         "full content",
			distilled:       "compressed",
			preferDistilled: false,
			expected:        "full content",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frag := &Fragment{
				Content:   tt.content,
				Distilled: tt.distilled,
			}
			result := frag.EffectiveContent(tt.preferDistilled)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}
