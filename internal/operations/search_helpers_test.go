package operations

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
)

// searchProfiles matches profiles by name, then description, then tag (in that
// precedence), recording which field matched.
func TestSearchProfiles(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"go-dev":   {Description: "Golang work", Tags: []string{"backend"}},
			"frontend": {Description: "UI things", Tags: []string{"react", "go-adjacent"}},
			"infra":    {Description: "ops", Tags: []string{"k8s"}},
		}},
	})

	t.Run("matches by name", func(t *testing.T) {
		got := searchProfiles(cfg, "go-dev")
		if len(got) != 1 || got[0].Name != "go-dev" || got[0].Match != "name" {
			t.Fatalf("got %+v, want one name match for go-dev", got)
		}
	})

	t.Run("matches by description when name misses", func(t *testing.T) {
		got := searchProfiles(cfg, "golang")
		if len(got) != 1 || got[0].Name != "go-dev" || got[0].Match != "description" {
			t.Fatalf("got %+v, want description match for go-dev", got)
		}
	})

	t.Run("matches by tag when name and description miss", func(t *testing.T) {
		got := searchProfiles(cfg, "k8s")
		if len(got) != 1 || got[0].Name != "infra" || got[0].Match != "tag" {
			t.Fatalf("got %+v, want tag match for infra", got)
		}
	})

	t.Run("every result is typed profile and carries tags", func(t *testing.T) {
		got := searchProfiles(cfg, "go")
		if len(got) == 0 {
			t.Fatal("expected matches for 'go'")
		}
		for _, r := range got {
			if r.Type != "profile" {
				t.Errorf("result %q has type %q, want profile", r.Name, r.Type)
			}
		}
	})

	t.Run("no match yields empty", func(t *testing.T) {
		if got := searchProfiles(cfg, "nonexistent"); len(got) != 0 {
			t.Fatalf("got %+v, want none", got)
		}
	})
}

// searchMCPServers matches REGISTERED servers by name or command substring —
// the resolved bundle set, which every project gets ctxloom's own server in
// through the builtin ctxloom bundle.
func TestSearchMCPServers(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{})

	t.Run("matches ctxloom's own server by name", func(t *testing.T) {
		got := searchMCPServers(cfg, "ctxloom")
		if len(got) != 1 || got[0].Name != "ctxloom" || got[0].Type != "mcp_server" {
			t.Fatalf("got %+v, want mcp_server ctxloom", got)
		}
		if got[0].Source == "" {
			t.Error("source must carry the server's command")
		}
	})

	t.Run("no match yields empty", func(t *testing.T) {
		if got := searchMCPServers(cfg, "zzz"); len(got) != 0 {
			t.Fatalf("got %+v, want none", got)
		}
	})
}

// sortResults orders in place by the chosen key, honoring the reverse flag.
func TestSortResults(t *testing.T) {
	t.Run("by name ascending", func(t *testing.T) {
		r := []SearchResult{{Name: "charlie"}, {Name: "alice"}, {Name: "bob"}}
		sortResults(r, "name", false)
		if r[0].Name != "alice" || r[1].Name != "bob" || r[2].Name != "charlie" {
			t.Fatalf("got %v", names(r))
		}
	})

	t.Run("by name descending", func(t *testing.T) {
		r := []SearchResult{{Name: "alice"}, {Name: "charlie"}, {Name: "bob"}}
		sortResults(r, "name", true)
		if r[0].Name != "charlie" || r[2].Name != "alice" {
			t.Fatalf("got %v", names(r))
		}
	})

	t.Run("by type ascending", func(t *testing.T) {
		r := []SearchResult{{Type: "fragment"}, {Type: "mcp_server"}, {Type: "command"}}
		sortResults(r, "type", false)
		if r[0].Type != "command" || r[1].Type != "fragment" || r[2].Type != "mcp_server" {
			t.Fatalf("got %v", types(r))
		}
	})

	t.Run("by relevance puts name matches before tag matches", func(t *testing.T) {
		r := []SearchResult{
			{Name: "a", Match: "tag"},
			{Name: "b", Match: "name"},
			{Name: "c", Match: "description"},
		}
		sortResults(r, "relevance", false)
		if r[0].Match != "name" {
			t.Fatalf("name match should sort first; got %v", matches(r))
		}
	})

	t.Run("relevance reversed puts weakest first", func(t *testing.T) {
		r := []SearchResult{
			{Name: "b", Match: "name"},
			{Name: "a", Match: "tag"},
		}
		sortResults(r, "relevance", true)
		if r[0].Match == "name" {
			t.Fatalf("reverse relevance should not lead with name; got %v", matches(r))
		}
	})
}

func names(r []SearchResult) []string {
	out := make([]string, len(r))
	for i := range r {
		out[i] = r[i].Name
	}
	return out
}

func types(r []SearchResult) []string {
	out := make([]string, len(r))
	for i := range r {
		out[i] = r[i].Type
	}
	return out
}

func matches(r []SearchResult) []string {
	out := make([]string, len(r))
	for i := range r {
		out[i] = r[i].Match
	}
	return out
}
