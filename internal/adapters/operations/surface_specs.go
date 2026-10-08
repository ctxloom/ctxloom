package operations

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/present"
)

// RawSurfaceSpec is one `--surface KIND[=MECHANISM][:DEST]` as typed,
// trimmed and unvalidated: whether the kind or mechanism exists is the
// consumer's question (agent set asks the bound engine; materialize asks the
// vocabulary).
type RawSurfaceSpec struct {
	Kind, Mechanism, Dest string
}

// SplitSurfaceSpecs is the ONE --surface splitter, shared by `agent set` and
// `ctxloom materialize`: KIND, then after `=` the MECHANISM, then after the
// first `:` that follows `=` the DEST (mechanism names hold no `:`, so a
// Windows `C:\…` destination survives). The one fault only the command line
// can see is refused here: one kind named two different ways (a map would
// keep the last, delivering something the command line does not say).
func SplitSurfaceSpecs(flags []string) ([]RawSurfaceSpec, error) {
	var out []RawSurfaceSpec
	seen := map[string]RawSurfaceSpec{}
	for _, f := range flags {
		kind, rest, _ := strings.Cut(f, "=")
		mech, dest, _ := strings.Cut(rest, ":")
		s := RawSurfaceSpec{Kind: strings.TrimSpace(kind), Mechanism: strings.TrimSpace(mech), Dest: strings.TrimSpace(dest)}
		if prev, dup := seen[s.Kind]; dup {
			if prev != s {
				return nil, fmt.Errorf("--surface names %s twice, as %s and %s; a surface is delivered one way", s.Kind, prev.spelled(), s.spelled())
			}
			continue
		}
		seen[s.Kind] = s
		out = append(out, s)
	}
	return out, nil
}

// spelled is the spec's value as the user wrote it after the kind.
func (s RawSurfaceSpec) spelled() string {
	if s.Dest == "" {
		return s.Mechanism
	}
	return s.Mechanism + ":" + s.Dest
}

// ParseSurfaceSpecs is SplitSurfaceSpecs with each kind read from the
// surface vocabulary: an unknown kind is refused by name. Mechanism and
// destination are checked by Materialize, against the at-rest delivery.
func ParseSurfaceSpecs(flags []string) ([]SurfaceSpec, error) {
	raw, err := SplitSurfaceSpecs(flags)
	if err != nil {
		return nil, err
	}
	out := make([]SurfaceSpec, 0, len(raw))
	for _, r := range raw {
		k, ok := present.ParseKind(r.Kind)
		if !ok {
			return nil, fmt.Errorf("%w: --surface %q: %q is not a surface kind (context, mcp, settings, hooks, commands, skills)", ErrMaterializeRequest, r.Kind, r.Kind)
		}
		out = append(out, SurfaceSpec{Kind: k, Mechanism: r.Mechanism, Dest: r.Dest})
	}
	return out, nil
}
