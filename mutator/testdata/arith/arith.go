package arith

func Ops(a, b int) int {
	return a + b - a*b + a/b + a%b
}

// Concat's "-" mutant does not type-check.
func Concat(s string) string {
	return s + "!"
}

// Zero's "/" mutant is a constant division by zero.
func Zero() int {
	return 10 * 0
}
