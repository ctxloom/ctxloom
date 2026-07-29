package remote

import (
	"reflect"
	"testing"
)

func TestParseSearchQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  SearchQuery
	}{
		{
			name:  "plain text",
			query: "security",
			want: SearchQuery{
				Text: "security",
				Tags: TagQuery{Operator: TagOperatorAND},
			},
		},
		{
			name:  "single tag",
			query: "tag:security",
			want: SearchQuery{
				Tags: TagQuery{
					Tags:     []string{"security"},
					Operator: TagOperatorAND,
				},
			},
		},
		{
			name:  "multiple tags with AND (implicit)",
			query: "tag:security/golang",
			want: SearchQuery{
				Tags: TagQuery{
					Tags:     []string{"security", "golang"},
					Operator: TagOperatorAND,
				},
			},
		},
		{
			name:  "multiple tags with AND (explicit)",
			query: "tag:security/golang/AND",
			want: SearchQuery{
				Tags: TagQuery{
					Tags:     []string{"security", "golang"},
					Operator: TagOperatorAND,
				},
			},
		},
		{
			name:  "multiple tags with OR",
			query: "tag:security/golang/OR",
			want: SearchQuery{
				Tags: TagQuery{
					Tags:     []string{"security", "golang"},
					Operator: TagOperatorOR,
				},
			},
		},
		{
			name:  "negated tag",
			query: "tag:deprecated/NOT",
			want: SearchQuery{
				Tags: TagQuery{
					Tags:     []string{"deprecated"},
					Operator: TagOperatorAND,
					Negated:  true,
				},
			},
		},
		{
			name:  "author filter",
			query: "author:alice",
			want: SearchQuery{
				Tags:   TagQuery{Operator: TagOperatorAND},
				Author: "alice",
			},
		},
		{
			name:  "version filter",
			query: "version:1.0",
			want: SearchQuery{
				Tags:    TagQuery{Operator: TagOperatorAND},
				Version: "1.0",
			},
		},
		{
			name:  "combined filters",
			query: "security tag:golang author:alice",
			want: SearchQuery{
				Text: "security",
				Tags: TagQuery{
					Tags:     []string{"golang"},
					Operator: TagOperatorAND,
				},
				Author: "alice",
			},
		},
		{
			name:  "complex tag expression",
			query: "tag:security/testing/NOT/OR",
			want: SearchQuery{
				Tags: TagQuery{
					Tags:     []string{"security", "testing"},
					Operator: TagOperatorOR,
					Negated:  true,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseSearchQuery(tt.query)

			if got.Text != tt.want.Text {
				t.Errorf("Text = %q, want %q", got.Text, tt.want.Text)
			}
			if got.Author != tt.want.Author {
				t.Errorf("Author = %q, want %q", got.Author, tt.want.Author)
			}
			if got.Version != tt.want.Version {
				t.Errorf("Version = %q, want %q", got.Version, tt.want.Version)
			}
			if got.Tags.Operator != tt.want.Tags.Operator {
				t.Errorf("Tags.Operator = %q, want %q", got.Tags.Operator, tt.want.Tags.Operator)
			}
			if got.Tags.Negated != tt.want.Tags.Negated {
				t.Errorf("Tags.Negated = %v, want %v", got.Tags.Negated, tt.want.Tags.Negated)
			}
			if !reflect.DeepEqual(got.Tags.Tags, tt.want.Tags.Tags) {
				t.Errorf("Tags.Tags = %v, want %v", got.Tags.Tags, tt.want.Tags.Tags)
			}
		})
	}
}

