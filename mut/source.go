package mut

import _ "embed"

// Source is mut.go, the whole runtime: stdlib-only and self-contained, so
// `mutrim test` can write it into the build overlay under a module-internal
// import path and the target module never depends on mutrim.
//
//go:embed mut.go
var Source []byte
