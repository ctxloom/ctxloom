package engine

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	ltkengine "github.com/ctxloom/ctxloom/internal/ltk/engine"
)

// engineNameCorpus is the spellings every registry in parityRegistries must
// agree about: the one each has to accept, and the shapes every one has to
// refuse. An engine has ONE name; the retired short spellings and case
// variants are refused exactly as a typo or a removed engine is.
var engineNameCorpus = []struct {
	in     string
	accept bool
}{
	{claude.EngineName, true},
	{"CLAUDE-CODE", false},
	{"Claude-Code", false},
	{"claudecode", false},
	{"claude", false},
	{"CLAUDE", false},
	{"antigravity", false}, // removed engine: no longer resolves anywhere
	{"agy", false},         // its former alias, also removed
	{"antigravity-cli", false},
	{"", false},
	{"claude-", false},
	{"clau", false},
	{"antigrav", false},
	{"nonsense", false},
}

// engineRegistry is one name -> engine registry the shared vocabulary has to
// reach. resolve returns the name the registry landed on, or an error when it
// refuses the spelling; exists is the registry's cheap membership predicate
// where it has one, and must never disagree with resolve (a caller that gates
// on the predicate and then dereferences the lookup is the shape that turns a
// refusal into a nil).
type engineRegistry struct {
	pkg     string
	resolve func(in string) (string, error)
	exists  func(in string) bool
}

// parityRegistries is every registry that turns a user-typed engine name into
// an engine: taskloom's and ltk's lean registries, and ctxloom's backend
// registry.
func parityRegistries() []engineRegistry {
	return []engineRegistry{
		{
			pkg: "github.com/ctxloom/ctxloom/internal/taskloom/engine",
			resolve: func(in string) (string, error) {
				e, err := Get(in)
				if err != nil {
					return "", err
				}
				return e.Name(), nil
			},
		},
		{
			pkg: "github.com/ctxloom/ctxloom/internal/ltk/engine",
			resolve: func(in string) (string, error) {
				e, err := ltkengine.Get(in)
				if err != nil {
					return "", err
				}
				return e.Name(), nil
			},
		},
		{
			pkg: "github.com/ctxloom/ctxloom/internal/engines",
			resolve: func(in string) (string, error) {
				h, ok := engines.Hosted(in)
				if !ok {
					return "", fmt.Errorf("unknown engine %q", in)
				}
				return h.Backend(nil).Name(), nil
			},
			exists: func(name string) bool { _, ok := engines.Registry().Lookup(engine.Name(name)); return ok },
		},
	}
}

// TestEngineNameVocabularyParity pins every engine registry to ONE spelling
// vocabulary. The engine names are shared vocabulary — a user types the same
// name as ltk's --engine, as taskloom's --engine and as ctxloom's --type — and
// each registry deciding independently is how a spelling resolves under one
// binary and errors under another. With no alias table, the only thing to
// agree on is exact-match on the registered name; this holds that no registry
// has grown a private fold or alias.
func TestEngineNameVocabularyParity(t *testing.T) {
	registries := parityRegistries()
	require.NotEmpty(t, registries)

	for _, tc := range engineNameCorpus {
		for _, r := range registries {
			got, err := r.resolve(tc.in)
			if !tc.accept {
				assert.Error(t, err, "%s must reject %q", r.pkg, tc.in)
			} else {
				require.NoError(t, err, "%s must resolve %q", r.pkg, tc.in)
				assert.Equal(t, tc.in, got, "%s resolved %q", r.pkg, tc.in)
			}
			if r.exists == nil {
				continue
			}
			assert.Equal(t, err == nil, r.exists(tc.in),
				"%s: membership predicate disagrees with the lookup for %q", r.pkg, tc.in)
		}
	}
}
