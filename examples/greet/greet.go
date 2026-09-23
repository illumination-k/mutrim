// Package greet is a fixture for go:embed: its greeting comes from an
// embedded file, so the schemata library must carry the embedded sources.
package greet

import (
	_ "embed"
	"strings"
)

//go:embed templates/hello.txt
var hello string

// Greet returns the embedded greeting for name, or for "world" when name is
// empty.
func Greet(name string) string {
	if name == "" {
		name = "world"
	}
	return strings.ReplaceAll(strings.TrimSpace(hello), "{name}", name)
}
