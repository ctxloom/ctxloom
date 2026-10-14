package isolation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// This file implements the agent-image BASE resolver (resolveBase): the
// isolation_base choice mapped to the ONE local-build BASE (stage 1) that
// composed engine fragments (enginespec.go, imagebuild.go's
// composeAgentContainerfile) layer onto — ctxloom's embedded default, a
// configured image ref, or the project's .devcontainer/devcontainer.json.
//
// ONLY THE BASE IS HONORED: ctxloom reads image | build.{dockerfile,context,
// args} | dockerComposeFile + service, and nothing else. Every other
// top-level field present in the file (features, mounts, containerEnv,
// remoteUser, the lifecycle commands, hostRequirements, build.target, a field
// a later spec adds ...) is WARNED once per build, by name, with what the agent
// container does instead (warnDevcontainerUnhonoured) — never silently
// dropped, never a reason to hard-fail the build. "features" is one of them:
// materializing features needs the devcontainer CLI (`devcontainer build`),
// which pre1 does NOT depend on (D1; a deliberate, narrower choice than the
// plan's original fail-loud-on-features recommendation — see the stark-wheat
// task text). ${...} variables in the honored fields are not substituted, and
// that is named in the same warning.
//
// dockerComposeFile FAILS LOUD without an explicit service pick
// (isolation_devcontainer_service / the devcontainer.json's own "service"
// key): a multi-service compose project does not map to ONE agent container.

// devcontainerFile is the small subset of the devcontainer.json spec this
// resolver understands: enough to map "image" | "build" | "dockerComposeFile"
// to a base stage. Unrecognized/unsupported shapes are surfaced to the caller
// as an error rather than guessed at.
type devcontainerFile struct {
	Image             string             `json:"image"`
	Build             *devcontainerBuild `json:"build"`
	DockerComposeFile json.RawMessage    `json:"dockerComposeFile"`
	Service           string             `json:"service"`
}

// devcontainerBuild is devcontainer.json's "build" object.
type devcontainerBuild struct {
	Dockerfile string            `json:"dockerfile"`
	Context    string            `json:"context"`
	Args       map[string]string `json:"args"`
}

