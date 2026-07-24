package mockengine

import "github.com/ctxloom/ctxloom/internal/shared/agent"

// Resolver supplies the ambient facts the discovery walk resolves probe roots
// against — this process's real cwd and home, and its environment. It is an
// interface-free struct so a test can drive the walk over a temp workspace
// without touching the real process environment.
type Resolver struct {
	// Cwd is the working directory ScopeCwd probes resolve against.
	Cwd string
	// Home is the $HOME ScopeHome / ScopeEnvDir-fallback probes resolve against.
	Home string
	// Getenv reads an environment variable (os.Getenv in production).
	Getenv func(string) string
}

// Walk resolves and probes every context surface the engine's CLI declaration
// lists, in declaration order, returning one ProbeRecord per probe — INCLUDING
// present:false rows for absent paths and for flag-value probes whose flag was
// not in argv. It is the single discovery engine every personality shares; it
// asks L1 (the EngineCLI) where things live and never restates per-backend
// knowledge.
//
// STUB: returns nil until implemented (TDD red).
func Walk(cli agent.EngineCLI, argv agent.ParsedArgv, res Resolver) []ProbeRecord {
	return nil
}
