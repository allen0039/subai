// Package rules embeds the versioned default rule packages (§15 rules/defaults).
package rules

import _ "embed"

//go:embed defaults/rules.yaml
var defaults []byte

// Defaults returns the built-in rule package bytes.
func Defaults() []byte { return defaults }