// FindDevcontainerJSON locates the project's devcontainer.json by the two
// canonical single-config paths the spec defines. The multi-config
// `.devcontainer/<name>/devcontainer.json` layout is deliberately out of
// scope — this resolver picks ONE base, and a project with several named
// configs has no unambiguous "the" devcontainer to auto-adopt (a user with
// that layout builds the one they want and names it: isolation_base: <image
// ref>).
func FindDevcontainerJSON(appRoot string) string {
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

// ErrNoDevcontainer is isolation_base: devcontainer in a project that has no
// devcontainer.json at either canonical path.
var ErrNoDevcontainer = errors.New("isolation_base is devcontainer but the project has no .devcontainer/devcontainer.json or .devcontainer.json")

// resolveBase maps the isolation_base choice to the ONE base stage a local
// build layers the agent stage onto — shared by every caller (containerFor,
// runEnsureImage, BuildAgentImage, Diagnose) so the tag a run looks up and
// the image a build produces can never disagree:
//   - config.IsolationBaseCtxloom → the embedded default, devcontainer or not.
//   - config.IsolationBaseDevcontainer → the project devcontainer;
//     ErrNoDevcontainer when there is none (or no appRoot to look in).
//   - "" → the project devcontainer when one is detected under appRoot, else
//     the embedded default.
//   - anything else → that image ref as a declared base.
//
// The stage is NEVER nil: on an error it is the embedded default, so a caller
// that downgrades the error to a finding (runEnsureImage) or an advisory line
// (Diagnose) still has an identity to key on. Every caller surfaces the error.
func resolveBase(choice, appRoot, service string) (*baseStage, error) {
	switch choice {
	case config.IsolationBaseCtxloom:
		return defaultBaseStage(), nil
	case config.IsolationBaseDevcontainer, "":
		return projectDevcontainerBase(appRoot, service, choice == config.IsolationBaseDevcontainer)
	default:
		return imageRefBaseStage(choice), nil
	}
}

// projectDevcontainerBase is the project devcontainer's base stage, or — when
// there is none — ctxloom's own, which is an ErrNoDevcontainer when the
// devcontainer was required. Any error comes with the embedded default (see
// resolveBase).
func projectDevcontainerBase(appRoot, service string, required bool) (*baseStage, error) {
	var dev *baseStage
	var err error
	if appRoot != "" {
		dev, err = resolveDevcontainerBase(appRoot, service)
	}
	if err == nil && dev == nil && required {
		err = ErrNoDevcontainer
	}
	if err != nil || dev == nil {
		return defaultBaseStage(), err
	}
	return dev, nil
}

// resolveDevcontainerBase auto-detects the project's devcontainer.json (or
// .devcontainer.json) and maps it to a base stage per the locked decisions:
//   - absent devcontainer.json → (nil, nil): resolveBase decides what that
//     means for the configured choice.
//   - "image": <ref> → a synthetic FROM base.
//   - "build": {dockerfile, context, args} → that Dockerfile as the base,
//     with the devcontainer's own context dir + build args threaded through.
//   - "dockerComposeFile" → does not map to ONE agent container; an error
//     unless `service` (or the devcontainer.json's own "service" key) names a
//     service this resolver can locate a plain image/build for.
//   - any present field outside the honored set ("features" among them) →
//     WARNED once (warnDevcontainerUnhonoured); never a reason by itself to
//     error.
//   - malformed JSON, an unreadable file, or an unrecognized shape → an
//     error, never a silent fallback to the default base.
func resolveDevcontainerBase(appRoot, service string) (*baseStage, error) {
	path := FindDevcontainerJSON(appRoot)
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var dc devcontainerFile
	var top map[string]json.RawMessage
	body := stripJSONC(raw)
	if err := json.Unmarshal(body, &dc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	warnDevcontainerUnhonoured(path, top)

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

// devcontainerComposeServiceError is resolveComposeBase's refusal of a
// dockerComposeFile with no service named; its one verb is the
// devcontainer.json path. Held to the same accepted-keys rule as
// devcontainerUnhonouredRemedy.
const devcontainerComposeServiceError = "%s declares dockerComposeFile (a multi-service compose project does not map to one agent container); configure isolation_devcontainer_service to pick one, or opt out of the devcontainer base with isolation_base: ctxloom"

// devcontainerHonoured is the explicit set of top-level devcontainer.json
// keys resolveDevcontainerBase acts on; devcontainerBuildHonoured is the same
// for the "build" object. devcontainerInert are keys with no container
// behaviour at all (document metadata). Every OTHER present key is reported
// by unhonouredDevcontainerFields — the report is derived from the file's
// keys against these sets, so a field a later spec adds is reported with no
// code change. There is deliberately no list of "unsupported" fields.
var (
	devcontainerHonoured      = map[string]bool{"image": true, "build": true, "dockerComposeFile": true, "service": true}
	devcontainerBuildHonoured = map[string]bool{"dockerfile": true, "context": true, "args": true}
	devcontainerInert         = map[string]bool{"name": true, "$schema": true}
)

// devcontainerFieldReasons words the report for fields whose substitute is
// worth naming: why the field has no effect and what the agent container does
// instead. It decides NOTHING about what is reported (that is the honoured
// set above); a field absent here is reported with devcontainerDefaultReason.
// Config keys named here are held to the accepted-keys rule
// (TestDevcontainerAdvice_NamesOnlyAcceptedConfigKeys).
var devcontainerFieldReasons = map[string]string{
	"features":             "needs the devcontainer CLI; the base builds without whatever they install",
	"mounts":               "not mounted; the agent container gets ctxloom's own workspace and state mounts",
	"workspaceMount":       "not mounted; the agent container gets ctxloom's own workspace mount",
	"workspaceFolder":      "the agent works in ctxloom's own workspace path",
	"containerEnv":         "not set; the agent gets ctxloom's own sandboxed environment",
	"remoteEnv":            "not set; the agent gets ctxloom's own sandboxed environment",
	"remoteUser":           "the agent runs as ctxloom's mapped user (your host uid and gid)",
	"containerUser":        "the agent runs as ctxloom's mapped user (your host uid and gid)",
	"updateRemoteUserUID":  "ctxloom maps the agent to your host uid and gid itself",
	"initializeCommand":    "not run (it would run on the host)",
	"onCreateCommand":      "not run; bake that setup into the devcontainer's Dockerfile",
	"updateContentCommand": "not run; bake that setup into the devcontainer's Dockerfile",
	"postCreateCommand":    "not run; bake that setup into the devcontainer's Dockerfile",
	"postStartCommand":     "not run; bake that setup into the devcontainer's Dockerfile",
	"postAttachCommand":    "not run; bake that setup into the devcontainer's Dockerfile",
	"runArgs":              "not passed; ctxloom composes the agent container's run flags itself",
	"hostRequirements":     "not checked against the host; ctxloom caps the agent container itself with isolation_memory_limit and isolation_pids_limit",
	"forwardPorts":         "no ports are published from the agent container",
	"appPort":              "no ports are published from the agent container",
	"overrideCommand":      "ctxloom sets the agent container's command",
	"shutdownAction":       "ctxloom manages the agent container's lifetime",
	"runServices":          "only the one named service is used, as the base",
	"customizations":       "editor-specific; no editor runs in the agent container",
	"privileged":           "ctxloom applies its own container security settings",
	"capAdd":               "ctxloom applies its own container security settings",
	"securityOpt":          "ctxloom applies its own container security settings",
	"init":                 "ctxloom supplies the agent container's own entrypoint",
	"dockerFile":           "the legacy top-level build form is not read; use build.dockerfile",
	"context":              "the legacy top-level build form is not read; use build.context",
	"build.target":         "the base builds the Dockerfile's final stage",
}

// devcontainerDefaultReason words a reported field with no specific reason.
const devcontainerDefaultReason = "not read by ctxloom"

// devcontainerUnhonouredNotice is warnDevcontainerUnhonoured's field part;
// its verb is the "field (detail): reason; ..." list. The message leads with
// the devcontainer.json path.
const devcontainerUnhonouredNotice = "ctxloom builds the agent container from the devcontainer's image/build only and does NOT honor these fields: %s"

// devcontainerVarsNotice is the variable part, present when an honoured field
// carries a ${...} variable; its verb is the comma-joined field paths.
const devcontainerVarsNotice = "${...} variables are not substituted in %s, so they reach the build literally"

// devcontainerUnhonouredRemedy is the remedy line of that warning. Its config
// keys are held to the accepted-keys rule.
const devcontainerUnhonouredRemedy = "put what the agent needs into the devcontainer's own Dockerfile, or opt out of the devcontainer base with isolation_base: ctxloom (or isolation_base: <image ref>)"

// unhonouredField is one present-but-unhonoured devcontainer.json field:
// its name, an optional detail (the feature ids), and the reason.
type unhonouredField struct{ name, detail, reason string }

// String renders the field as the warning names it: "name (detail): reason".
func (f unhonouredField) String() string {
	name := f.name
	if f.detail != "" {
		name += " (" + f.detail + ")"
	}
	return name + ": " + f.reason
}

// unhonouredDevcontainerFields derives the present-but-unhonoured fields from
// the parsed file's keys (see devcontainerHonoured), sorted by name. Nested
// "build" keys outside devcontainerBuildHonoured report as build.<key>;
// "features" carries its feature names.
func unhonouredDevcontainerFields(top map[string]json.RawMessage) []unhonouredField {
	var out []unhonouredField
	add := func(name, detail string) {
		reason, ok := devcontainerFieldReasons[name]
		if !ok {
			reason = devcontainerDefaultReason
		}
		out = append(out, unhonouredField{name: name, detail: detail, reason: reason})
	}
	for key, raw := range top {
		switch {
		case key == "build":
			var build map[string]json.RawMessage
			if json.Unmarshal(raw, &build) == nil {
				for sub := range build {
					if !devcontainerBuildHonoured[sub] {
						add("build."+sub, "")
					}
				}
			}
		case devcontainerHonoured[key], devcontainerInert[key]:
		case key == "features":
			add(key, featureNames(raw))
		default:
			add(key, "")
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// featureNames is the sorted, comma-joined feature ids of a "features" value
// ("" when it is not an object).
func featureNames(raw json.RawMessage) string {
	var features map[string]json.RawMessage
	if json.Unmarshal(raw, &features) != nil {
		return ""
	}
	names := make([]string, 0, len(features))
	for k := range features {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// devcontainerVarRefs names the honoured fields (dotted paths, sorted) whose
// string values use a ${...} variable (${localEnv:X}, ${containerWorkspaceFolder}
// ...), which ctxloom does not substitute. Unhonoured fields are not scanned:
// they are already reported as having no effect.
func devcontainerVarRefs(top map[string]json.RawMessage) []string {
	var out []string
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case string:
			if strings.Contains(x, "${") {
				out = append(out, path)
			}
		case []any:
			for _, e := range x {
				walk(path, e)
			}
		case map[string]any:
			for k, e := range x {
				walk(path+"."+k, e)
			}
		}
	}
	for key, raw := range top {
		if !devcontainerHonoured[key] {
			continue
		}
		var v any
		if json.Unmarshal(raw, &v) != nil {
			continue
		}
		if m, ok := v.(map[string]any); ok && key == "build" {
			for sub, e := range m {
				if devcontainerBuildHonoured[sub] {
					walk("build."+sub, e)
				}
			}
			continue
		}
		walk(key, v)
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// warnDevcontainerUnhonoured warns ONCE per build (WarnRemedyOnce: the base
// is resolved several times in one build, with identical text each time)
// naming every present-but-unhonoured field with its reason, and any ${...}
// variable in an honoured field. Never fatal: the base still builds from
// image/build.
func warnDevcontainerUnhonoured(path string, top map[string]json.RawMessage) {
	fields := unhonouredDevcontainerFields(top)
	vars := devcontainerVarRefs(top)
	if len(fields) == 0 && len(vars) == 0 {
		return
	}
	var parts []string
	if len(fields) > 0 {
		names := make([]string, 0, len(fields))
		for _, f := range fields {
			names = append(names, f.String())
		}
		parts = append(parts, fmt.Sprintf(devcontainerUnhonouredNotice, strings.Join(names, "; ")))
	}
	if len(vars) > 0 {
		parts = append(parts, fmt.Sprintf(devcontainerVarsNotice, strings.Join(vars, ", ")))
	}
	msg := path + ": " + strings.Join(parts, "; and ")
	clidiag.WarnRemedyOnce("ctxloom", devcontainerUnhonouredRemedy, "%s", msg)
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
		return nil, fmt.Errorf(devcontainerComposeServiceError, devcontainerPath)
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
