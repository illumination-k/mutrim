package equivalent

// Identity: every swap whose operators both leave x unchanged is
// equivalent; the others are not.
func Identity(x int, f float64, u uint) (int, float64, uint) {
	a := x + 0 - 0*x
	b := x*1 + x/1
	c := x<<0 + 0 - x>>0
	d := f*1 + f/1 + f + 0
	e := u + 0
	x += 0
	x *= 1
	return a + b + c + x, d, e
}

// NonNegative: len, cap and unsigned values compared with a constant.
func NonNegative(s []int, u uint) bool {
	return len(s) > -1 && -1 < cap(s) && len(s) >= 0 && u > 0
}
