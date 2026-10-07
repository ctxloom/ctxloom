package kit

import (
	"maps"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// ComposeEnv is the engine-native env every Exec starts from: each relocated
// home var at its bound path, then every presentation's env channel in
// delivery order (a later value overrides an earlier one, a home var
// included). The engine adds its own switches on top.
func ComposeEnv(s engine.Session, presented []present.Presentation) map[string]string {
	env := map[string]string{}
	for _, h := range s.Home {
		env[h.Var] = h.Path
	}
	for _, p := range presented {
		maps.Copy(env, p.Env)
	}
	return env
}

// PresentedArgs is every presentation's argv channel in delivery order,
// never nil. refuse, when set, vetoes a presentation the engine cannot take;
// its error is returned as is.
func PresentedArgs(presented []present.Presentation, refuse func(present.Presentation) error) ([]string, error) {
	args := []string{}
	for _, p := range presented {
		if refuse != nil {
			if err := refuse(p); err != nil {
				return nil, err
			}
		}
		args = append(args, p.Args...)
	}
	return args, nil
}
