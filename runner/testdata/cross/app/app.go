// Package app imports the lib fixture; its tests kill the mutants of
// lib.Clamp that lib's own test cannot.
package app

import "github.com/illumination-k/mutrim/runner/testdata/cross/lib"

// Percent limits x to [0, 100].
func Percent(x int) int {
	return lib.Clamp(x, 0, 100)
}
