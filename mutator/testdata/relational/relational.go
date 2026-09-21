package relational

func Cmp(a, b int) (lt, le, gt, ge, eq, ne bool) {
	return a < b, a <= b, a > b, a >= b, a == b, a != b
}
