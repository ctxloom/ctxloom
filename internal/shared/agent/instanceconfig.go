package agent

// InstanceConfigWriter is the INSTANCE-CONFIG facet of an engine: it GENERATES
// the engine's own top-level config file inside a config home ctxloom
// provisioned — a per-session in-tree instance or a per-agent worktree home —
// from the engine's OWN knowledge of its OWN file format.
//
// It is a SIBLING of SettingsWriter and ContextWriter, not an extension, for
// the same reason those two are siblings of each other: a backend with no
// generated instance config (kiro) simply does not contribute one, and a
// consumer that only writes settings does not grow an unused method.
//
// WHY IT EXISTS AT ALL — the engine write-config directive (ruled 2026-08-11):
// config-file manipulation is an ENGINE capability. The ambient copy-in
// (isolation.CopyAmbient) decides WHICH files and classes cross from the
// user's real host home into an instance; it must never perform byte-level
// surgery on a format it does not own. claude's field-scoped `.claude.json`
// allow-list and codex's `[mcp_servers]`/`[hooks]` section elision are both
// edits of a vendor's own schema, so both live in that vendor's package behind
// this one method. Generic cross-engine file surgery in isolation/operations
// is exactly what this interface exists to prevent.
//
// ONE WAY, ALWAYS. An implementation may READ req.HostHome and must never
// write to it: the user's real ~/.claude, ~/.codex and ~/.kiro are the durable
// truth of their engine configuration and ctxloom never writes them.
// tests/arch's real-home byte-identity gate is what holds that honest.
type InstanceConfigWriter interface {
	// WriteInstanceConfig generates this engine's instance config under
	// req.InstanceHome, reading (never writing) req.HostHome for the ambient
	// half. It returns the paths it wrote and any fail-loud warnings the
	// caller should surface — a host file missing a key the engine expected is
	// schema drift, and drift that nobody says out loud becomes a silent
	// behaviour change on the next vendor release.
	WriteInstanceConfig(req InstanceConfigRequest) (InstanceConfigReport, error)
}

// InstanceConfigRequest carries the three facts an instance-config generation
// needs. It is a struct (not three bare strings) so the request can grow
// without churning every engine's signature.
type InstanceConfigRequest struct {
	// HostHome is the user's REAL host home directory — the ambient SOURCE,
	// resolved once by the caller so no engine package looks it up itself.
	// READ-ONLY: see the interface doc. Empty means the host home could not be
	// resolved, in which case an implementation contributes its generated and
	// hardened halves and skips the ambient one.
	HostHome string
	// InstanceHome is the config-home ROOT ctxloom provisioned for this run.
	// Each engine appends its OWN leaf (".codex" / "claude" / "kiro") — the
	// same composition both axes already perform — so one root hosts every
	// engine without collision.
	InstanceHome string
	// WorkDir is the absolute project directory this run works in. It is the
	// key for engine-GENERATED per-project state, most importantly the
	// workspace-trust answer an instance would otherwise re-prompt for (or,
	// headless, silently proceed without). Empty means the caller could not
	// name one, and the per-project half is skipped.
	WorkDir string
}

// CredentialProjector is the AMBIENT-CREDENTIAL facet of an engine: it PROJECTS
// the bytes of one host credential file as they are copied into an instance
// home, so an engine that must not hand its full on-disk credential to a
// DISPOSABLE instance can strip the fields that would be dangerous there.
//
// It is a SIBLING of InstanceConfigWriter, for the same reason that one is a
// sibling of SettingsWriter/ContextWriter: an engine whose whole credential
// file is safe to copy verbatim (codex's auth.json, opencode's auth.json)
// simply registers no projector and the copy is byte-for-byte.
//
// WHY IT EXISTS — the same engine write-config directive InstanceConfigWriter
// serves: byte-level surgery on a vendor's own file format is an ENGINE
// capability and must live in that vendor's package, never in generic
// isolation/operations code. isolation.CopyAmbient decides WHICH credential
// files cross; it must never parse or edit a format it does not own. claude's
// case is the reason this exists: its `.credentials.json` carries a SINGLE-USE
// rotating refresh token, and any copy that refreshes invalidates the host
// login (proven live 2026-08-12). So claude's projector strips the refresh
// token, handing a disposable instance an access-token-only credential that
// can authenticate but can never rotate the host's.
//
// ONE WAY, ALWAYS. An implementation receives a COPY of the host bytes and its
// result is written ONLY into the instance; the user's real credential file is
// read (by the caller) and never written. tests/arch's real-home byte-identity
// gate holds that honest.
type CredentialProjector interface {
	// ProjectAmbientCredential transforms the raw bytes of ONE host credential
	// file being copied into an instance home. destName identifies which file
	// by the engine's own leaf (e.g. ".credentials.json"), so a projector that
	// only cares about one of several ambient files can pass the rest through.
	// It returns the exact bytes to WRITE into the instance copy. An error
	// FAILS the copy loud rather than writing an unprojected credential: for a
	// security-motivated projection (claude's refresh-token strip) a silent
	// passthrough of a credential the projector could not sanitize would defeat
	// the whole control.
	ProjectAmbientCredential(destName string, hostBytes []byte) ([]byte, error)
}

// InstanceConfigReport is what one WriteInstanceConfig call did.
type InstanceConfigReport struct {
	// Wrote lists the absolute paths written, so a caller can report real
	// bytes rather than an unfalsifiable "prepared" (this project's
	// characteristic false green is exit 0 with zero bytes written).
	Wrote []string
	// Warnings carries fail-loud, human-readable notices — schema drift in the
	// host file, a precedence file shadowing what we just wrote. Returned
	// rather than printed so a test can assert on them; the caller prints.
	Warnings []string
}
