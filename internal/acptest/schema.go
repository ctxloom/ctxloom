// Package acptest is the ACP conformance harness (H1 slice): it vendors the
// CURRENT Agent Client Protocol JSON Schema — deliberately newer than the
// pinned SDK (github.com/joshgarnett/agent-client-protocol-go, pinned
// 2025-09-02, see go.mod) — and validates individual wire payloads against
// it. This is TEST/HARNESS INFRASTRUCTURE: it is never imported by cmd/ctxloom
// or any production request path; it exists to measure the gap between what
// ctxloom emits and what the current spec defines, so the next slice (SDK1,
// which re-vendors the SDK) has an honest, evidence-based acceptance
// checklist rather than a guess.
//
// Provenance of acp-schema-v1.json:
//
//	Source:  https://github.com/agentclientprotocol/agent-client-protocol
//	Path:    schema/v1/schema.json (the STABLE v1 schema — matches the
//	         protocolVersion=1 ctxloom negotiates; schema/v2 is an unreleased,
//	         breaking protocol redesign — session/new -> session/resume,
//	         auth/login, session/close/delete/list — and is NOT what ctxloom
//	         speaks, so validating against it would misreport every v1 frame
//	         as broken).
//	Commit:  a34b896504dd86136f80aab0e69de7a77bacc181 (2026-07-06)
//	Version: schema-v1.19.0 (schema/v1/CHANGELOG.md), i.e. 10+ months of
//	         schema evolution AHEAD of the pinned SDK's 2025-09-02 snapshot.
//	Vendored: 2026-07-16.
//
// Re-vendor with:
//
//	curl -sL -o internal/acptest/acp-schema-v1.json \
//	  https://raw.githubusercontent.com/agentclientprotocol/agent-client-protocol/main/schema/v1/schema.json
package acptest

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed acp-schema-v1.json
var schemaJSON []byte

// U015-F02: SchemaSourceURL/SchemaCommit/SchemaVersion/SchemaVendoredAt used
// to be exported here too, documenting the vendored schema's provenance --
// but the same provenance is already stated, in more detail, in the package
// doc comment above (source URL, commit, version, vendor date), and these
// four constants had zero readers anywhere in the repo, including this
// package's own tests. Deleted rather than left as an unenforced,
// independently-driftable second copy of the same facts (nothing checked
// that a re-vendor updated both the file's provenance comment AND these
// constants together — see the package doc's re-vendor recipe, which only
// updates the JSON file).
//
// schemaResourceName is the compiler resource id acp-schema-v1.json is
// registered under; $defs entries are addressed as
// schemaResourceName + "#/$defs/<Name>".
const schemaResourceName = "acp-schema-v1.json"

// Validator validates individual ACP wire payloads (a request's params, a
// response's result, a notification's params) against the current spec's
// named $defs schema — NOT the whole JSON-RPC envelope, so a failure names
// the exact ACP type that diverged (e.g. "NewSessionResponse") rather than a
// generic "no branch of this oneOf matched" from the top-level union.
type Validator struct {
	// mu guards BOTH the cache map and the compiler. The compiler needs it
	// as much as the map does: jsonschema.Compiler mutates its own resource
	// table inside Compile (findResource) and documents no concurrency
	// guarantee, so two goroutines compiling different $defs of the same
	// resource race each other inside the library.
	mu       sync.Mutex
	compiler *jsonschema.Compiler
	cache    map[string]*jsonschema.Schema
}

// NewValidator compiles the vendored schema. Safe for concurrent use after
// construction (Validate compiles each $defs entry once and caches it).
func NewValidator() (*Validator, error) {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaResourceName, bytes.NewReader(schemaJSON)); err != nil {
		return nil, fmt.Errorf("acptest: add schema resource: %w", err)
	}
	return &Validator{compiler: compiler, cache: make(map[string]*jsonschema.Schema)}, nil
}

// def compiles (or returns the cached compilation of) the named $defs entry.
//
// The lock spans the compile, not just the map access, because the compiler
// itself is shared mutable state (see Validator.mu). Compilation is held once
// per $defs entry for the process's lifetime, so the serialisation costs at
// most one compile per name — a conformance run validating many frames
// concurrently pays it on the first frame of each type and never again.
func (v *Validator) def(name string) (*jsonschema.Schema, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if s, ok := v.cache[name]; ok {
		return s, nil
	}
	s, err := v.compiler.Compile(schemaResourceName + "#/$defs/" + name)
	if err != nil {
		return nil, fmt.Errorf("acptest: compile $defs/%s: %w", name, err)
	}
	v.cache[name] = s
	return s, nil
}

// ValidateDef validates raw (a JSON payload — a request's params, a
// response's result, or a notification's params) against the named $defs
// entry of the current ACP schema (e.g. "NewSessionResponse",
// "SessionNotification", "RequestPermissionRequest"). A nil/empty raw is
// validated as JSON null (the same thing a real ACP peer would receive for an
// omitted params/result field), so an accidentally-empty frame is measured
// honestly rather than skipped.
func (v *Validator) ValidateDef(defName string, raw json.RawMessage) error {
	schema, err := v.def(defName)
	if err != nil {
		return err
	}
	var payload interface{}
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("acptest: payload is not valid JSON: %w", err)
	}
	return schema.Validate(payload)
}
