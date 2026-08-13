package isolation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// This file implements the stark-wheat "project devcontainer as agent-image
// BASE" resolver (locked decisions, task stark-wheat, 2026-07-14): the
// project's .devcontainer/devcontainer.json — when present and not opted out
// — becomes an auto-detected local-build BASE (stage 1) that composed engine
// fragments (enginespec.go, imagebuild.go's composeAgentContainerfile) layer
// onto, exactly like the embedded default base or an explicit
// isolation_base_containerfile.
//
// FEATURES ARE NOT HONORED (D1, pre1): a devcontainer.json declaring
// "features" (ghcr.io/devcontainers/features/*) needs the devcontainer CLI
// (`devcontainer build`) to materialize — pre1 does NOT take that dependency.
// Their presence is WARNED loudly (never silently honored, never a reason to
// hard-fail the whole build): the resolved base still builds from
// image/build, just without whatever those features would have installed.
// This is a deliberate, narrower choice than the plan's original
// fail-loud-on-features recommendation — see the stark-wheat task text.
//
// dockerComposeFile FAILS LOUD without an explicit service pick
// (isolation_devcontainer_service / the devcontainer.json's own "service"
// key): a multi-service compose project does not map to ONE agent container.

// devcontainerFile is the small subset of the devcontainer.json spec this
// resolver understands: enough to map "image" | "build" | "dockerComposeFile"
// to a base stage. Unrecognized/unsupported shapes are surfaced to the caller
// as an error rather than guessed at.
type devcontainerFile struct {
	Image             string                     `json:"image"`
	Build             *devcontainerBuild         `json:"build"`
	DockerComposeFile json.RawMessage            `json:"dockerComposeFile"`
	Service           string                     `json:"service"`
	Features          map[string]json.RawMessage `json:"features"`
}

// devcontainerBuild is devcontainer.json's "build" object.
type devcontainerBuild struct {
	Dockerfile string            `json:"dockerfile"`
	Context    string            `json:"context"`
	Args       map[string]string `json:"args"`
}

