package mutname

// mut1 takes the first fallback runtime import name; mut, the default, is
// only bound inside Above, where the package scope does not see it.
var mut1 = 1

// Above is documented; the schemata source drops this comment but keeps
// the directive below it.
//
//go:noinline
func Above(x int) bool {
	mut := 3
	return x > mut+mut1
}
