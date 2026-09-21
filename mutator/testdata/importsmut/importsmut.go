// Package importsmut already imports the runtime; the schemata lowering
// imports it again under another name.
package importsmut

import "github.com/illumination-k/mutrim/mut"

func Less(a, b int) bool { return a < b && !mut.Active("") }