// findDevcontainerJSON locates the project's devcontainer.json by the two
// canonical single-config paths the spec defines. The multi-config
// `.devcontainer/<name>/devcontainer.json` layout is deliberately out of
// scope — this resolver picks ONE base, and a project with several named
// configs has no unambiguous "the" devcontainer to auto-adopt (a user with
// that layout should use isolation_base_containerfile / --base-containerfile
// explicitly, or opt out and provide their own base).
func findDevcontainerJSON(appRoot string) string {
	for _, rel := range []string{
		filepath.Join(".devcontainer", "devcontainer.json"),
		".devcontainer.json",
	} {
		p := filepath.Join(appRoot, rel)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// resolveDevBase wraps resolveDevcontainerBase with the opt-out gate every
// caller (containerFor, runEnsureImage, BuildAgentImage, Diagnose) shares:
// NoDevcontainerBase (config isolation_devcontainer_base: false /
// --no-devcontainer-base) or an empty appRoot (a test harness, or a caller
// that never learned the project root) means "no auto-detect", never an
// error — only an EXPLICITLY-detected devcontainer.json that turns out
// unusable raises one.
func resolveDevBase(appRoot string, noDevcontainerBase bool, service string) (*baseStage, error) {
	if noDevcontainerBase || appRoot == "" {
		return nil, nil
	}
	return resolveDevcontainerBase(appRoot, service)
}

// resolveDevcontainerBase auto-detects the project's devcontainer.json (or
// .devcontainer.json) and maps it to a base stage per the locked decisions:
//   - absent devcontainer.json → (nil, nil): no auto-detected base, the
//     caller falls through to the next source (explicit base > default).
//   - "image": <ref> → a synthetic FROM base.
//   - "build": {dockerfile, context, args} → that Dockerfile as the base,
//     with the devcontainer's own context dir + build args threaded through.
//   - "dockerComposeFile" → does not map to ONE agent container; an error
//     unless `service` (or the devcontainer.json's own "service" key) names a
//     service this resolver can locate a plain image/build for.
//   - "features" present → WARNED (see the package doc above); never a
//     reason by itself to error.
//   - malformed JSON, an unreadable file, or an unrecognized shape → an
//     error, never a silent fallback to the default base.
func resolveDevcontainerBase(appRoot, service string) (*baseStage, error) {
	path := findDevcontainerJSON(appRoot)
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var dc devcontainerFile
	if err := json.Unmarshal(stripJSONC(raw), &dc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	warnDevcontainerFeatures(path, dc.Features)

	if dc.DockerComposeFile != nil {
		return resolveComposeBase(path, dc, service)
	}
	contextDir := filepath.Dir(path)
	if dc.Build != nil && dc.Build.Dockerfile != "" {
		dockerfile := filepath.Join(contextDir, filepath.FromSlash(dc.Build.Dockerfile))
		bctx := contextDir
		if dc.Build.Context != "" {
			bctx = filepath.Join(contextDir, filepath.FromSlash(dc.Build.Context))
		}
		return devcontainerBuildStage(
			fmt.Sprintf("project devcontainer %s (build.dockerfile %s)", path, dc.Build.Dockerfile),
			dockerfile, bctx, dc.Build.Args), nil
	}
	if dc.Image != "" {
		return devcontainerImageStage(fmt.Sprintf("project devcontainer %s (image %s)", path, dc.Image), dc.Image), nil
	}
	return nil, fmt.Errorf("%s: no recognized base shape (need \"image\", \"build\", or \"dockerComposeFile\" + a service)", path)
}

// warnDevcontainerFeatures loudly names any declared devcontainer features
// (ghcr.io/devcontainers/features/*) ctxloom does NOT honor — pre1 does not
// shell out to the devcontainer CLI to materialize them (D1). Never fatal:
// the resolved base still builds from image/build, just without whatever
// those features would have installed.
func warnDevcontainerFeatures(path string, features map[string]json.RawMessage) {
	if len(features) == 0 {
		return
	}
	names := make([]string, 0, len(features))
	for k := range features {
		names = append(names, k)
	}
	sort.Strings(names)
	clidiag.Warn("ctxloom", "%s declares devcontainer features (%s) that ctxloom does NOT honor in the agent image (pre1 does not depend on the devcontainer CLI) — the resolved base misses whatever those features would install; install the missing tools in the base yourself (a Containerfile via isolation_base_containerfile, or edit the devcontainer's own Dockerfile), or opt out of the devcontainer base with isolation_devcontainer_base: false",
		path, strings.Join(names, ", "))
}

// resolveComposeBase resolves the ONE service dockerComposeFile names as the
// base, per the locked decision: a multi-service compose project does not map
// to a single agent container, so this FAILS LOUD (a returned error) unless a
// service is explicitly named (isolation_devcontainer_service, or the
// devcontainer.json's own "service" key naming which service the editor
// attaches to).
func resolveComposeBase(devcontainerPath string, dc devcontainerFile, service string) (*baseStage, error) {
	if service == "" {
		service = dc.Service
	}
	if service == "" {
		return nil, fmt.Errorf("%s declares dockerComposeFile (a multi-service compose project does not map to one agent container); configure isolation_devcontainer_service to pick one, or opt out with isolation_devcontainer_base: false", devcontainerPath)
	}
	files, ferr := decodeComposeFileList(dc.DockerComposeFile)
	if len(files) == 0 {
		if ferr == nil {
			ferr = errors.New("it is neither a non-empty string nor an array of strings")
		}
		return nil, fmt.Errorf("%s: could not parse dockerComposeFile: %w", devcontainerPath, ferr)
	}
	contextDir := filepath.Dir(devcontainerPath)
	var lastErr error
	for _, f := range files {
		composePath := filepath.Join(contextDir, filepath.FromSlash(f))
		stage, err := composeServiceStage(composePath, service)
		if err != nil {
			lastErr = err
			continue
		}
		if stage != nil {
			return stage, nil
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("%s: dockerComposeFile %v: %w", devcontainerPath, files, lastErr)
	}
	return nil, fmt.Errorf("%s: service %q not found (or has neither image nor build) in dockerComposeFile %v", devcontainerPath, service, files)
}

// decodeComposeFileList decodes devcontainer.json's dockerComposeFile, which
// the spec allows as either a single string or an array of strings. A value
// that is neither returns the ARRAY decode's error: the caller reports "could
// not parse dockerComposeFile", and without a cause the human is told the file
// is wrong but never what about it is wrong.
func decodeComposeFileList(raw json.RawMessage) ([]string, error) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if single == "" {
			return nil, nil
		}
		return []string{single}, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// composeServiceStage reads one docker-compose file and resolves the named
// service's "image" or "build" into a base stage. Best-effort YAML shape
// (services.<name>.image | services.<name>.build, build itself either a
// bare context string or {dockerfile, context, args}) — a shape this cannot
// recognize errors rather than guessing. Returns (nil, nil) when the compose
// file parses but does not declare the named service, so the caller can try
// another file in a multi-file dockerComposeFile list.
func composeServiceStage(composePath, service string) (*baseStage, error) {
	raw, err := os.ReadFile(composePath)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Services map[string]struct {
			Image string `yaml:"image"`
			Build any    `yaml:"build"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", composePath, err)
	}
	svc, ok := doc.Services[service]
	if !ok {
		return nil, nil
	}
	contextDir := filepath.Dir(composePath)
	desc := fmt.Sprintf("compose service %q (%s)", service, composePath)
	if svc.Image != "" {
		return devcontainerImageStage(desc+fmt.Sprintf(", image %s", svc.Image), svc.Image), nil
	}
	switch b := svc.Build.(type) {
	case string:
		base := filepath.Join(contextDir, filepath.FromSlash(b))
		return devcontainerBuildStage(desc+fmt.Sprintf(", build %s", b), filepath.Join(base, "Dockerfile"), base, nil), nil
	case map[string]any:
		return composeBuildObjectStage(desc, contextDir, b), nil
	default:
		return nil, fmt.Errorf("service %q has neither image nor build", service)
	}
}

// composeBuildObjectStage maps a compose service's OBJECT build form
// ({dockerfile, context, args}) to a base stage, defaulting dockerfile to
// "Dockerfile" and context to the compose file's own directory, and keeping
// only the string-valued build args (compose allows numbers and nulls there,
// which no --build-arg can carry).
func composeBuildObjectStage(desc, contextDir string, b map[string]any) *baseStage {
	dockerfile := "Dockerfile"
	if v, ok := b["dockerfile"].(string); ok && v != "" {
		dockerfile = v
	}
	rel := "."
	if v, ok := b["context"].(string); ok && v != "" {
		rel = v
	}
	var args map[string]string
	if raw, ok := b["args"].(map[string]any); ok {
		args = map[string]string{}
		for k, v := range raw {
			if s, ok := v.(string); ok {
				args[k] = s
			}
		}
	}
	base := filepath.Join(contextDir, filepath.FromSlash(rel))
	return devcontainerBuildStage(desc, filepath.Join(base, filepath.FromSlash(dockerfile)), base, args)
}

// stripJSONC removes devcontainer.json's JSONC extensions (// and /* */
// comments, trailing commas before } or ]) so encoding/json can parse it. A
// minimal, string-aware stripper — devcontainer.json bodies are simple
// key/value documents, not a general JS/JSON5 surface.
func stripJSONC(src []byte) []byte {
	return stripTrailingCommas(stripComments(src))
}

// copyStringLiteral copies the JSON string literal opening at src[i] (which
// must be the opening quote) into out and returns the index of its closing
// quote — or len(src) for an unterminated literal, which is copied verbatim
// so a malformed document reaches the JSON parser to be reported there.
// Backslash escaping is honored, so an escaped quote does not end the literal
// and an escaped backslash does not escape the quote after it.
//
// The single string-aware step both JSONC strippers share: everything either
// of them does is "outside a string", so this is the whole of the string
// state either of them needs.
func copyStringLiteral(src []byte, i int, out []byte) (int, []byte) {
	out = append(out, src[i])
	escaped := false
	for i++; i < len(src); i++ {
		c := src[i]
		out = append(out, c)
		switch {
		case escaped:
			escaped = false
		case c == '\\':
			escaped = true
		case c == '"':
			return i, out
		}
	}
	return i, out
}

// skipLineComment returns the index of the newline ending the // comment
// starting at src[i], or len(src) when it runs to EOF.
func skipLineComment(src []byte, i int) int {
	for i < len(src) && src[i] != '\n' {
		i++
	}
	return i
}

// skipBlockComment returns the index of the '/' closing the /* */ comment
// opening at src[i], or the end of an unterminated one.
func skipBlockComment(src []byte, i int) int {
	i += 2
	for i+1 < len(src) && (src[i] != '*' || src[i+1] != '/') {
		i++
	}
	return i + 1
}

// stripComments removes // line comments and /* */ block comments, leaving
// their content replaced by nothing (a line comment collapses to a newline so
// line numbers stay roughly stable for error messages) — never touching
// bytes inside a JSON string.
func stripComments(src []byte) []byte {
	var out []byte
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '"':
			i, out = copyStringLiteral(src, i, out)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			i = skipLineComment(src, i)
			out = append(out, '\n')
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i = skipBlockComment(src, i) // the loop's i++ steps past the closing '/'
		default:
			out = append(out, c)
		}
	}
	return out
}

// stripTrailingCommas removes a comma immediately before a closing } or ]
// (across intervening whitespace) — the other JSONC extension devcontainer.json
// files commonly use. String-aware for the same reason as stripComments.
func stripTrailingCommas(src []byte) []byte {
	var out []byte
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c == '"' {
			i, out = copyStringLiteral(src, i, out)
			continue
		}
		if c == ',' && closesAfterSpace(src, i+1) {
			continue // drop the trailing comma
		}
		out = append(out, c)
	}
	return out
}

// closesAfterSpace reports whether the next non-whitespace byte at or after i
// closes an object or array — i.e. whether a comma just before it is a
// trailing one.
func closesAfterSpace(src []byte, i int) bool {
	for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r') {
		i++
	}
	return i < len(src) && (src[i] == '}' || src[i] == ']')
}
