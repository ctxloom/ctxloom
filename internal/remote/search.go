package remote

import (
	"regexp"
	"strings"
)

// ParseSearchQuery parses a search query string into structured filters.
// Supports:
//   - Plain text: full-text search on name/description
//   - tag:foo/bar or tag:foo/bar/AND: tags with AND (default)
//   - tag:foo/bar/OR: tags with OR
//   - tag:foo/NOT: negated tag
//   - author:name: filter by author
//   - version:spec: version constraint
func ParseSearchQuery(query string) SearchQuery {
	result := SearchQuery{
		Tags: TagQuery{
			Operator: TagOperatorAND,
		},
	}

	// Regular expressions for structured filters
	tagRegex := regexp.MustCompile(`tag:([^\s]+)`)
	authorRegex := regexp.MustCompile(`author:([^\s]+)`)
	versionRegex := regexp.MustCompile(`version:([^\s]+)`)

	remaining := query

	// Extract tag filters
	tagMatches := tagRegex.FindAllStringSubmatch(query, -1)
	for _, match := range tagMatches {
		if len(match) >= 2 {
			parseTagExpression(&result.Tags, match[1])
		}
		remaining = strings.Replace(remaining, match[0], "", 1)
	}

	// Extract author filter
	authorMatches := authorRegex.FindAllStringSubmatch(query, -1)
	for _, match := range authorMatches {
		if len(match) >= 2 {
			result.Author = match[1]
		}
		remaining = strings.Replace(remaining, match[0], "", 1)
	}

	// Extract version filter
	versionMatches := versionRegex.FindAllStringSubmatch(query, -1)
	for _, match := range versionMatches {
		if len(match) >= 2 {
			result.Version = match[1]
		}
		remaining = strings.Replace(remaining, match[0], "", 1)
	}

	// Remaining text is full-text search
	result.Text = strings.TrimSpace(remaining)

	return result
}

// parseTagExpression parses a postfix tag expression.
// Examples:
//   - foo → single tag
//   - foo/bar → foo AND bar (default)
//   - foo/bar/AND → foo AND bar
//   - foo/bar/OR → foo OR bar
//   - foo/NOT → NOT foo
//   - foo/bar/NOT/OR → (NOT bar) OR foo
func parseTagExpression(q *TagQuery, expr string) {
	parts := strings.Split(expr, "/")
	if len(parts) == 0 {
		return
	}

	// Check for operators at the end
	for len(parts) > 0 {
		last := strings.ToUpper(parts[len(parts)-1])
		switch last {
		case "AND":
			q.Operator = TagOperatorAND
			parts = parts[:len(parts)-1]
		case "OR":
			q.Operator = TagOperatorOR
			parts = parts[:len(parts)-1]
		case "NOT":
			q.Negated = true
			parts = parts[:len(parts)-1]
		default:
			// Not an operator, done parsing
			goto done
		}
	}

done:
	// Remaining parts are tag names
	q.Tags = append(q.Tags, parts...)
}

// MatchesQuery checks if a manifest entry matches the search query.
func MatchesQuery(entry ManifestEntry, query SearchQuery) bool {
	// Check text search (name and description)
	if query.Text != "" {
		textLower := strings.ToLower(query.Text)
		nameLower := strings.ToLower(entry.Name)
		descLower := strings.ToLower(entry.Description)

		if !strings.Contains(nameLower, textLower) && !strings.Contains(descLower, textLower) {
			return false
		}
	}

	// Check author filter
	if query.Author != "" {
		if !strings.EqualFold(entry.Author, query.Author) {
			return false
		}
	}

	// Check version filter (simple prefix match for now)
	if query.Version != "" {
		if !strings.HasPrefix(entry.Version, query.Version) {
			return false
		}
	}

	// Check tag filter
	if len(query.Tags.Tags) > 0 {
		matched := matchTags(entry.Tags, query.Tags)
		if !matched {
			return false
		}
	}

	return true
}

// matchTags checks if entry tags match the tag query.
func matchTags(entryTags []string, query TagQuery) bool {
	// Convert to lowercase for comparison
	entryTagsLower := make(map[string]bool)
	for _, t := range entryTags {
		entryTagsLower[strings.ToLower(t)] = true
	}

	switch query.Operator {
	case TagOperatorAND:
		// All query tags must be present
		for _, t := range query.Tags {
			present := entryTagsLower[strings.ToLower(t)]
			if query.Negated {
				present = !present
			}
			if !present {
				return false
			}
		}
		return true

	case TagOperatorOR:
		// At least one query tag must be present
		for _, t := range query.Tags {
			present := entryTagsLower[strings.ToLower(t)]
			if query.Negated {
				present = !present
			}
			if present {
				return true
			}
		}
		return false

	default:
		return true
	}
}

// SearchResults holds results from searching across remotes.
type SearchResults struct {
	Results []SearchResult
}

// SearchResult represents a single search match.
type SearchResult struct {
	Remote      string        // Remote name
	Entry       ManifestEntry // Matched entry
	RemoteURL   string        // URL of the remote repo
	MatchedTags []string      // Which tags matched (for highlighting)
	ItemType    ItemType      // Type of item (bundle or profile)
}
