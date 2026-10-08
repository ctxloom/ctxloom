package agent

// ApplyLocalCLIConfig applies the local-CLI args an agent module's typed
// config carries to a backend. Each module keeps its own typed config struct
// and BackendType() (the config registry dispatches on the concrete types);
// only this identical application body is shared. Empty args leave the
// backend's defaults in place.
func ApplyLocalCLIConfig(b *BaseBackend, args []string) {
	if len(args) > 0 {
		b.Args = args
	}
}
