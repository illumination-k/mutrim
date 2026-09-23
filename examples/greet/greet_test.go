package greet

import (
	_ "embed"
	"strings"
	"testing"
)

// gopher is the expected greeting, embedded so mutation_test must carry the
// embedsrcs of the test sources as well as the library's.
//
//go:embed testdata/gopher.golden
var gopher string

func TestGreet(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"", "Hello, world!"},
		{"gopher", strings.TrimSpace(gopher)},
	} {
		if got := Greet(tc.name); got != tc.want {
			t.Errorf("Greet(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}
