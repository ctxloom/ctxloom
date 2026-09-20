package sessions

import (
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// Layout is the harp-keyed tree under the ctxloom home. The project tree
// holds NO session state.
type Layout struct{ Root string }

// HomeLayout is the Layout over the resolved ctxloom home.
func HomeLayout() (Layout, error) { return Layout{}, nil }

func (l Layout) SessionsRoot() string                          { return "" }
func (l Layout) Dir(harp string) string                        { return "" }
func (l Layout) Member(harp string, m paths.HarpMember) string { return "" }
func (l Layout) SessionHome(harp string) string                { return "" }
func (l Layout) Persist(harp string) string                    { return "" }
func (l Layout) Ephemeral(harp string) string                  { return "" }
func (l Layout) Segments(harp string) string                   { return "" }
func (l Layout) Spool(harp string) string                      { return "" }
func (l Layout) Sidecar(harp string) string                    { return "" }
