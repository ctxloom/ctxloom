package main

import _ "embed"

// defaultRules is the rule set shipped with ltk and written by `ltk manage
// install` unless --no-default-rules is given. sample.ltk.yaml is generated from
// docs/DEFAULTS.md (the source of truth) by tools/extract-defaults and kept in
// sync by the lefthook pre-commit hook.
//
//go:embed sample.ltk.yaml
var defaultRules string

// minimalRules is written instead when --no-default-rules is given: a valid but
// empty config for the user to fill in. Both templates are separate files
// embedded at compile time.
//
//go:embed empty.ltk.yaml
var minimalRules string