func TestMatchesQuery(t *testing.T) {
	entry := ManifestEntry{
		Name:        "owasp-top-10",
		Tags:        []string{"security", "golang", "web"},
		Author:      "alice",
		Description: "OWASP Top 10 security guidelines for Go",
		Version:     "1.2.0",
	}

	tests := []struct {
		name  string
		query SearchQuery
		want  bool
	}{
		{
			name: "text match on name",
			query: SearchQuery{
				Text: "owasp",
				Tags: TagQuery{Operator: TagOperatorAND},
			},
			want: true,
		},
		{
			name: "text match on description",
			query: SearchQuery{
				Text: "guidelines",
				Tags: TagQuery{Operator: TagOperatorAND},
			},
			want: true,
		},
		{
			name: "text no match",
			query: SearchQuery{
				Text: "javascript",
				Tags: TagQuery{Operator: TagOperatorAND},
			},
			want: false,
		},
		{
			name: "author match",
			query: SearchQuery{
				Tags:   TagQuery{Operator: TagOperatorAND},
				Author: "alice",
			},
			want: true,
		},
		{
			name: "author no match",
			query: SearchQuery{
				Tags:   TagQuery{Operator: TagOperatorAND},
				Author: "bob",
			},
			want: false,
		},
		{
			name: "single tag match",
			query: SearchQuery{
				Tags: TagQuery{
					Tags:     []string{"security"},
					Operator: TagOperatorAND,
				},
			},
			want: true,
		},
		{
			name: "multiple tags AND match",
			query: SearchQuery{
				Tags: TagQuery{
					Tags:     []string{"security", "golang"},
					Operator: TagOperatorAND,
				},
			},
			want: true,
		},
		{
			name: "multiple tags AND no match",
			query: SearchQuery{
				Tags: TagQuery{
					Tags:     []string{"security", "python"},
					Operator: TagOperatorAND,
				},
			},
			want: false,
		},
		{
			name: "multiple tags OR match",
			query: SearchQuery{
				Tags: TagQuery{
					Tags:     []string{"python", "golang"},
					Operator: TagOperatorOR,
				},
			},
			want: true,
		},
		{
			name: "negated tag match",
			query: SearchQuery{
				Tags: TagQuery{
					Tags:     []string{"deprecated"},
					Operator: TagOperatorAND,
					Negated:  true,
				},
			},
			want: true,
		},
		{
			name: "negated tag no match",
			query: SearchQuery{
				Tags: TagQuery{
					Tags:     []string{"security"},
					Operator: TagOperatorAND,
					Negated:  true,
				},
			},
			want: false,
		},
		{
			name: "version prefix match",
			query: SearchQuery{
				Tags:    TagQuery{Operator: TagOperatorAND},
				Version: "1.2",
			},
			want: true,
		},
		{
			name: "version no match",
			query: SearchQuery{
				Tags:    TagQuery{Operator: TagOperatorAND},
				Version: "2.0",
			},
			want: false,
		},
		{
			name: "combined match",
			query: SearchQuery{
				Text: "owasp",
				Tags: TagQuery{
					Tags:     []string{"security"},
					Operator: TagOperatorAND,
				},
				Author: "alice",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesQuery(entry, tt.query)
			if got != tt.want {
				t.Errorf("MatchesQuery() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchTags_CaseInsensitive(t *testing.T) {
	entryTags := []string{"Security", "GOLANG", "web"}

	tests := []struct {
		name      string
		queryTags []string
		want      bool
	}{
		{"lowercase query", []string{"security"}, true},
		{"uppercase query", []string{"SECURITY"}, true},
		{"mixed case query", []string{"GoLang"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := TagQuery{
				Tags:     tt.queryTags,
				Operator: TagOperatorAND,
			}
			got := matchTags(entryTags, query)
			if got != tt.want {
				t.Errorf("matchTags() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestMatchTags_UnknownOperatorFailsClosed pins U095-F16: a TagQuery with a
// zero-value (or otherwise unrecognized) Operator must NOT fail open. The
// default arm used to return true unconditionally, so such a query matched
// every entry regardless of its tags — a fail-open default in a filter.
// ParseSearchQuery always sets AND, so this only bites a hand-constructed
// query, but the type itself must not silently mean "match everything".
func TestMatchTags_UnknownOperatorFailsClosed(t *testing.T) {
	entryTags := []string{"security", "golang"}
	query := TagQuery{Tags: []string{"unrelated-tag"}, Operator: TagOperator("")}
	if got := matchTags(entryTags, query); got {
		t.Errorf("matchTags() with an unrecognized operator = true, want false (fail closed, not fail open)")
	}
}
