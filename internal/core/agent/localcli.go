package agent

// ApplyLocalCLIConfig applies the local-CLI overrides an agent module's typed
// config carries — binary path and args — to a backend. Each module keeps its
// own typed config struct and BackendType() (the config registry dispatches
// on the concrete types); only this identical application body is shared.
// Empty values leave the backend's defaults in place.
func ApplyLocalCLIConfig(b *BaseBackend, binaryPath string, args []string) {
	if binaryPath != "" {
		b.BinaryPath = binaryPath
	}
	if len(args) > 0 {
		b.Args = args
	}
}
