package logical

func Both(a, b bool) bool {
	return a && b || !a
}
