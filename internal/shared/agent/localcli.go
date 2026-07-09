package agent

// ApplyLocalCLIConfig applies the local-CLI overrides every agent module's
// typed config carries — binary path, args, env — to a backend. Each module
// keeps its own typed config struct and BackendType() (the config registry
// dispatches on the concrete types); only this identical application body is
// shared. Empty values leave the backend's defaults in place; env entries
// merge into (never replace) the backend's env map.
func ApplyLocalCLIConfig(b *BaseBackend, binaryPath string, args []string, env map[string]string) {
	if binaryPath != "" {
		b.BinaryPath = binaryPath
	}
	if len(args) > 0 {
		b.Args = args
	}
	for k, v := range env {
		b.Env[k] = v
	}
}
